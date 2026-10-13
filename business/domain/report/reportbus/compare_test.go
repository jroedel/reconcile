package reportbus_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// row is the row of a name among rows, or a zero one.
func row(rows []reportbus.Row, name string) reportbus.Row {
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}

	return reportbus.Row{}
}

// July against the July before: each row's total beside the year
// before's, a category with money then and none now a row of its own, and
// the totals; and saved, the choice is kept.
func TestComparedWithTheYearBefore(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()
	m := money.MustParse

	w.importFile(h.owner, h.checking, "checking-july-2025.csv")

	txs, err := w.ledger.Transactions(ctx, h.owner, h.checking, "2025-07")
	if err != nil {
		t.Fatal(err)
	}

	for _, tx := range txs {
		cat := map[string]types.ID{"MASS STIPENDS": h.stipends, "GROCERY MART": h.groceries, "FUEL STATION": h.fuel}[tx.Description]
		if _, err := w.ledger.SetSplits(ctx, now, h.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount, CategoryID: cat}}); err != nil {
			t.Fatal(err)
		}
	}

	choices := reportbus.IncomeExpensesChoices{Compare: reportbus.WithYearBefore}

	r, err := w.reports.IncomeExpenses(ctx, now, h.owner, types.OrgScope(h.org), period(t, "2026-07", "2026-07"), choices)
	if err != nil {
		t.Fatal(err)
	}

	if r.YearBefore != period(t, "2025-07", "2025-07") || len(r.Sections) != 1 {
		t.Fatalf("the year before: %+v, %d sections", r.YearBefore, len(r.Sections))
	}

	s := r.Sections[0]

	for name, want := range map[string][3]money.Amount{
		"Mass stipends": {m("1200.00"), m("1000.00"), m("200.00")},
		"Groceries":     {m("-150.00"), m("-100.00"), m("-50.00")},
		"Fuel":          {0, m("-40.00"), m("40.00")},
		"Travel":        {m("-600.00"), 0, m("-600.00")},
	} {
		rows := s.Expenses
		if name == "Mass stipends" {
			rows = s.Income
		}

		if got := row(rows, name); got.Total != want[0] || got.Before != want[1] || got.Change() != want[2] {
			t.Errorf("%s: %s, a year before %s, change %s", name, got.Total, got.Before, got.Change())
		}
	}

	if s.IncomeTotal.Before != m("1000.00") || s.ExpensesTotal.Before != m("-140.00") || s.Net.Before != m("860.00") || s.Unsorted.Before != 0 {
		t.Errorf("the totals a year before: %s %s %s %s", s.IncomeTotal.Before, s.ExpensesTotal.Before, s.Net.Before, s.Unsorted.Before)
	}

	if got := row(s.Expenses, "Fuel"); len(got.Months) != 1 || got.Months[0] != 0 {
		t.Errorf("fuel's months: %v", got.Months)
	}

	saved, err := w.reports.Save(ctx, now, h.owner, types.OrgScope(h.org), reportbus.IncomeExpensesByMonth, reportbus.Fields{Title: "P&L", Choices: choices})
	if err != nil {
		t.Fatal(err)
	}

	if run, err := w.reports.Run(ctx, now, h.owner, saved.ID, period(t, "2026-07", "2026-07")); err != nil || run.Report.Choices.Compare != reportbus.WithYearBefore || run.Report.Sections[0].Net.Before != m("860.00") {
		t.Errorf("saved: %+v %v", run.Report.Choices, err)
	}
}

// July and August against the 2026 budget: each category's line beside
// its row, a budgeted category with no money yet a row of its own, the
// kinds' totals as the budget page reckons them, and by group the group's
// lines together; the last twelve months straddle two years and are
// compared with neither.
func TestComparedWithTheBudget(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()
	m := money.MustParse
	scope := types.OrgScope(h.org)

	if err := w.budgets.SetOrgYear(ctx, now, h.owner, h.org, 2026, "USD", []budgetbus.Entry{
		{CategoryID: h.stipends, Kind: categorybus.Income, Amount: m("12000.00")},
		{CategoryID: h.groceries, Kind: categorybus.Expense, Amount: m("2000.00")},
		{CategoryID: h.fuel, Kind: categorybus.Expense, Amount: m("500.00")},
		{Kind: categorybus.Expense, Amount: m("10000.00")},
	}); err != nil {
		t.Fatal(err)
	}

	choices := reportbus.IncomeExpensesChoices{Compare: reportbus.WithBudget}

	r, err := w.reports.IncomeExpenses(ctx, now, h.owner, scope, period(t, "2026-07", "2026-08"), choices)
	if err != nil {
		t.Fatal(err)
	}

	if b := r.Budget; b.Year != 2026 || b.Label != "2026" || !b.Set || !b.Fits || b.Pace != 61 || b.Currency != "USD" {
		t.Fatalf("the budget: %+v", b)
	}

	s := r.Sections[0]

	if got := row(s.Income, "Mass stipends"); got.Budget != m("12000.00") || !got.Budgeted || got.AgainstBudget() != m("-10800.00") {
		t.Errorf("stipends: %s against %s", got.Budget, got.AgainstBudget())
	}

	if got := row(s.Expenses, "Groceries"); got.Budget != m("-2000.00") || got.AgainstBudget() != m("1670.00") {
		t.Errorf("groceries: %s against %s", got.Budget, got.AgainstBudget())
	}

	if got := row(s.Expenses, "Fuel"); got.Total != 0 || got.Budget != m("-500.00") {
		t.Errorf("fuel, budgeted and not spent: %+v", got)
	}

	if got := row(s.Expenses, "Travel"); got.Budgeted {
		t.Errorf("travel has no line and is budgeted: %+v", got)
	}

	if s.IncomeTotal.Budget != m("12000.00") || s.ExpensesTotal.Budget != m("-10000.00") || s.Net.Budget != m("2000.00") || !s.Net.Budgeted {
		t.Errorf("the totals' budget: %s %s %s", s.IncomeTotal.Budget, s.ExpensesTotal.Budget, s.Net.Budget)
	}

	// By group: groceries and fuel under "Car and table" share one line.
	group, err := w.cats.AddHeading(ctx, now, h.owner, scope, "Car and table", categorybus.Expense)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []types.ID{h.groceries, h.fuel} {
		if _, err := w.cats.SetHeading(ctx, now, h.owner, c, group.ID); err != nil {
			t.Fatal(err)
		}
	}

	choices.Detail = reportbus.EachGroupRow

	if r, err = w.reports.IncomeExpenses(ctx, now, h.owner, scope, period(t, "2026-07", "2026-08"), choices); err != nil {
		t.Fatal(err)
	}

	if got := row(r.Sections[0].Expenses, "Car and table"); got.Total != m("-330.00") || got.Budget != m("-2500.00") {
		t.Errorf("the group: %s, budget %s", got.Total, got.Budget)
	}

	r, err = w.reports.IncomeExpenses(ctx, now, h.owner, scope, period(t, "2025-08", "2026-07"), choices)
	if err != nil {
		t.Fatal(err)
	}

	if r.Budget.Fits || r.Sections[0].IncomeTotal.Budgeted {
		t.Errorf("twelve months across two years: %+v", r.Budget)
	}
}
