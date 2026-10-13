package reportbus

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// --- the comparisons (docs/reports.md, "The comparisons") ---------------------------

// Compared is what a report of income and expenses is compared with.
type Compared string

// The comparisons. Each is shown beside the period's total, never beside
// each month: a column for every month would double the table, and a
// house's month no longer fits on one sheet.
const (
	WithNothing Compared = ""

	// WithYearBefore is the same months a year before, row by row.
	WithYearBefore Compared = "year-before"

	// WithBudget is the organization's budget for the year the months are
	// in. A budget is for a whole year, so it is never spread over the
	// months, and a period across two budget years is compared with
	// neither; the page says how much of the year has gone by.
	WithBudget Compared = "budget"
)

// Budget is the budget year a report is compared with.
type Budget struct {
	Year     int
	Label    string
	Pace     int // how much of the year has gone by today, 0 to 100
	Currency string
	Set      bool

	// Fits reports whether every month of the report is in this budget
	// year: a report of the last twelve months straddles two, and is
	// compared with neither.
	Fits bool
}

// compare fills in the comparison the report's choices ask for. Budgets
// are an organization's, so an account on its own is compared with
// nothing when the budget is asked for.
func (b *Business) compare(ctx context.Context, now time.Time, actor types.ID, r *IncomeExpenses, by []Dimension) error {
	switch {
	case len(r.Months) == 0:
		return nil
	case r.Choices.Compare == WithYearBefore && r.Period.All:
		// Every month there is has no year before it: the months a year
		// earlier are mostly the same months.
		return nil
	case r.Choices.Compare == WithYearBefore:
		return b.yearBefore(ctx, actor, r, by)
	case r.Choices.Compare == WithBudget && r.Scope.Kind == types.ScopeOrg:
		return b.budget(ctx, now, actor, r)
	case r.Choices.Compare == WithBudget:
		r.Choices.Compare = WithNothing
	}

	return nil
}

// yearBefore is the same question for the same months a year earlier, laid
// out the same way, each row's total set beside the row it matches. A row
// with money then and none now is a row of its own, with nothing in the
// months: a category that has stopped is worth seeing.
func (b *Business) yearBefore(ctx context.Context, actor types.ID, r *IncomeExpenses, by []Dimension) error {
	r.YearBefore = Period{From: monthsBack(r.Months[0], 12), To: monthsBack(r.Months[len(r.Months)-1], 12)}

	a, err := b.Sum(ctx, actor, Question{Scope: r.Scope, Accounts: r.Choices.Accounts, Period: r.YearBefore, By: by})
	if err != nil {
		return err
	}

	for _, was := range sections(a.Cells, r.YearBefore.Months(), ledgerbus.Settling{}) {
		s := r.section(was.Currency)

		for _, pair := range []struct{ now, then *[]Row }{{&s.Income, &was.Income}, {&s.Expenses, &was.Expenses}} {
			for _, then := range *pair.then {
				row := matching(pair.now, then, r.Months)
				row.Before += then.Total
			}
		}

		s.IncomeTotal.Before, s.ExpensesTotal.Before = was.IncomeTotal.Total, was.ExpensesTotal.Total
		s.Net.Before, s.Unsorted.Before = was.Net.Total, was.Unsorted.Total
	}

	r.sortRows()

	return nil
}

// budget sets the budget for the year the report's months are in beside
// each row: a category's line on its row, or on its group's when the
// report is by group, and each kind's total on the kind's.
func (b *Business) budget(ctx context.Context, now time.Time, actor types.ID, r *IncomeExpenses) error {
	first, _ := monthStart(r.Months[0])
	last, _ := monthStart(r.Months[len(r.Months)-1])
	year := budgetbus.YearOf(first, r.FiscalStart)

	e, err := b.budgets.OrgExpected(ctx, now, actor, r.Scope.ID, year)
	if err != nil {
		return err
	}

	r.Budget = Budget{
		Year: e.Year, Label: e.Label, Pace: e.Pace, Currency: e.Currency, Set: e.Set,
		Fits: budgetbus.YearOf(last, r.FiscalStart) == year,
	}

	if !r.Budget.Set || !r.Budget.Fits {
		return nil
	}

	s := r.section(e.Currency)

	for _, side := range []struct {
		rows  *[]Row
		total *Row
		kind  categorybus.Kind
		lines budgetbus.Side
		sign  money.Amount
	}{
		{&s.Income, &s.IncomeTotal, categorybus.Income, e.Income, 1},
		{&s.Expenses, &s.ExpensesTotal, categorybus.Expense, e.Expenses, -1},
	} {
		for _, line := range side.lines.Rows {
			like := Row{Kind: side.kind, CategoryID: line.CategoryID, Name: line.Name}

			if g := r.groupOf[line.CategoryID]; !g.Zero() && r.Choices.Detail == EachGroupRow {
				like = Row{Kind: side.kind, GroupID: g, Name: r.Headings[g].Name}
			}

			row := matching(side.rows, like, r.Months)
			row.Budget += side.sign * line.Budget
			row.Budgeted = true
		}

		side.total.Budget, side.total.Budgeted = side.sign*side.lines.Total.Budget, side.lines.Total.Budgeted
	}

	s.Net.Budget = s.IncomeTotal.Budget + s.ExpensesTotal.Budget
	s.Net.Budgeted = s.IncomeTotal.Budgeted || s.ExpensesTotal.Budgeted

	r.sortRows()

	return nil
}

// section is the report's section in a currency, made empty when the
// report has no money in it: a budget in dollars beside a period with
// none still says what was expected.
func (r *IncomeExpenses) section(currency string) *Section {
	for i := range r.Sections {
		if r.Sections[i].Currency == currency {
			return &r.Sections[i]
		}
	}

	n := len(r.Months)
	s := Section{
		Currency:      currency,
		IncomeTotal:   newRow(categorybus.Income, "", r.Months),
		ExpensesTotal: newRow(categorybus.Expense, "", r.Months),
		Net:           newRow("", "", r.Months),
		Unsorted:      newRow(categorybus.Unsaid, "", r.Months),
		Sorted:        slices.Repeat([]int{-1}, n),
		SortedTotal:   -1,
	}

	r.Sections = append(r.Sections, s)
	slices.SortFunc(r.Sections, func(a, b Section) int { return strings.Compare(a.Currency, b.Currency) })

	return r.section(currency)
}

// sortRows puts each section's rows back in the order of their names,
// after a comparison has added some.
func (r *IncomeExpenses) sortRows() {
	for i := range r.Sections {
		for _, rows := range [][]Row{r.Sections[i].Income, r.Sections[i].Expenses} {
			slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		}
	}
}

// matching is the row among rows that is like's -- its group's, or its
// category's -- made with nothing in its months when there is none.
func matching(rows *[]Row, like Row, months []string) *Row {
	for i, row := range *rows {
		if (!like.GroupID.Zero() && row.GroupID == like.GroupID) || (like.GroupID.Zero() && row.GroupID.Zero() && row.CategoryID == like.CategoryID) {
			return &(*rows)[i]
		}
	}

	row := newRow(like.Kind, like.Name, months)
	row.CategoryID, row.GroupID = like.CategoryID, like.GroupID
	*rows = append(*rows, row)

	return &(*rows)[len(*rows)-1]
}

// monthsBack is a month n months earlier.
func monthsBack(month string, n int) string {
	m, err := monthStart(month)
	if err != nil {
		return month
	}

	return m.AddDate(0, -n, 0).Format("2006-01")
}
