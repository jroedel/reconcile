package ledgerbus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The actions written into a project's history when money is put into it
// or taken out. Not into the account's: sorting a month of transactions is
// a hundred changes, and an account's history is where a person looks for
// the five that mattered. A project's is where an accountant asks who moved
// this charge into the pilgrimage, and when.
const (
	SplitAdded   eventbus.Action = "split.added"
	SplitRemoved eventbus.Action = "split.removed"
)

// MaxParts is the most parts a transaction may be split into. A receipt
// from a shop with twenty lines is still sorted into a handful of
// categories.
const MaxParts = 20

// MaxMemo is the longest memo on a part.
const MaxMemo = 500

// Invalid is a part that cannot be saved: which part, counting from zero,
// and what is wrong with it. Index is -1 for a problem with the parts
// together, such as their sum.
type Invalid struct {
	Index int
	Field string
	Err   error
}

func (e Invalid) Error() string { return fmt.Sprintf("part %d, %s: %v", e.Index+1, e.Field, e.Err) }
func (e Invalid) Unwrap() error { return e.Err }

// Part is one part as a person chose it.
type Part struct {
	Amount     money.Amount
	CategoryID types.ID
	ProjectID  types.ID
	Memo       string
}

// Categories is the lists a transaction's parts are sorted with
// (categorybus).
type Categories interface {
	ForAccount(ctx context.Context, a tenancybus.Account) ([]categorybus.Category, error)
}

// Editor is a transaction and everything its page needs to show it and
// offer to change it.
type Editor struct {
	Transaction Transaction
	Account     tenancybus.Account
	Access      tenancybus.Access

	// Categories is the account's list, archived ones only where a part
	// already has them.
	Categories []categorybus.Category

	// Projects is those the actor keeps the books of, and any a part is
	// already in -- including one the actor cannot otherwise see, named
	// so that a person knows where the money is.
	Projects []tenancybus.Project

	// Lock is the reconciliation whose period holds the transaction, if
	// one does: its parts cannot change until that is reopened.
	Lock Reconciliation
}

// CanSort reports whether the parts may be changed by this reader now.
func (e Editor) CanSort() bool { return e.Access.Can(tenancybus.Bookkeep) && !e.Lock.Made() }

// Grouped is the category choices a kind at a time.
func (e Editor) Grouped() []categorybus.Group { return categorybus.Grouped(e.Categories) }

// CategoryName and ProjectName name a part's choices for a page.
func (e Editor) CategoryName(id types.ID) string {
	for _, c := range e.Categories {
		if c.ID == id {
			return c.Name
		}
	}

	return ""
}

func (e Editor) ProjectName(id types.ID) string {
	for _, p := range e.Projects {
		if p.ID == id {
			return p.Name
		}
	}

	return ""
}

// Transaction is one transaction with its parts, and the choices for them.
func (b *Business) Transaction(ctx context.Context, actor, id types.ID) (Editor, error) {
	t, err := b.store.TransactionByID(ctx, id)
	if err != nil {
		return Editor{}, err
	}

	account, access, err := b.accounts.Account(ctx, actor, t.AccountID)
	if err != nil {
		return Editor{}, err
	}

	e := Editor{Transaction: t, Account: account, Access: access}

	if e.Lock, err = b.lock(ctx, t.AccountID, t.PostedOn); err != nil {
		return Editor{}, err
	}

	cats, err := b.categories.ForAccount(ctx, account)
	if err != nil {
		return Editor{}, err
	}

	used := map[types.ID]bool{}
	for _, s := range t.Splits {
		used[s.CategoryID] = true
	}

	for _, c := range cats {
		if !c.Archived() || used[c.ID] {
			e.Categories = append(e.Categories, c)
		}
	}

	if e.Projects, err = b.projectChoices(ctx, actor, t, access); err != nil {
		return Editor{}, err
	}

	return e, nil
}

// projectChoices is the projects a part may be put in, and those parts are
// in already.
func (b *Business) projectChoices(ctx context.Context, actor types.ID, t Transaction, access tenancybus.Access) ([]tenancybus.Project, error) {
	var out []tenancybus.Project

	if access.Can(tenancybus.Bookkeep) {
		mine, err := b.accounts.ProjectsFor(ctx, actor, tenancybus.Bookkeep)
		if err != nil {
			return nil, err
		}

		out = mine
	}

	var missing []types.ID

	for _, s := range t.Splits {
		if !s.ProjectID.Zero() && !slices.ContainsFunc(out, func(p tenancybus.Project) bool { return p.ID == s.ProjectID }) {
			missing = append(missing, s.ProjectID)
		}
	}

	if len(missing) > 0 {
		names, err := b.accounts.ProjectNames(ctx, missing)
		if err != nil {
			return nil, err
		}

		for _, id := range slices.Compact(missing) {
			out = append(out, tenancybus.Project{ID: id, Name: names[id]})
		}
	}

	return out, nil
}

