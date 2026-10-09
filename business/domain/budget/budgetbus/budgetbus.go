// Package budgetbus is budgets: what a project's owners expect its money to
// do, and -- in a later step -- an organization's year (docs/budgets.md).
//
// A budget is lines of income and expense, one per category or a total
// for a kind, in one currency, set by the owners. What it is compared with
// is the ledger's: a project's book (ledgerbus.ProjectBook), asked with the
// reader's own access, so that a budget shows nobody money they could not
// see in the book.
//
// Only income and expenses are budgeted, because only they are operations
// (docs/plan.md, "Kinds of money"). Transfers and pass-through are left out
// of the comparison, and money not sorted yet is shown beside it rather than
// guessed into a line.
package budgetbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The errors a page tells apart, tenancy's for not found and not allowed.
var (
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden
)

// The actions this domain writes into a project's history.
const (
	Set     eventbus.Action = "budget.set"
	Removed eventbus.Action = "budget.removed"
)

// MaxLines is the most lines one budget may have: every category of a long
// list twice over.
const MaxLines = 200

// Line is one line of a budget.
type Line struct {
	ID    types.ID
	Scope types.Scope // a project's, or an organization's
	Year  int         // an organization's budget year; zero for a project

	// CategoryID is the category the line is for; zero for the total of
	// its kind.
	CategoryID types.ID
	Kind       categorybus.Kind // income or expense
	Amount     money.Amount     // positive: what is expected to come in, or go out
	Currency   string

	UpdatedBy types.ID
	UpdatedAt time.Time
}

// Access is how this domain asks who may do what (tenancybus).
type Access interface {
	Project(ctx context.Context, actor, id types.ID) (tenancybus.Project, tenancybus.Access, error)
	Overview(ctx context.Context, actor types.ID) (tenancybus.Overview, error)
}

// Ledger is where the money a budget is compared with is.
type Ledger interface {
	ProjectBook(ctx context.Context, actor, projectID types.ID) (ledgerbus.Book, error)
}

// Categories is the lists a budget's lines are in.
type Categories interface {
	Of(ctx context.Context, owner types.Scope) ([]categorybus.Category, error)
}

// Storer keeps the lines.
type Storer interface {
	Lines(ctx context.Context, scope types.Scope, year int) ([]Line, error)
	Replace(ctx context.Context, scope types.Scope, year int, lines []Line, events []eventbus.Event) error
}

// Business is the set of operations on budgets.
type Business struct {
	log        *slog.Logger
	store      Storer
	access     Access
	ledger     Ledger
	categories Categories
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, access Access, ledger Ledger, categories Categories) *Business {
	return &Business{log: log, store: store, access: access, ledger: ledger, categories: categories}
}

// --- comparing --------------------------------------------------------------

// Row is one line of a budget beside what happened, or a category that has
// money and no line. Budget and Actual both run the way the kind does:
// income received, expenses spent. A refund lowers its expense's Actual.
type Row struct {
	CategoryID types.ID // zero for a kind's total
	Name       string

	Budget   money.Amount
	Budgeted bool
	Actual   money.Amount

	// Moved is a line whose category is now of another kind than when the
	// line was set: the page says so rather than quietly moving the line.
	Moved bool
}

// Left is what is still to come in or to be spent; negative when the
// actual is past the budget.
func (r Row) Left() money.Amount { return r.Budget - r.Actual }

// Over reports whether the actual is past a budgeted line.
func (r Row) Over() bool { return r.Budgeted && r.Actual > r.Budget }

// Percent is the actual as a share of the budget, 0 to 100, for a bar's
// width. The words beside the bar say the figures.
func (r Row) Percent() int {
	if !r.Budgeted || r.Budget <= 0 || r.Actual <= 0 {
		return 0
	}

	return int(min(100, r.Actual*100/r.Budget))
}

// Side is one kind of a budget: its lines, its total, and the categories
// of that kind with money and no line.
type Side struct {
	Kind  categorybus.Kind
	Rows  []Row
	Total Row

	// Typed is the total an owner typed, if they did; Lines is the sum of
	// the category lines. The total budgeted is the larger: an owner who
	// typed $15,000 and has lines for $12,000 so far expects $15,000.
	Typed    money.Amount
	HasTyped bool
	Lines    money.Amount

	NotInBudget []Row
}

// Mismatch reports whether the lines and the typed total disagree, which
// the page says, since one of them is likely a mistake.
func (s Side) Mismatch() bool { return s.HasTyped && len(s.Rows) > 0 && s.Typed != s.Lines }

