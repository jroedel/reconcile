package reportbus

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// --- styles ----------------------------------------------------------------------

// Style is one layout of a report, written here and reviewed by pull
// request (docs/reports.md, "Styles"). Key is how a saved report and an
// address name it, and never changes; the name a person reads is the
// template's, translated.
type Style struct {
	Key    string
	Scopes []types.ScopeKind
}

// IncomeExpensesByMonth is the books' income and expenses, a month to a
// column.
const IncomeExpensesByMonth = "income-expenses-by-month"

// Styles is every style, in the order a page lists them.
var Styles = []Style{
	{Key: IncomeExpensesByMonth, Scopes: []types.ScopeKind{types.ScopeOrg, types.ScopeAccount}},
}

// StyleOf is the style with a key.
func StyleOf(key string) (Style, bool) {
	i := slices.IndexFunc(Styles, func(s Style) bool { return s.Key == key })
	if i < 0 {
		return Style{}, false
	}

	return Styles[i], true
}

// StylesFor is the styles a kind of scope can be reported in.
func StylesFor(kind types.ScopeKind) []Style {
	var out []Style

	for _, s := range Styles {
		if slices.Contains(s.Scopes, kind) {
			out = append(out, s)
		}
	}

	return out
}

// --- income and expenses by month -------------------------------------------------

// PassThroughShown is how a report shows pass-through below the line.
type PassThroughShown string

// The ways. One total keeps a house meeting's sheet about the house's money
// rather than one person's repaid lunch; it is a way of showing, not a
// secret, since whoever reads the report may read the transactions too.
const (
	OneTotal     PassThroughShown = "total"
	EachCategory PassThroughShown = "each"
)

// Detail is how finely a report's income and expenses are shown.
type Detail string

// The details. A category under no group is its own row either way, so a
// list with no groups reads the same in both.
const (
	EachCategoryRow Detail = "category"
	EachGroupRow    Detail = "group"
)

// IncomeExpensesChoices is what a person chooses of this style. A field
// added later gets a default here, so that every address and saved report
// from before it reads as that default.
type IncomeExpensesChoices struct {
	Accounts    []types.ID // none: every account of the books
	PassThrough PassThroughShown
	Detail      Detail
}

func (c IncomeExpensesChoices) normal() IncomeExpensesChoices {
	if c.PassThrough != EachCategory {
		c.PassThrough = OneTotal
	}

	if c.Detail != EachGroupRow {
		c.Detail = EachCategoryRow
	}

	return c
}

// Row is a line of the table: a category, a group of them, a kind's total,
// the net, or what is not sorted yet. Months is a figure per month of the
// report, in its order; Total is theirs together.
type Row struct {
	Kind       categorybus.Kind
	CategoryID types.ID // zero for a group or a total
	GroupID    types.ID // a group's row
	Name       string
	Months     []money.Amount
	Total      money.Amount
}

// Section is the report in one currency. Nothing is added across
// currencies, so a report with two has two sections.
type Section struct {
	Currency string

	Income, Expenses           []Row
	IncomeTotal, ExpensesTotal Row
	Net                        Row

	// Unsorted is money not sorted yet, and Sorted how much of each month's
	// money is sorted, by amount, 0 to 100, or -1 for a month with none. A
	// month at 99.9% says 99: it is not done until it is.
	Unsorted    Row
	Sorted      []int
	SortedTotal int

	// What should come back to zero, as the categories page reckons it
	// (ledgerbus.Settling): each pass-through category's balance at the
	// end of the period -- what is still owed, or not yet forwarded, then --
	// and the transfers', with each month of the period whose transfers do
	// not net to zero.
	PassThrough      []Owed
	PassThroughTotal money.Amount
	Transfers        money.Amount
	TransferMonths   []ledgerbus.MonthBalance
}

// Owed is a pass-through category's balance.
type Owed struct {
	CategoryID types.ID
	Name       string
	Balance    money.Amount
}

// Mark is how a month of the report stands against the bank's paper.
type Mark string

// The marks.
const (
	// Reconciled: every account with money or a statement in the month is
	// reconciled for every day of it. Its figures are as checked.
	Reconciled Mark = "reconciled"

	// NotYet: some account's month is not, so the figures are provisional.
	NotYet Mark = "not-yet"

	// Nothing: no account of the report has money or a statement in it.
	Nothing Mark = "nothing"
)

