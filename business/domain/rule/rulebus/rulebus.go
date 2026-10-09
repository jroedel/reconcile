// Package rulebus is an account's sorting rules: text to look for in what
// the bank wrote, and the category and project a charge that has it is
// sorted into (docs/sorting.md).
//
// A rule is a person's decision written down once -- "SHELL OIL is Fuel",
// "the CAPITAL ONE AUTOPAY is a transfer" -- so that next month's statement
// arrives sorted. The ledger applies them (ledgerbus): to each new
// transaction at import, and on request to what is not sorted yet. This
// package keeps the rules, says which one a description meets, and asks
// nobody's permission but its own about them.
//
// # Matching
//
// Lifted in spirit from eumaeus' categorizebus: a rule is a piece of text,
// found anywhere in the description, ignoring case and runs of spaces, in a
// direction (money out, money in, or either -- a refund from the same payee
// is usually not the same thing as the charge). Of several, the longest
// text wins, because it is the more particular: "AMAZON WEB SERVICES" over
// "AMAZON". Two of the same length that disagree decide nothing, and the
// charge is left for a person, rather than one of two people's decisions
// winning silently.
//
// # What a rule never does
//
// It never changes what a person sorted, a transaction in more than one
// part, or anything in a reconciled period; and changing or removing a
// rule leaves what it already sorted as it is. Those are the ledger's to
// enforce, where the transactions are.
package rulebus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The errors a page tells apart, tenancy's for not found and not allowed.
var (
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden

	// ErrDuplicate is a second rule with the same text in one account,
	// which a store refuses if two people make it at once.
	ErrDuplicate = errors.New("the account already has a rule for that text")
)

// The actions this domain writes into the account's history.
const (
	Made    eventbus.Action = "rule.made"
	Changed eventbus.Action = "rule.changed"
	Removed eventbus.Action = "rule.removed"
)

// Limits on what a rule's text may be. At least three characters, so that
// "A" does not sort every charge with an A in it.
const (
	MinMatch = 3
	MaxMatch = 100
)

// Direction is which way the money must move for a rule to apply.
type Direction string

const (
	Out    Direction = "out"
	In     Direction = "in"
	Either Direction = "any"
)

// Directions is every direction, in the order a page offers them.
var Directions = []Direction{Out, In, Either}