// Comparison is a budget beside what happened.
type Comparison struct {
	Project tenancybus.Project
	Access  tenancybus.Access

	// Currency is the budget's. Set reports whether it has any lines.
	Currency string
	Set      bool

	Income, Expenses Side

	// Unsorted is money in the project in the budget's currency that is in
	// no category, or one whose kind is not said yet: outside the
	// comparison, and shown beside it.
	Unsorted money.Amount

	// Elsewhere is the project's money in other currencies, by currency,
	// never converted.
	Elsewhere []ledgerbus.Total
}

// CanSet reports whether the reader may set the budget: an owner's.
func (c Comparison) CanSet() bool { return c.Access.Can(tenancybus.Manage) }

// ExpectedNet is budgeted income less budgeted expenses: whether the
// budget expects the project to come out even.
func (c Comparison) ExpectedNet() money.Amount {
	return c.Income.Total.Budget - c.Expenses.Total.Budget
}

// ActualNet is the same of what happened.
func (c Comparison) ActualNet() money.Amount { return c.Income.Total.Actual - c.Expenses.Total.Actual }

// Project is a project's budget beside its book, for anyone who may read
// the project.
func (b *Business) Project(ctx context.Context, actor, projectID types.ID) (Comparison, error) {
	book, err := b.ledger.ProjectBook(ctx, actor, projectID)
	if err != nil {
		return Comparison{}, err
	}

	lines, err := b.store.Lines(ctx, types.ProjectScope(projectID), 0)
	if err != nil {
		return Comparison{}, err
	}

	cats, err := b.choices(ctx, book)
	if err != nil {
		return Comparison{}, err
	}

	c := Comparison{Project: book.Project, Access: book.Access, Set: len(lines) > 0}

	if c.Currency = currencyOf(lines); c.Currency == "" {
		if c.Currency, err = b.defaultCurrency(ctx, actor, book); err != nil {
			return Comparison{}, err
		}
	}

	compare(&c, lines, book.Lines, cats)

	for _, t := range book.Totals {
		if t.Currency != c.Currency {
			c.Elsewhere = append(c.Elsewhere, t)
		}
	}

	return c, nil
}

func currencyOf(lines []Line) string {
	if len(lines) == 0 {
		return ""
	}

	return lines[0].Currency
}

// compare fills both sides from the lines and the project's parts in the
// budget's currency. cats names the categories, by ID, with their kinds now.
func compare(c *Comparison, lines []Line, parts []ledgerbus.ProjectLine, cats map[types.ID]categorybus.Category) {
	actual := map[types.ID]money.Amount{}         // by category, signed
	byKind := map[categorybus.Kind]money.Amount{} // signed

	for _, p := range parts {
		if p.Currency != c.Currency {
			continue
		}

		switch p.CategoryKind {
		case categorybus.Income, categorybus.Expense:
			actual[p.Split.CategoryID] += p.Split.Amount
			byKind[p.CategoryKind] += p.Split.Amount
		case categorybus.Transfer, categorybus.PassThrough:
		default:
			c.Unsorted += p.Split.Amount
		}
	}

	// The way the kind runs: income in is positive, an expense out is.
	run := func(k categorybus.Kind, a money.Amount) money.Amount {
		if k == categorybus.Expense {
			return -a
		}

		return a
	}

	for _, side := range []*Side{&c.Income, &c.Expenses} {
		side.Kind = categorybus.Income
		if side == &c.Expenses {
			side.Kind = categorybus.Expense
		}

		lined := map[types.ID]bool{}

		for _, l := range lines {
			if l.Kind != side.Kind {
				continue
			}

			if l.CategoryID.Zero() {
				side.Typed, side.HasTyped = l.Amount, true

				continue
			}

			lined[l.CategoryID] = true
			side.Lines += l.Amount

			cat := cats[l.CategoryID]
			side.Rows = append(side.Rows, Row{
				CategoryID: l.CategoryID, Name: cat.Name, Budget: l.Amount, Budgeted: true,
				Actual: run(side.Kind, actual[l.CategoryID]), Moved: cat.Kind != side.Kind,
			})
		}

		for id, a := range actual {
			if !lined[id] && cats[id].Kind == side.Kind && a != 0 {
				side.NotInBudget = append(side.NotInBudget, Row{CategoryID: id, Name: cats[id].Name, Actual: run(side.Kind, a)})
			}
		}

		byName := func(x, y Row) int {
			return cmp.Or(strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), strings.Compare(x.CategoryID.String(), y.CategoryID.String()))
		}

		slices.SortFunc(side.Rows, byName)
		slices.SortFunc(side.NotInBudget, byName)

		side.Total = Row{
			Budget: max(side.Typed, side.Lines), Budgeted: side.HasTyped || len(side.Rows) > 0,
			Actual: run(side.Kind, byKind[side.Kind]),
		}
	}
}