// IncomeExpenses is the style's report.
type IncomeExpenses struct {
	Answer
	Choices IncomeExpensesChoices

	// Months is the report's columns, oldest first.
	Months []string
	Marks  []Mark

	Sections []Section

	// Presets is the periods the page offers; Before and After the same
	// length either side, zero for every month.
	Presets       []Preset
	Before, After Period

	// Partial reports that only some of the books' accounts were chosen,
	// while what should come back to zero is reckoned across all of them:
	// a transfer to an account left out is still a transfer.
	Partial bool
}

// IncomeExpenses is the books' income and expenses by month, for anyone who
// may read the scope. A zero period is the year to date.
func (b *Business) IncomeExpenses(ctx context.Context, now time.Time, actor types.ID, scope types.Scope, p Period, c IncomeExpensesChoices) (IncomeExpenses, error) {
	books, err := b.Books(ctx, actor, scope)
	if err != nil {
		return IncomeExpenses{}, err
	}

	today := types.DateOf(now)
	c = c.normal()
	p = defaulted(p, today, books.FiscalStart)

	by := []Dimension{ByKind, ByCategory, ByMonth}
	if c.Detail == EachGroupRow {
		by = []Dimension{ByKind, ByGroup, ByCategory, ByMonth}
	}

	a, err := b.Sum(ctx, actor, Question{Scope: scope, Accounts: c.Accounts, Period: p, By: by})
	if err != nil {
		return IncomeExpenses{}, err
	}

	r := IncomeExpenses{
		Answer: a, Choices: c, Months: p.Months(),
		Presets: Presets(today, books.FiscalStart), Partial: len(a.Chosen) < len(a.Accounts),
	}

	if !p.All {
		r.Before, r.After = p.Shifted(-1), p.Shifted(1)
	} else if a.First != "" {
		r.Months = Period{From: a.First, To: a.Last}.Months()
	}

	settling, err := b.ledger.Settling(ctx, actor, scope)
	if err != nil {
		return IncomeExpenses{}, err
	}

	r.Sections = sections(a.Cells, r.Months, settling)

	if r.Marks, err = b.marks(ctx, actor, today, a, r.Months); err != nil {
		return IncomeExpenses{}, err
	}

	return r, nil
}

// sections lays the cells out a currency at a time.
func sections(cells []Cell, months []string, settling ledgerbus.Settling) []Section {
	column := make(map[string]int, len(months))
	for i, m := range months {
		column[m] = i
	}

	end := ""
	if len(months) > 0 {
		end = months[len(months)-1]
	}

	byCurrency := map[string]*Section{}

	var order []string

	section := func(cur string) *Section {
		s, ok := byCurrency[cur]
		if !ok {
			s = &Section{Currency: cur}
			s.IncomeTotal, s.ExpensesTotal = newRow(categorybus.Income, "", months), newRow(categorybus.Expense, "", months)
			s.Net, s.Unsorted = newRow("", "", months), newRow(categorybus.Unsaid, "", months)
			byCurrency[cur] = s
			order = append(order, cur)
		}

		return s
	}

	moved := map[string][]money.Amount{} // how much moved each month, by currency
	unsortedMoved := map[string][]money.Amount{}

	for _, c := range cells {
		i, ok := column[c.Month]
		if !ok {
			continue
		}

		s := section(c.Currency)

		if moved[c.Currency] == nil {
			moved[c.Currency], unsortedMoved[c.Currency] = make([]money.Amount, len(months)), make([]money.Amount, len(months))
		}

		moved[c.Currency][i] += c.In - c.Out

		switch c.Kind {
		case categorybus.Income:
			add(rowFor(&s.Income, c, months), i, c.Sum)
			add(&s.IncomeTotal, i, c.Sum)
			add(&s.Net, i, c.Sum)
		case categorybus.Expense:
			add(rowFor(&s.Expenses, c, months), i, c.Sum)
			add(&s.ExpensesTotal, i, c.Sum)
			add(&s.Net, i, c.Sum)
		case categorybus.Unsaid:
			add(&s.Unsorted, i, c.Sum)
			unsortedMoved[c.Currency][i] += c.In - c.Out
		}
	}

	for _, pt := range settling.PassThrough {
		balance := balanceAt(pt, end)
		if balance == 0 && byCurrency[pt.Currency] == nil {
			continue
		}

		s := section(pt.Currency)
		s.PassThrough = append(s.PassThrough, Owed{CategoryID: pt.Category.ID, Name: pt.Category.Name, Balance: balance})
		s.PassThroughTotal += balance
	}

	for _, tr := range settling.Transfers {
		balance := balanceAt(tr, end)

		var off []ledgerbus.MonthBalance

		for _, m := range tr.Months {
			if _, in := column[m.Month]; in {
				off = append(off, m)
			}
		}

		if balance == 0 && len(off) == 0 && byCurrency[tr.Currency] == nil {
			continue
		}

		s := section(tr.Currency)
		s.Transfers, s.TransferMonths = balance, off
	}

	out := make([]Section, 0, len(order))

	slices.Sort(order)

	for _, cur := range order {
		s := byCurrency[cur]

		for _, rows := range [][]Row{s.Income, s.Expenses} {
			slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		}

		s.Sorted, s.SortedTotal = sortedShare(moved[cur], unsortedMoved[cur], len(months))
		out = append(out, *s)
	}

	return out
}

