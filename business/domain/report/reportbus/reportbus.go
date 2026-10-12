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
	Contents(ctx context.Context, actor, orgID types.ID) ([]tenancybus.Account, []tenancybus.Project, error)
}

// Ledger is where the parts are, and what the ledger already reckons that a
// report shows beside them.
type Ledger interface {
	OrgPeriod(ctx context.Context, actor, orgID types.ID, from, to types.Date) ([]ledgerbus.ProjectLine, []ledgerbus.Total, error)
	AccountPeriod(ctx context.Context, actor, accountID types.ID, from, to types.Date) ([]ledgerbus.ProjectLine, error)
	Settling(ctx context.Context, actor types.ID, owner types.Scope) (ledgerbus.Settling, error)
	Coverage(ctx context.Context, actor, accountID types.ID, today types.Date) ([]ledgerbus.MonthCover, error)
}

// Business is the reports.
type Business struct {
	access Access
	ledger Ledger
}

// NewBusiness constructs one.
func NewBusiness(access Access, ledger Ledger) *Business {
	return &Business{access: access, ledger: ledger}
}

// --- the books a report is of ---------------------------------------------------

// Books is a scope whose money a report adds up: an organization, or an
// account on its own.
type Books struct {
	Scope  types.Scope
	Name   string
	Access tenancybus.Access

	// FiscalStart is the month the books' year starts in, for the year to
	// date: the organization's, January for an account on its own.
	FiscalStart int

	// Accounts is every account of the books, archived ones included,
	// accounts for reference not: those a report may be of.
	Accounts []tenancybus.Account
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

// The dimensions. Category groups and payees join them when the reports
// that read them do (docs/reports.md, build order).
const (
	ByKind     Dimension = "kind"
	ByCategory Dimension = "category"
	ByProject  Dimension = "project"
	ByAccount  Dimension = "account"
	ByMonth    Dimension = "month"
)

// Dimensions is every one, for reading one from an address.
var Dimensions = []Dimension{ByKind, ByCategory, ByProject, ByAccount, ByMonth}

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

	lines []ledgerbus.ProjectLine
}

// Sum answers a question, for anyone who may read the scope.
func (b *Business) Sum(ctx context.Context, actor types.ID, q Question) (Answer, error) {
	a, err := b.read(ctx, actor, q)
	if err != nil {
		return Answer{}, err
	}

	a.Cells = sum(a.lines, q.By)

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
		got := keyOf(l, q.By)
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
	default:
		a.lines, err = b.ledger.AccountPeriod(ctx, actor, q.Scope.ID, from, to)
	}

	if err != nil {
		return Answer{}, err
	}

	in := make(map[types.ID]bool, len(a.Chosen))
	for _, acct := range a.Chosen {
		in[acct.ID] = true
	}

	a.lines = slices.DeleteFunc(a.lines, func(l ledgerbus.ProjectLine) bool { return !in[l.AccountID] })

	for _, l := range a.lines {
		m := l.PostedOn.String()[:7]
		if a.First == "" || m < a.First {
			a.First = m
		}

		a.Last = max(a.Last, m)
	}

	return a, nil
}

// kindOf is the kind a part counts as: its category's, or not sorted yet
// when it has none.
func kindOf(l ledgerbus.ProjectLine) categorybus.Kind {
	if l.Split.CategoryID.Zero() {
		return categorybus.Unsaid
	}

	return l.CategoryKind
}

// keyOf is a part's group under some dimensions.
func keyOf(l ledgerbus.ProjectLine, by []Dimension) Key {
	k := Key{Currency: l.Currency}

	for _, d := range by {
		switch d {
		case ByKind:
			k.Kind = kindOf(l)
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

// sum groups the parts.
func sum(lines []ledgerbus.ProjectLine, by []Dimension) []Cell {
	index := map[Key]int{}

	var out []Cell

	for _, l := range lines {
		k := keyOf(l, by)

		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i

			c := Cell{Key: k}
			if slices.Contains(by, ByCategory) {
				c.Category = l.CategoryName
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
			strings.Compare(strings.ToLower(a.Category), strings.ToLower(b.Category)),
			strings.Compare(a.CategoryID.String(), b.CategoryID.String()),
			strings.Compare(a.ProjectID.String(), b.ProjectID.String()),
			strings.Compare(a.AccountID.String(), b.AccountID.String()),
			strings.Compare(a.Month, b.Month),
		)
	})

	return out
}