// choices is the categories a project's budget may have lines for, by ID:
// the income and expense categories of its organization's list, and of
// every category its parts are in -- a personal project's money, or an
// organization's project paid from somebody's personal card, is sorted
// with other lists.
func (b *Business) choices(ctx context.Context, book ledgerbus.Book) (map[types.ID]categorybus.Category, error) {
	out := map[types.ID]categorybus.Category{}

	if !book.Project.OrgID.Zero() {
		list, err := b.categories.Of(ctx, types.OrgScope(book.Project.OrgID))
		if err != nil {
			return nil, err
		}

		for _, c := range list {
			out[c.ID] = c
		}
	}

	for _, p := range book.Lines {
		if id := p.Split.CategoryID; !id.Zero() {
			if _, ok := out[id]; !ok {
				out[id] = categorybus.Category{ID: id, Name: p.CategoryName, Kind: p.CategoryKind}
			}
		}
	}

	return out, nil
}

// defaultCurrency is a new budget's currency: the one most of the project's
// money is in, or else the one most of the reader's accounts are in.
func (b *Business) defaultCurrency(ctx context.Context, actor types.ID, book ledgerbus.Book) (string, error) {
	count := map[string]int{}

	for _, p := range book.Lines {
		count[p.Currency]++
	}

	if len(count) == 0 {
		ov, err := b.access.Overview(ctx, actor)
		if err != nil {
			return "", err
		}

		accounts := ov.Accounts
		for _, o := range ov.Orgs {
			accounts = append(accounts, o.Accounts...)
		}

		for _, a := range accounts {
			count[a.Currency]++
		}
	}

	if len(count) == 0 {
		return "USD", nil
	}

	return slices.SortedFunc(maps.Keys(count), func(x, y string) int { return cmp.Or(count[y]-count[x], strings.Compare(x, y)) })[0], nil
}

// --- setting ----------------------------------------------------------------

// Entry is one amount on the budget form: a category's, or a kind's total
// when CategoryID is zero. Zero is no line.
type Entry struct {
	CategoryID types.ID
	Kind       categorybus.Kind
	Amount     money.Amount
}

// Invalid is a budget that will not do, which field, and for which category
// (zero for the form as a whole or a total).
type Invalid struct {
	Field      string // "currency", "amount", "category", "count"
	CategoryID types.ID
	Kind       categorybus.Kind
	Err        error
}

func (e Invalid) Error() string { return e.Field + ": " + e.Err.Error() }

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Form is what the budget form offers: the categories a line may be for, a
// kind at a time, by name, without the archived unless one has a line.
type Form struct {
	Comparison
	Income, Expenses []categorybus.Category
	Currencies       []string
}

// ProjectForm is the form for a project's budget, an owner's.
func (b *Business) ProjectForm(ctx context.Context, actor, projectID types.ID) (Form, error) {
	c, err := b.Project(ctx, actor, projectID)
	if err != nil {
		return Form{}, err
	}

	if !c.CanSet() {
		return Form{}, ErrForbidden
	}

	book, err := b.ledger.ProjectBook(ctx, actor, projectID)
	if err != nil {
		return Form{}, err
	}

	cats, err := b.choices(ctx, book)
	if err != nil {
		return Form{}, err
	}

	f := Form{Comparison: c}

	lined := map[types.ID]bool{}
	for _, side := range []Side{c.Income, c.Expenses} {
		for _, r := range side.Rows {
			lined[r.CategoryID] = true
		}
	}

	for _, cat := range cats {
		if cat.Archived() && !lined[cat.ID] {
			continue
		}

		switch cat.Kind {
		case categorybus.Income:
			f.Income = append(f.Income, cat)
		case categorybus.Expense:
			f.Expenses = append(f.Expenses, cat)
		}
	}

	byName := func(x, y categorybus.Category) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	}

	slices.SortFunc(f.Income, byName)
	slices.SortFunc(f.Expenses, byName)

	seen := map[string]bool{c.Currency: true}
	f.Currencies = []string{c.Currency}

	for _, p := range book.Lines {
		if !seen[p.Currency] {
			seen[p.Currency] = true
			f.Currencies = append(f.Currencies, p.Currency)
		}
	}

	return f, nil
}