func newRow(kind categorybus.Kind, name string, months []string) Row {
	return Row{Kind: kind, Name: name, Months: make([]money.Amount, len(months))}
}

// rowFor is the cell's row among rows, made if it is not there yet: its
// group's, when the cells were read by group and it is under one, and
// otherwise its category's.
func rowFor(rows *[]Row, c Cell, months []string) *Row {
	for i := range *rows {
		if r := (*rows)[i]; (!c.GroupID.Zero() && r.GroupID == c.GroupID) || (c.GroupID.Zero() && r.GroupID.Zero() && r.CategoryID == c.CategoryID) {
			return &(*rows)[i]
		}
	}

	r := newRow(c.Kind, c.Category, months)
	r.CategoryID = c.CategoryID

	if !c.GroupID.Zero() {
		r.Name, r.CategoryID, r.GroupID = c.Group, types.ID{}, c.GroupID
	}

	*rows = append(*rows, r)

	return &(*rows)[len(*rows)-1]
}

func add(r *Row, month int, a money.Amount) {
	r.Months[month] += a
	r.Total += a
}

// balanceAt is a balance as it stood at the end of a month: its balance
// now, less what moved after. "" is now.
func balanceAt(s ledgerbus.Settled, month string) money.Amount {
	balance := s.Balance
	if month == "" {
		return balance
	}

	for _, m := range s.Months {
		if m.Month > month {
			balance -= m.Balance
		}
	}

	return balance
}

// sortedShare is how much of each month's money is sorted, by amount, and
// of all of them together.
func sortedShare(moved, unsorted []money.Amount, n int) ([]int, int) {
	share := func(all, not money.Amount) int {
		if all == 0 {
			return -1
		}

		return int((all - not) * 100 / all)
	}

	out := make([]int, n)

	var all, not money.Amount

	for i := range n {
		if moved == nil {
			out[i] = -1

			continue
		}

		out[i] = share(moved[i], unsorted[i])
		all += moved[i]
		not += unsorted[i]
	}

	return out, share(all, not)
}

// marks is each month's mark (Mark), from each account's months as the
// month-by-month page reckons them (ledgerbus.Coverage). An account counts
// in a month when it has money in it or a statement reaching it, so that a
// card closed two years ago neither holds this month back nor goes missing
// from the months it was used.
func (b *Business) marks(ctx context.Context, actor types.ID, today types.Date, a Answer, months []string) ([]Mark, error) {
	type am struct {
		account types.ID
		month   string
	}

	has := map[am]bool{}
	for _, c := range a.lines {
		has[am{c.AccountID, c.PostedOn.String()[:7]}] = true
	}

	counted := make([]int, len(months))
	reconciled := make([]int, len(months))

	for _, acct := range a.Chosen {
		cover, err := b.ledger.Coverage(ctx, actor, acct.ID, today)
		if err != nil {
			return nil, err
		}

		byMonth := make(map[string]ledgerbus.MonthCover, len(cover))
		for _, mc := range cover {
			byMonth[mc.Month] = mc
		}

		for i, m := range months {
			mc, known := byMonth[m]
			if !has[am{acct.ID, m}] && (!known || len(mc.Statements) == 0) {
				continue
			}

			counted[i]++

			if known && mc.State == ledgerbus.Reconciled {
				reconciled[i]++
			}
		}
	}

	out := make([]Mark, len(months))

	for i := range months {
		switch {
		case counted[i] == 0:
			out[i] = Nothing
		case reconciled[i] == counted[i]:
			out[i] = Reconciled
		default:
			out[i] = NotYet
		}
	}

	return out, nil
}
