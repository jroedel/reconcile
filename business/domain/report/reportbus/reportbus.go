// Package reportbus is reports (docs/reports.md): an engine that adds up the
// parts of a scope's books by any of a few dimensions, and the styles --
// each one layout, written here and reviewed by pull request -- that ask it
// a question and shape the answer into what a page shows.
//
// The engine adds no counting rule of its own. Its parts are the ledger's
// (ledgerbus.OrgPeriod, AccountPeriod), the same ones a project's book and
// a budget read, so that a report never disagrees with them: a pending
// charge that has posted counts once, an archived account's history counts
// in the months it was used, and an account for reference counts nowhere
// (docs/plan.md, "Whose books"). Sums are signed within a kind, as
// everywhere, so a refund lowers its expense. Nothing is converted between
// currencies, and nothing it adds up is stored: sorting a charge changes the
// next report at once.
//
// A builder on the web, if one ever comes, would offer what Question
// offers. Until then, a person chooses a style and its few choices; nobody
// types a query.
package reportbus

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// ErrNotFound is a scope the actor holds no role on, one that does not
// exist, or one no style takes: the same answer for each, as everywhere.
var ErrNotFound = tenancybus.ErrNotFound

// Access is how this domain asks who may do what (tenancybus).
type Access interface {
	Org(ctx context.Context, actor, id types.ID) (tenancybus.Org, tenancybus.Access, error)
	Account(ctx context.Context, actor, id types.ID) (tenancybus.Account, tenancybus.Access, error)
	Project(ctx context.Context, actor, id types.ID) (tenancybus.Project, tenancybus.Access, error)
	Contents(ctx context.Context, actor, orgID types.ID) ([]tenancybus.Account, []tenancybus.Project, error)
}

// Ledger is where the parts are, and what the ledger already reckons that a
// report shows beside them.
type Ledger interface {
	OrgPeriod(ctx context.Context, actor, orgID types.ID, from, to types.Date) ([]ledgerbus.ProjectLine, []ledgerbus.Total, error)
	AccountPeriod(ctx context.Context, actor, accountID types.ID, from, to types.Date) ([]ledgerbus.ProjectLine, error)
	ProjectBook(ctx context.Context, actor, projectID types.ID) (ledgerbus.Book, error)
	Settling(ctx context.Context, actor types.ID, owner types.Scope) (ledgerbus.Settling, error)
	Coverage(ctx context.Context, actor, accountID types.ID, today types.Date) ([]ledgerbus.MonthCover, error)
}

// Categories is the books' categories and the groups they are under
// (categorybus), read for grouping and asked no permission: the books
// have been asked already.
type Categories interface {
	Of(ctx context.Context, owner types.Scope) ([]categorybus.Category, error)
	HeadingsOf(ctx context.Context, owner types.Scope) ([]categorybus.Heading, error)
}

// Business is the reports.
type Business struct {
	access     Access
	ledger     Ledger
	categories Categories
	store      Storer
}

// NewBusiness constructs one.
func NewBusiness(access Access, ledger Ledger, categories Categories, store Storer) *Business {
	return &Business{access: access, ledger: ledger, categories: categories, store: store}
}

// --- the books a report is of ---------------------------------------------------

// Books is a scope whose money a report adds up: an organization, an
// account on its own, or a project, whose books are the parts sorted into
// it from every account it draws on.
type Books struct {
	Scope  types.Scope
	Name   string
	Access tenancybus.Access

	// FiscalStart is the month the books' year starts in, for the year to
	// date: the organization's, January for an account on its own.
	FiscalStart int

	// Accounts is every account of the books, archived ones included,
	// accounts for reference not: those a report may be of. None for a
	// project, whose accounts are whichever its parts are in.
	Accounts []tenancybus.Account

	// BreakEven is a project whose money should cover itself
	// (tenancybus.Project.BreakEven): its report is read in runs.
	BreakEven bool
}

// Books is a scope's books, for anyone who may read the scope. An account
// in an organization is not books of its own; its organization's report,
// with that account chosen, is its report.
func (b *Business) Books(ctx context.Context, actor types.ID, scope types.Scope) (Books, error) {
	switch scope.Kind {
	case types.ScopeOrg:
		o, access, err := b.access.Org(ctx, actor, scope.ID)
		if err != nil {
			return Books{}, err
		}

		accounts, _, err := b.access.Contents(ctx, actor, scope.ID)
		if err != nil {
			return Books{}, err
		}

		accounts = slices.DeleteFunc(accounts, func(a tenancybus.Account) bool { return a.Reference })

		return Books{Scope: scope, Name: o.Name, Access: access, FiscalStart: o.FiscalStart, Accounts: accounts}, nil
	case types.ScopeAccount:
		a, access, err := b.access.Account(ctx, actor, scope.ID)
		if err != nil {
			return Books{}, err
		}

		if !a.OrgID.Zero() {
			return Books{}, ErrNotFound
		}

		return Books{Scope: scope, Name: a.Name, Access: access, FiscalStart: 1, Accounts: []tenancybus.Account{a}}, nil
	case types.ScopeProject:
		// A project's year is January's: a role on a project need not
		// reach its organization, and its reports are of its life rather
		// than of a budget year.
		p, access, err := b.access.Project(ctx, actor, scope.ID)
		if err != nil {
			return Books{}, err
		}

		return Books{Scope: scope, Name: p.Name, Access: access, FiscalStart: 1, BreakEven: p.BreakEven}, nil
	}

	return Books{}, ErrNotFound
}