// SetProject makes a project's budget these entries, in this currency: an
// owner's. Each change is a line in the project's history.
func (b *Business) SetProject(ctx context.Context, now time.Time, actor, projectID types.ID, currency string, entries []Entry) error {
	p, access, err := b.access.Project(ctx, actor, projectID)
	if err != nil {
		return err
	}

	if !access.Can(tenancybus.Manage) {
		return ErrForbidden
	}

	book, err := b.ledger.ProjectBook(ctx, actor, projectID)
	if err != nil {
		return err
	}

	cats, err := b.choices(ctx, book)
	if err != nil {
		return err
	}

	scope := p.Scope()

	before, err := b.store.Lines(ctx, scope, 0)
	if err != nil {
		return err
	}

	lines, err := linesOf(scope, 0, currency, entries, cats, before, actor, now)
	if err != nil {
		return err
	}

	return b.store.Replace(ctx, scope, 0, lines, events(now, actor, scope, before, lines, cats))
}

// linesOf checks the entries and makes them lines. A category must be one
// the budget may have and of the kind its entry says; a line already there
// for a category since archived may stay.
func linesOf(scope types.Scope, year int, currency string, entries []Entry, cats map[types.ID]categorybus.Category, before []Line, actor types.ID, now time.Time) ([]Line, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !currencyCode.MatchString(currency) {
		return nil, Invalid{Field: "currency", Err: errors.New("a currency is three letters, such as USD")}
	}

	had := map[types.ID]bool{}
	for _, l := range before {
		had[l.CategoryID] = true
	}

	var lines []Line

	seen := map[string]bool{}

	for _, e := range entries {
		key := e.CategoryID.String() + "/" + string(e.Kind)

		switch {
		case e.Kind != categorybus.Income && e.Kind != categorybus.Expense:
			return nil, Invalid{Field: "category", CategoryID: e.CategoryID, Kind: e.Kind, Err: errors.New("only income and expenses are budgeted")}
		case e.Amount < 0:
			return nil, Invalid{Field: "amount", CategoryID: e.CategoryID, Kind: e.Kind, Err: errors.New("an amount is what is expected, without a sign")}
		case seen[key]:
			return nil, Invalid{Field: "category", CategoryID: e.CategoryID, Kind: e.Kind, Err: errors.New("the same line twice")}
		case e.Amount == 0:
			continue
		}

		seen[key] = true

		if !e.CategoryID.Zero() {
			c, ok := cats[e.CategoryID]
			if !ok || c.Kind != e.Kind || (c.Archived() && !had[c.ID]) {
				return nil, Invalid{Field: "category", CategoryID: e.CategoryID, Kind: e.Kind, Err: fmt.Errorf("that category cannot have a %s line", e.Kind)}
			}
		}

		lines = append(lines, Line{
			ID: types.NewID(), Scope: scope, Year: year, CategoryID: e.CategoryID, Kind: e.Kind,
			Amount: e.Amount, Currency: currency, UpdatedBy: actor, UpdatedAt: now,
		})
	}

	if len(lines) > MaxLines {
		return nil, Invalid{Field: "count", Err: fmt.Errorf("a budget has at most %d lines", MaxLines)}
	}

	return lines, nil
}

// events is a line of history for each line that changed: set, changed,
// or taken out. A change of currency alone is said on every line, since
// every amount now means something else.
func events(now time.Time, actor types.ID, scope types.Scope, before, after []Line, cats map[types.ID]categorybus.Category) []eventbus.Event {
	key := func(l Line) string { return l.CategoryID.String() + "/" + string(l.Kind) }

	was := map[string]Line{}
	for _, l := range before {
		was[key(l)] = l
	}

	detail := func(l Line, amount money.Amount, prior Line, had bool) map[string]string {
		d := map[string]string{
			"category": cats[l.CategoryID].Name,
			"kind":     string(l.Kind),
			"amount":   amount.String(),
			"currency": l.Currency,
		}

		if had {
			d["before"], d["before_currency"] = prior.Amount.String(), prior.Currency
		}

		return d
	}

	var out []eventbus.Event

	for _, l := range after {
		prior, had := was[key(l)]
		delete(was, key(l))

		if had && prior.Amount == l.Amount && prior.Currency == l.Currency {
			continue
		}

		out = append(out, eventbus.New(now, actor, scope, Set, detail(l, l.Amount, prior, had)))
	}

	gone := slices.SortedFunc(maps.Values(was), func(x, y Line) int { return strings.Compare(key(x), key(y)) })

	for _, l := range gone {
		out = append(out, eventbus.New(now, actor, scope, Removed, detail(l, 0, l, true)))
	}

	return out
}