// Rule is one rule of an account.
type Rule struct {
	ID        types.ID
	AccountID types.ID
	Match     string // as typed
	Direction Direction

	// At least one is set.
	CategoryID types.ID
	ProjectID  types.ID

	CreatedBy types.ID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Key is the text compared: lower case, spaces tidied.
func Key(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Meets reports whether a transaction with this description and amount
// meets the rule.
func (r Rule) Meets(description string, amount money.Amount) bool {
	switch {
	case r.Direction == Out && amount >= 0, r.Direction == In && amount <= 0:
		return false
	}

	return strings.Contains(Key(description), Key(r.Match))
}

// same reports whether two rules would sort a part the same way.
func same(a, b Rule) bool { return a.CategoryID == b.CategoryID && a.ProjectID == b.ProjectID }

// Pick is the rule a transaction meets, if exactly one decision does: the
// longest text, and of several as long, only when they agree. Torn names
// the disagreeing rules when the longest do not agree, so that a page can
// say so; nothing is picked then.
func Pick(rules []Rule, description string, amount money.Amount) (picked Rule, ok bool, torn []Rule) {
	var best []Rule

	longest := 0

	for _, r := range rules {
		if !r.Meets(description, amount) {
			continue
		}

		switch n := utf8.RuneCountInString(Key(r.Match)); {
		case n > longest:
			longest, best = n, []Rule{r}
		case n == longest:
			best = append(best, r)
		}
	}

	if len(best) == 0 {
		return Rule{}, false, nil
	}

	for _, r := range best[1:] {
		if !same(r, best[0]) {
			return Rule{}, false, best
		}
	}

	return best[0], true, nil
}

// --- the business -----------------------------------------------------------------

// Access is how this domain asks who may do what (tenancybus).
type Access interface {
	Account(ctx context.Context, actor, id types.ID) (tenancybus.Account, tenancybus.Access, error)
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
}

// Categories is the account's list a rule's category must be on.
type Categories interface {
	ForAccount(ctx context.Context, a tenancybus.Account) ([]categorybus.Category, error)
}

// Storer keeps the rules.
type Storer interface {
	Create(ctx context.Context, r Rule, ev eventbus.Event) error
	Update(ctx context.Context, r Rule, ev eventbus.Event) error
	Delete(ctx context.Context, id types.ID, ev eventbus.Event) error
	ByID(ctx context.Context, id types.ID) (Rule, error)
	Of(ctx context.Context, accountID types.ID) ([]Rule, error)
}

// Business is the set of operations on rules.
type Business struct {
	log        *slog.Logger
	store      Storer
	access     Access
	categories Categories
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, access Access, categories Categories) *Business {
	return &Business{log: log, store: store, access: access, categories: categories}
}

// account is the account and the check that the actor keeps its books:
// ErrNotFound for one they cannot read, ErrForbidden for one they can only
// read. Rules are a bookkeeper's tool, like sorting.
func (b *Business) account(ctx context.Context, actor, id types.ID) (tenancybus.Account, error) {
	a, access, err := b.access.Account(ctx, actor, id)

	switch {
	case err != nil:
		return tenancybus.Account{}, err
	case !access.Can(tenancybus.Bookkeep):
		return a, ErrForbidden
	}

	return a, nil
}

// Rules is an account's rules, by their text, and the account.
func (b *Business) Rules(ctx context.Context, actor, accountID types.ID) ([]Rule, tenancybus.Account, error) {
	a, err := b.account(ctx, actor, accountID)
	if err != nil {
		return nil, tenancybus.Account{}, err
	}

	rules, err := b.store.Of(ctx, accountID)

	return rules, a, err
}

// ForAccount is an account's rules, asking nobody's permission: for the
// ledger, which has asked already.
func (b *Business) ForAccount(ctx context.Context, accountID types.ID) ([]Rule, error) {
	return b.store.Of(ctx, accountID)
}

// Fields is what a person says about a rule.
type Fields struct {
	Match      string
	Direction  Direction
	CategoryID types.ID
	ProjectID  types.ID
}

// Invalid is a rule that will not do, and which field says why.
type Invalid struct {
	Field string // "match", "direction", "choice", "category" or "project"
	Err   error
}

func (e Invalid) Error() string { return e.Field + ": " + e.Err.Error() }

// check tidies the fields and refuses those that will not do: the text's
// length, the direction, something to set, a category on the account's
// list and not archived, a project the actor keeps the books of.
func (b *Business) check(ctx context.Context, actor types.ID, a tenancybus.Account, f Fields) (Fields, error) {
	f.Match = strings.Join(strings.Fields(f.Match), " ")

	switch n := utf8.RuneCountInString(f.Match); {
	case n < MinMatch || n > MaxMatch:
		return f, Invalid{Field: "match", Err: fmt.Errorf("give the text to look for, %d to %d characters", MinMatch, MaxMatch)}
	case f.Direction != Out && f.Direction != In && f.Direction != Either:
		return f, Invalid{Field: "direction", Err: errors.New("choose money out, money in or either")}
	case f.CategoryID.Zero() && f.ProjectID.Zero():
		return f, Invalid{Field: "choice", Err: errors.New("choose a category, a project or both")}
	}

	if !f.CategoryID.Zero() {
		cats, err := b.categories.ForAccount(ctx, a)
		if err != nil {
			return f, err
		}

		ok := false
		for _, c := range cats {
			ok = ok || (c.ID == f.CategoryID && !c.Archived())
		}

		if !ok {
			return f, Invalid{Field: "category", Err: errors.New("that category is not on this account's list")}
		}
	}

	if !f.ProjectID.Zero() {
		access, err := b.access.AccessTo(ctx, actor, types.ProjectScope(f.ProjectID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return f, err
		}

		if !access.Can(tenancybus.Bookkeep) {
			return f, Invalid{Field: "project", Err: errors.New("that project is not one you keep the books of")}
		}
	}

	return f, nil
}

func detail(r Rule) map[string]string { return map[string]string{"match": r.Match} }

// Save makes a rule, or corrects the account's rule with the same text:
// "always sort these this way", said twice, is one rule, the second time
// a correction.
func (b *Business) Save(ctx context.Context, now time.Time, actor, accountID types.ID, f Fields) (Rule, error) {
	a, err := b.account(ctx, actor, accountID)
	if err != nil {
		return Rule{}, err
	}

	if f, err = b.check(ctx, actor, a, f); err != nil {
		return Rule{}, err
	}

	rules, err := b.store.Of(ctx, accountID)
	if err != nil {
		return Rule{}, err
	}

	for _, r := range rules {
		if Key(r.Match) == Key(f.Match) {
			return b.update(ctx, now, actor, r, f)
		}
	}

	r := Rule{
		ID: types.NewID(), AccountID: accountID, Match: f.Match, Direction: f.Direction,
		CategoryID: f.CategoryID, ProjectID: f.ProjectID, CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}

	if err := b.store.Create(ctx, r, eventbus.New(now, actor, a.Scope(), Made, detail(r))); err != nil {
		return Rule{}, err
	}

	return r, nil
}

// Change rewrites a rule. What it already sorted stays as it is.
func (b *Business) Change(ctx context.Context, now time.Time, actor, id types.ID, f Fields) (Rule, error) {
	r, err := b.store.ByID(ctx, id)
	if err != nil {
		return Rule{}, err
	}

	a, err := b.account(ctx, actor, r.AccountID)
	if err != nil {
		return Rule{}, err
	}

	if f, err = b.check(ctx, actor, a, f); err != nil {
		return r, err
	}

	return b.update(ctx, now, actor, r, f)
}

func (b *Business) update(ctx context.Context, now time.Time, actor types.ID, r Rule, f Fields) (Rule, error) {
	r.Match, r.Direction, r.CategoryID, r.ProjectID, r.UpdatedAt = f.Match, f.Direction, f.CategoryID, f.ProjectID, now

	if err := b.store.Update(ctx, r, eventbus.New(now, actor, types.AccountScope(r.AccountID), Changed, detail(r))); err != nil {
		return Rule{}, err
	}

	return r, nil
}

// Remove deletes a rule. What it sorted stays sorted, and keeps saying a
// rule did it until a person saves it.
func (b *Business) Remove(ctx context.Context, now time.Time, actor, id types.ID) (Rule, error) {
	r, err := b.store.ByID(ctx, id)
	if err != nil {
		return Rule{}, err
	}

	if _, err := b.account(ctx, actor, r.AccountID); err != nil {
		return Rule{}, err
	}

	return r, b.store.Delete(ctx, id, eventbus.New(now, actor, types.AccountScope(r.AccountID), Removed, detail(r)))
}

// Payee is a description's likely payee: the words before the first that
// is mostly digits or a reference -- a store number, a date, a card's last
// four -- which would make a rule meet only this one charge. It prefills
// the text of a rule made from a transaction, and is what suggestions
// group earlier transactions by (suggest.go).
//
// Before the payee, the words a card network or a bank puts in front of
// it are passed over, and so is a leading reference: "POS 0712 CORNER
// GROCERY" and "SQ *COFFEE CART" are the grocery and the coffee cart, not
// "POS" and "SQ", which would group every card charge as one payee.
func Payee(description string) string {
	var words []string

	for w := range strings.FieldsSeq(description) {
		bare := strings.TrimLeft(w, "*")

		digits := 0
		for _, c := range bare {
			if c >= '0' && c <= '9' {
				digits++
			}
		}

		reference := bare == "" || digits*2 >= utf8.RuneCountInString(bare) || strings.ContainsAny(bare, "#*")

		switch {
		case len(words) == 0 && (reference || prefixes[strings.ToLower(bare)]):
			continue
		case reference:
		default:
			words = append(words, bare)

			continue
		}

		break
	}

	p := strings.Join(words, " ")
	if utf8.RuneCountInString(p) < MinMatch {
		p = strings.Join(strings.Fields(description), " ")
	}

	// Cut to what a rule may hold, so that the box is never filled with
	// text that cannot be saved.
	if r := []rune(p); len(r) > MaxMatch {
		p = strings.TrimSpace(string(r[:MaxMatch]))
	}

	return p
}

// prefixes are the words a card network or a bank puts before a payee.
// Kept short: a word wrongly here is a payee's first word lost.
var prefixes = map[string]bool{
	"pos": true, "debit": true, "checkcard": true, "purchase": true, "card": true,
	"sq": true, "tst": true, "ach": true, "recurring": true,
}