// chosen is the books' accounts that are among ids, or every one when ids
// names none of them: an address with an account since removed still
// shows a report, of everything, rather than of nothing.
func (bk Books) chosen(ids []types.ID) []tenancybus.Account {
	out := slices.DeleteFunc(slices.Clone(bk.Accounts), func(a tenancybus.Account) bool { return !slices.Contains(ids, a.ID) })
	if len(out) == 0 {
		return bk.Accounts
	}

	return out
}

// --- the engine --------------------------------------------------------------------

// Dimension is a way the engine groups parts.
type Dimension string

// The dimensions. A category's group is the heading it is under
// (categorybus.Heading), zero for one under none. Payees join them when a
// report that reads them does (docs/reports.md, build order).
const (
	ByKind     Dimension = "kind"
	ByGroup    Dimension = "group"
	ByCategory Dimension = "category"
	ByProject  Dimension = "project"
	ByAccount  Dimension = "account"
	ByMonth    Dimension = "month"
)

// Dimensions is every one, for reading one from an address.
var Dimensions = []Dimension{ByKind, ByGroup, ByCategory, ByProject, ByAccount, ByMonth}

// Question is what the engine is asked: the parts of a scope's books in a
// period, of some of its accounts or all of them, grouped by some
// dimensions. Currency is always one of them, since nothing is added
// across currencies.
type Question struct {
	Scope    types.Scope
	Accounts []types.ID // none: every account of the books
	Period   Period     // zero is the same as All here; a style defaults it first
	By       []Dimension
}

// Key is a group of parts: the value of each dimension asked for, zero for
// those not asked for. A Kind of categorybus.Unsaid is "not sorted yet": a
// part in no category, or in one whose kind its owner has not said.
type Key struct {
	Kind       categorybus.Kind
	GroupID    types.ID
	CategoryID types.ID
	ProjectID  types.ID
	AccountID  types.ID
	Month      string
	Currency   string
}

// Cell is the sum of one group. In and Out are its money in and out apart,
// both as signed as Sum, so that how much moved is In - Out whichever way.
type Cell struct {
	Key
	Category string // the category's name, when grouped by category
	Group    string // the group's name, when grouped by group and it has one
	Sum      money.Amount
	In, Out  money.Amount
	Count    int
}

// Answer is the engine's answer: the books, the accounts asked about, the
// months that have money, and a cell per group that has parts, ordered by
// key.
type Answer struct {
	Books
	Chosen      []tenancybus.Account
	Period      Period
	First, Last string // the earliest and latest months with parts; "" with none

	Cells []Cell

	// Headings is the books' groups, by ID, archived ones too: a report
	// of last year names the groups it was read under.
	Headings map[types.ID]categorybus.Heading

	lines   []ledgerbus.ProjectLine
	groupOf map[types.ID]types.ID // a category's group
}

// Frame is what every style's report has around its figures: the books
// and the period, and the periods a page offers beside it -- Before and
// After the same length either side, zero for every month.
type Frame struct {
	Answer
	Presets       []Preset
	Before, After Period
}

// frame is the frame of an answer for a period, as of today.
func frame(a Answer, today types.Date) Frame {
	f := Frame{Answer: a, Presets: Presets(today, a.FiscalStart)}
	if !a.Period.All {
		f.Before, f.After = a.Period.Shifted(-1), a.Period.Shifted(1)
	}

	return f
}

// Sum answers a question, for anyone who may read the scope.
func (b *Business) Sum(ctx context.Context, actor types.ID, q Question) (Answer, error) {
	a, err := b.read(ctx, actor, q)
	if err != nil {
		return Answer{}, err
	}

	a.Cells = sum(a.lines, q.By, a.groupOf, a.Headings)

	return a, nil
}

// Parts is the parts of one group of a question's answer, oldest first:
// what a figure on a report is made of. A dimension of k that the question
// does not group by is not asked of the parts, and neither is its currency
// when k names none.
func (b *Business) Parts(ctx context.Context, actor types.ID, q Question, k Key) (Answer, []ledgerbus.ProjectLine, error) {
	a, err := b.read(ctx, actor, q)
	if err != nil {
		return Answer{}, nil, err
	}

	var out []ledgerbus.ProjectLine

	for _, l := range a.lines {
		got := keyOf(l, q.By, a.groupOf)
		if k.Currency == "" {
			got.Currency = ""
		}

		if got == k {
			out = append(out, l)
		}
	}

	return a, out, nil
}