// SetSplits replaces a transaction's parts.
//
// The parts must add up to the transaction, each one the same way round as
// it -- money out split into money out -- and none of them zero. A category
// must be on the account's list. A project must be one the actor keeps the
// books of, unless a part was in it already: a bookkeeper of the account
// who cannot see a project may still re-sort a transaction that somebody
// else put in it, without taking it out.
func (b *Business) SetSplits(ctx context.Context, now time.Time, actor, id types.ID, parts []Part) (Transaction, error) {
	e, err := b.Transaction(ctx, actor, id)
	if err != nil {
		return Transaction{}, err
	}

	if !e.Access.Can(tenancybus.Bookkeep) {
		return Transaction{}, ErrForbidden
	}

	if e.Lock.Made() {
		return Transaction{}, ErrLocked
	}

	t := e.Transaction

	if err := b.checkParts(ctx, actor, e, parts); err != nil {
		return Transaction{}, err
	}

	// Saved through a key, the parts say so until a person saves them on
	// the web, which says nothing and so clears it.
	via := eventbus.ViaFrom(ctx)

	splits := make([]Split, len(parts))
	for i, p := range parts {
		splits[i] = Split{
			ID: types.NewID(), TransactionID: t.ID, Position: i,
			Amount: p.Amount, CategoryID: p.CategoryID, ProjectID: p.ProjectID, Memo: strings.TrimSpace(p.Memo),
			Via: via,
		}
	}

	events := projectEvents(now, actor, e, t.Splits, splits)

	if err := b.store.ReplaceSplits(ctx, t.ID, splits, events); err != nil {
		return Transaction{}, err
	}

	t.Splits = splits

	return t, nil
}

var (
	errSum      = errors.New("the parts must add up to the whole")
	errAmount   = errors.New("each part needs an amount, the same way round as the whole")
	errCount    = errors.New("there must be at least one part, and not too many")
	errCategory = errors.New("that category is not on this account's list")
	errProject  = errors.New("that project is not one you keep the books of")
	errMemo     = errors.New("the memo is too long")
)

func (b *Business) checkParts(ctx context.Context, actor types.ID, e Editor, parts []Part) error {
	t := e.Transaction

	if len(parts) == 0 || len(parts) > MaxParts {
		return Invalid{Index: -1, Field: "count", Err: errCount}
	}

	already := map[types.ID]bool{}
	for _, s := range t.Splits {
		already[s.ProjectID] = true
	}

	var sum money.Amount

	for i, p := range parts {
		sum += p.Amount

		switch {
		case p.Amount == 0 && t.Amount != 0,
			p.Amount < 0 && t.Amount > 0,
			p.Amount > 0 && t.Amount < 0:
			return Invalid{Index: i, Field: "amount", Err: errAmount}
		case utf8.RuneCountInString(p.Memo) > MaxMemo:
			return Invalid{Index: i, Field: "memo", Err: errMemo}
		case !p.CategoryID.Zero() && e.CategoryName(p.CategoryID) == "":
			return Invalid{Index: i, Field: "category", Err: errCategory}
		}

		if p.ProjectID.Zero() || already[p.ProjectID] {
			continue
		}

		access, err := b.accounts.AccessTo(ctx, actor, types.ProjectScope(p.ProjectID))
		if err != nil {
			return err
		}

		if !access.Can(tenancybus.Bookkeep) || e.ProjectName(p.ProjectID) == "" {
			return Invalid{Index: i, Field: "project", Err: errProject}
		}
	}

	if sum != t.Amount {
		return Invalid{Index: -1, Field: "sum", Err: errSum}
	}

	return nil
}

