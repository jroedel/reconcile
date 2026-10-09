package ledgerbus

import (
	"context"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// RulesApplied is the line in an account's history when "Sort what is not
// sorted yet" sorted something. An import says how many it sorted in its
// own line.
const RulesApplied eventbus.Action = "rules.applied"

// Rules is how the ledger reads an account's sorting rules (rulebus). The
// ledger has asked who may before it asks this.
type Rules interface {
	ForAccount(ctx context.Context, accountID types.ID) ([]rulebus.Rule, error)
}

// SortedPart is a part a rule sorted, and the lines of project history
// that go with it, for the store to write together or not at all.
type SortedPart struct {
	Split  Split
	Events []eventbus.Event
}

// RuleProblem is why a rule sorts nothing, or "" when it may.
type RuleProblem string

const (
	// RuleCategory: its category is archived, or no longer on the list.
	RuleCategory RuleProblem = "category"

	// RuleProject: its project is archived, or not one the person applying
	// it keeps the books of.
	RuleProject RuleProblem = "project"

	// RuleEnded and RuleNotStarted: the charge is outside the project's
	// dates. On the rules page, today is.
	RuleEnded      RuleProblem = "ended"
	RuleNotStarted RuleProblem = "not-started"
)

// sorter is what applying an account's rules needs, read once: the rules,
// the account's list, and the projects the person applying them keeps the
// books of -- which is what the transaction's own page would offer them
// (projectChoices), so a rule never puts money where its applier could not.
type sorter struct {
	account    tenancybus.Account
	actor      types.ID
	now        time.Time
	rules      []rulebus.Rule
	categories map[types.ID]categorybus.Category
	projects   map[types.ID]tenancybus.Project
}

func (b *Business) sorter(ctx context.Context, now time.Time, actor types.ID, account tenancybus.Account) (sorter, error) {
	s := sorter{account: account, actor: actor, now: now}

	var err error

	if s.rules, err = b.rules.ForAccount(ctx, account.ID); err != nil || len(s.rules) == 0 {
		return s, err
	}

	cats, err := b.categories.ForAccount(ctx, account)
	if err != nil {
		return s, err
	}

	s.categories = make(map[types.ID]categorybus.Category, len(cats))
	for _, c := range cats {
		s.categories[c.ID] = c
	}

	mine, err := b.accounts.ProjectsFor(ctx, actor, tenancybus.Bookkeep)
	if err != nil {
		return s, err
	}

	s.projects = make(map[types.ID]tenancybus.Project, len(mine))
	for _, p := range mine {
		s.projects[p.ID] = p
	}

	return s, nil
}

// problem is why the rule cannot sort a charge posted on that date.
func (s sorter) problem(r rulebus.Rule, on types.Date) RuleProblem {
	if !r.CategoryID.Zero() {
		if c, ok := s.categories[r.CategoryID]; !ok || c.Archived() {
			return RuleCategory
		}
	}

	if r.ProjectID.Zero() {
		return ""
	}

	p, ok := s.projects[r.ProjectID]

	switch {
	case !ok || p.Archived():
		return RuleProject
	case !p.EndsOn.Zero() && p.EndsOn.Before(on):
		return RuleEnded
	case !p.StartsOn.Zero() && on.Before(p.StartsOn):
		return RuleNotStarted
	}

	return ""
}

// blank reports whether a rule may still sort the transaction: one part,
// nothing chosen, no memo. Anything else is a person's, or was a rule's
// already. (The store also leaves out a reconciled period.)
func blank(t Transaction) bool {
	if len(t.Splits) != 1 {
		return false
	}

	sp := t.Splits[0]

	return sp.CategoryID.Zero() && sp.ProjectID.Zero() && sp.Memo == ""
}

// sort gives a blank transaction's part the choice of the one rule it
// meets, if there is one and it may be applied, and returns the part as
// sorted with its project history. ok is false when nothing applies.
func (s sorter) sort(t Transaction) (SortedPart, bool) {
	if !blank(t) {
		return SortedPart{}, false
	}

	r, ok, _ := rulebus.Pick(s.rules, t.Description, t.Amount)
	if !ok || s.problem(r, t.PostedOn) != "" {
		return SortedPart{}, false
	}

	before := t.Splits
	sp := before[0]
	sp.CategoryID, sp.ProjectID, sp.RuleID = r.CategoryID, r.ProjectID, r.ID

	e := Editor{Transaction: t, Account: s.account}

	return SortedPart{Split: sp, Events: projectEvents(s.now, s.actor, e, before, []Split{sp})}, true
}

// sortNew sorts the transactions a statement brings, in place, before they
// are stored, and returns their project history by transaction.
func (b *Business) sortNew(ctx context.Context, now time.Time, actor types.ID, account tenancybus.Account, txs []Transaction) (map[types.ID][]eventbus.Event, error) {
	s, err := b.sorter(ctx, now, actor, account)
	if err != nil || len(s.rules) == 0 {
		return nil, err
	}

	also := map[types.ID][]eventbus.Event{}

	for i := range txs {
		if p, ok := s.sort(txs[i]); ok {
			txs[i].Splits = []Split{p.Split}
			also[txs[i].ID] = p.Events
		}
	}

	return also, nil
}

// bookkept is the account, if the actor keeps its books: ErrNotFound for
// one they cannot read, ErrForbidden for one they can only read.
func (b *Business) bookkept(ctx context.Context, actor, accountID types.ID) (tenancybus.Account, error) {
	account, access, err := b.accounts.Account(ctx, actor, accountID)

	switch {
	case err != nil:
		return tenancybus.Account{}, err
	case !access.Can(tenancybus.Bookkeep):
		return account, ErrForbidden
	}

	return account, nil
}

// SortUnsorted applies the account's rules to every transaction of it that
// is still one part with nothing chosen, outside a reconciled period, and
// says how many it sorted. A bookkeeper's, like sorting one by hand.
func (b *Business) SortUnsorted(ctx context.Context, now time.Time, actor, accountID types.ID) (int, error) {
	account, err := b.bookkept(ctx, actor, accountID)
	if err != nil {
		return 0, err
	}

	s, err := b.sorter(ctx, now, actor, account)
	if err != nil || len(s.rules) == 0 {
		return 0, err
	}

	txs, err := b.store.Blank(ctx, accountID)
	if err != nil {
		return 0, err
	}

	var sorted []SortedPart

	for _, t := range txs {
		if p, ok := s.sort(t); ok {
			sorted = append(sorted, p)
		}
	}

	if len(sorted) == 0 {
		return 0, nil
	}

	return b.store.SortByRule(ctx, sorted, eventbus.New(now, actor, account.Scope(), RulesApplied, nil))
}

// RuleUse is one rule as its page shows it.
type RuleUse struct {
	Rule rulebus.Rule

	// Parts is how many parts it sorted that nobody has saved since.
	Parts int

	// Problem is why it sorts nothing now, if it does not.
	Problem RuleProblem

	// Waiting is how many transactions not sorted yet it would sort now.
	Waiting int
}

// Torn is a transaction not sorted yet that rules of the same length
// disagree about, so none of them sorts it.
type Torn struct {
	Transaction Transaction
	Rules       []rulebus.Rule
}

// Rulebook is an account's rules with what they did and would do.
type Rulebook struct {
	Account tenancybus.Account
	Rules   []RuleUse

	// Waiting is how many transactions "Sort what is not sorted yet" would
	// sort now.
	Waiting int

	Torn []Torn
}

// Rulebook is the account's rules for their page: a bookkeeper's, as the
// rules themselves are (rulebus).
func (b *Business) Rulebook(ctx context.Context, now time.Time, actor, accountID types.ID) (Rulebook, error) {
	account, err := b.bookkept(ctx, actor, accountID)
	if err != nil {
		return Rulebook{}, err
	}

	book := Rulebook{Account: account}

	s, err := b.sorter(ctx, now, actor, account)
	if err != nil || len(s.rules) == 0 {
		return book, err
	}

	counts, err := b.store.RuleCounts(ctx, accountID)
	if err != nil {
		return book, err
	}

	txs, err := b.store.Blank(ctx, accountID)
	if err != nil {
		return book, err
	}

	waiting := map[types.ID]int{}

	for _, t := range txs {
		r, ok, torn := rulebus.Pick(s.rules, t.Description, t.Amount)

		switch {
		case len(torn) > 0:
			book.Torn = append(book.Torn, Torn{Transaction: t, Rules: torn})
		case ok && s.problem(r, t.PostedOn) == "":
			waiting[r.ID]++
			book.Waiting++
		}
	}

	today := types.DateOf(now)

	for _, r := range s.rules {
		book.Rules = append(book.Rules, RuleUse{
			Rule: r, Parts: counts[r.ID], Problem: s.problem(r, today), Waiting: waiting[r.ID],
		})
	}

	return book, nil
}