// read is the question's books and parts.
func (b *Business) read(ctx context.Context, actor types.ID, q Question) (Answer, error) {
	books, err := b.Books(ctx, actor, q.Scope)
	if err != nil {
		return Answer{}, err
	}

	a := Answer{Books: books, Chosen: books.chosen(q.Accounts), Period: q.Period}
	from, to := q.Period.span()

	switch q.Scope.Kind {
	case types.ScopeOrg:
		a.lines, _, err = b.ledger.OrgPeriod(ctx, actor, q.Scope.ID, from, to)
	case types.ScopeProject:
		a.lines, err = b.projectLines(ctx, actor, q.Scope.ID, from, to)
	default:
		a.lines, err = b.ledger.AccountPeriod(ctx, actor, q.Scope.ID, from, to)
	}

	if err != nil {
		return Answer{}, err
	}

	a.Headings = map[types.ID]categorybus.Heading{}

	if q.Scope.Kind != types.ScopeProject {
		if err := b.accountsAndGroups(ctx, &a); err != nil {
			return Answer{}, err
		}
	}

	a.First, a.Last = months(a.lines)

	return a, nil
}

// projectLines is a project's parts posted from from to before to: its
// book's, which are the ones its page lists, from every account it draws
// on.
func (b *Business) projectLines(ctx context.Context, actor, projectID types.ID, from, to types.Date) ([]ledgerbus.ProjectLine, error) {
	book, err := b.ledger.ProjectBook(ctx, actor, projectID)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(book.Lines, func(l ledgerbus.ProjectLine) bool {
		return l.PostedOn.Before(from) || !l.PostedOn.Before(to)
	}), nil
}

// accountsAndGroups keeps only the chosen accounts' parts, and reads the
// books' categories' groups. A project has neither: its accounts are
// whichever its parts are in, and its categories are its organization's,
// whose groups its report does not read by.
func (b *Business) accountsAndGroups(ctx context.Context, a *Answer) error {
	in := make(map[types.ID]bool, len(a.Chosen))
	for _, acct := range a.Chosen {
		in[acct.ID] = true
	}

	a.lines = slices.DeleteFunc(a.lines, func(l ledgerbus.ProjectLine) bool { return !in[l.AccountID] })

	cats, err := b.categories.Of(ctx, a.Scope)
	if err != nil {
		return err
	}

	a.groupOf = make(map[types.ID]types.ID, len(cats))
	for _, c := range cats {
		if !c.GroupID.Zero() {
			a.groupOf[c.ID] = c.GroupID
		}
	}

	headings, err := b.categories.HeadingsOf(ctx, a.Scope)
	if err != nil {
		return err
	}

	for _, h := range headings {
		a.Headings[h.ID] = h
	}

	return nil
}

// kindOf is the kind a part counts as: its category's, or not sorted yet
// when it has none.
func kindOf(l ledgerbus.ProjectLine) categorybus.Kind {
	if l.Split.CategoryID.Zero() {
		return categorybus.Unsaid
	}

	return l.CategoryKind
}

// keyOf is a part's place under some dimensions.
func keyOf(l ledgerbus.ProjectLine, by []Dimension, groupOf map[types.ID]types.ID) Key {
	k := Key{Currency: l.Currency}

	for _, d := range by {
		switch d {
		case ByKind:
			k.Kind = kindOf(l)
		case ByGroup:
			k.GroupID = groupOf[l.Split.CategoryID]
		case ByCategory:
			k.CategoryID = l.Split.CategoryID
		case ByProject:
			k.ProjectID = l.Split.ProjectID
		case ByAccount:
			k.AccountID = l.AccountID
		case ByMonth:
			k.Month = l.PostedOn.String()[:7]
		}
	}

	return k
}

// sum adds the parts up by the dimensions.
func sum(lines []ledgerbus.ProjectLine, by []Dimension, groupOf map[types.ID]types.ID, headings map[types.ID]categorybus.Heading) []Cell {
	index := map[Key]int{}

	var out []Cell

	for _, l := range lines {
		k := keyOf(l, by, groupOf)

		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i

			c := Cell{Key: k}
			if slices.Contains(by, ByCategory) {
				c.Category = l.CategoryName
			}

			if !k.GroupID.Zero() {
				c.Group = headings[k.GroupID].Name
			}

			out = append(out, c)
		}

		c := &out[i]
		c.Sum += l.Split.Amount
		c.Count++

		if l.Split.Amount > 0 {
			c.In += l.Split.Amount
		} else {
			c.Out += l.Split.Amount
		}
	}

	slices.SortFunc(out, func(a, b Cell) int {
		return cmp.Or(
			strings.Compare(a.Currency, b.Currency),
			strings.Compare(string(a.Kind), string(b.Kind)),
			strings.Compare(strings.ToLower(a.Group), strings.ToLower(b.Group)),
			strings.Compare(a.GroupID.String(), b.GroupID.String()),
			strings.Compare(strings.ToLower(a.Category), strings.ToLower(b.Category)),
			strings.Compare(a.CategoryID.String(), b.CategoryID.String()),
			strings.Compare(a.ProjectID.String(), b.ProjectID.String()),
			strings.Compare(a.AccountID.String(), b.AccountID.String()),
			strings.Compare(a.Month, b.Month),
		)
	})

	return out
}