// projectEvents is a line in each project's history for money that went
// into it or came out of it.
func projectEvents(now time.Time, actor types.ID, e Editor, before, after []Split) []eventbus.Event {
	total := func(splits []Split) map[types.ID]money.Amount {
		out := map[types.ID]money.Amount{}

		for _, s := range splits {
			if !s.ProjectID.Zero() {
				out[s.ProjectID] += s.Amount
			}
		}

		return out
	}

	was, is := total(before), total(after)

	ids := slices.Collect(maps.Keys(was))
	for id := range is {
		if _, ok := was[id]; !ok {
			ids = append(ids, id)
		}
	}

	slices.SortFunc(ids, func(a, b types.ID) int { return strings.Compare(a.String(), b.String()) })

	t := e.Transaction

	var events []eventbus.Event

	for _, id := range ids {
		if was[id] == is[id] {
			continue
		}

		detail := map[string]string{
			"account":     e.Account.Name,
			"currency":    e.Account.Currency,
			"date":        t.PostedOn.String(),
			"description": t.Description,
			"amount":      is[id].String(),
			"before":      was[id].String(),
		}

		action := SplitAdded
		if is[id] == 0 {
			action = SplitRemoved
		}

		events = append(events, eventbus.New(now, actor, types.ProjectScope(id), action, detail))
	}

	return events
}

// --- a project's book -------------------------------------------------------

// ProjectLine is one part of some account's transaction that is in a
// project.
type ProjectLine struct {
	Split          Split
	PostedOn       types.Date
	Description    string
	OwnDescription string
	Payee          string
	CheckMemo      string
	AccountID      types.ID
	AccountName    string
	Currency       string
	CategoryName   string
	CategoryKind   categorybus.Kind
}

// Total is money in one currency, under one heading -- a category's name,
// a month, or nothing for the whole -- as cash (money in and out) and as
// operations (by kind).
type Total struct {
	Key      string
	Currency string
	In, Out  money.Amount

	Operations
}

// Net is what the operations did: whether the project came out even.
// Transfers and pass-through are not in it, however much moved.
func (t Total) Net() money.Amount { return t.Operations.Net() }

// Book is a project's money, from every account it draws on.
//
// Totals are kept per currency: a pilgrimage paid partly from a dollar
// account and partly from a real one has two totals, and adding them would
// be a number that means nothing.
type Book struct {
	Project tenancybus.Project
	Access  tenancybus.Access

	Lines      []ProjectLine
	Totals     []Total
	ByCategory []Total
	ByMonth    []Total
}

// ProjectBook is a project's book. Anyone who may read the project may read
// it, including the parts of accounts they are given nothing on: a role on
// a project is exactly the right to see what is in it (docs/plan.md,
// "Inheritance").
func (b *Business) ProjectBook(ctx context.Context, actor, projectID types.ID) (Book, error) {
	p, access, err := b.accounts.Project(ctx, actor, projectID)
	if err != nil {
		return Book{}, err
	}

	lines, err := b.store.ProjectLines(ctx, projectID)
	if err != nil {
		return Book{}, err
	}

	book := Book{Project: p, Access: access, Lines: lines}

	book.Totals = totals(lines, func(ProjectLine) string { return "" })
	book.ByCategory = totals(lines, func(l ProjectLine) string { return l.CategoryName })
	book.ByMonth = totals(lines, func(l ProjectLine) string { return l.PostedOn.String()[:7] })

	return book, nil
}

// totals adds the lines up under a heading, per currency, in heading
// order.
// OrgPeriod is every part of an organization's accounts posted from from
// to before to, for anyone who may read the organization -- who may read
// every account in it. For an organization's budget year (budgetbus).
func (b *Business) OrgPeriod(ctx context.Context, actor, orgID types.ID, from, to types.Date) ([]ProjectLine, []Total, error) {
	access, err := b.accounts.AccessTo(ctx, actor, types.OrgScope(orgID))
	if err != nil {
		return nil, nil, err
	}

	if !access.Can(tenancybus.Read) {
		return nil, nil, ErrNotFound
	}

	lines, err := b.store.OrgLines(ctx, orgID, from, to)
	if err != nil {
		return nil, nil, err
	}

	return lines, totals(lines, func(ProjectLine) string { return "" }), nil
}

func totals(lines []ProjectLine, key func(ProjectLine) string) []Total {
	index := map[[2]string]int{}

	var out []Total

	for _, l := range lines {
		k := [2]string{key(l), l.Currency}

		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, Total{Key: k[0], Currency: k[1]})
		}

		if l.Split.Amount > 0 {
			out[i].In += l.Split.Amount
		} else {
			out[i].Out += l.Split.Amount
		}

		kind := l.CategoryKind
		if l.Split.CategoryID.Zero() {
			kind = categorybus.Unsaid
		}

		out[i].Add(kind, l.Split.Amount)
	}

	slices.SortStableFunc(out, func(a, b Total) int {
		if c := strings.Compare(a.Key, b.Key); c != 0 {
			return c
		}

		return strings.Compare(a.Currency, b.Currency)
	})

	return out
}
