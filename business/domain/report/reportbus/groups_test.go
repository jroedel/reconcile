package reportbus_test

import (
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A report by group shows a group's categories as one row and a category
// under none as its own; by category it is as it was. The group's figure
// is made of its categories' parts, a saved report keeps the choice, and
// a group archived since still names last year's rows.
func TestGroupsInTheReport(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()
	books := types.OrgScope(h.org)

	daily, err := w.cats.AddHeading(ctx, now, h.owner, books, "Daily life", categorybus.Expense)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []types.ID{h.groceries, h.household} {
		if _, err := w.cats.SetHeading(ctx, now, h.owner, c, daily.ID); err != nil {
			t.Fatal(err)
		}
	}

	expenses := func(c reportbus.IncomeExpensesChoices) map[string][]money.Amount {
		t.Helper()

		r, err := w.reports.IncomeExpenses(ctx, now, h.owner, books, period(t, "2026-07", "2026-08"), c)
		if err != nil {
			t.Fatal(err)
		}

		out := map[string][]money.Amount{}
		for _, row := range r.Sections[0].Expenses {
			out[row.Name] = row.Months
		}

		if !slices.Equal(r.Sections[0].ExpensesTotal.Months, amounts("-825.00", "-180.00")) {
			t.Errorf("the expenses' total: %v", r.Sections[0].ExpensesTotal.Months)
		}

		return out
	}

	byGroup := expenses(reportbus.IncomeExpensesChoices{Detail: reportbus.EachGroupRow})
	if len(byGroup) != 2 || !slices.Equal(byGroup["Daily life"], amounts("-225.00", "-180.00")) || !slices.Equal(byGroup["Travel"], amounts("-600.00", "0")) {
		t.Errorf("by group: %v", byGroup)
	}

	if byCategory := expenses(reportbus.IncomeExpensesChoices{}); len(byCategory) != 3 || byCategory["Daily life"] != nil {
		t.Errorf("by category: %v", byCategory)
	}

	q := reportbus.Question{Scope: books, Period: period(t, "2026-07", "2026-07"), By: []reportbus.Dimension{reportbus.ByKind, reportbus.ByGroup, reportbus.ByMonth}}

	for name, c := range map[string]struct {
		group types.ID
		want  string
	}{
		"the group":          {daily.ID, "-225.00"},
		"what is under none": {types.ID{}, "-600.00"},
	} {
		_, lines, err := w.reports.Parts(ctx, h.owner, q, reportbus.Key{Kind: categorybus.Expense, GroupID: c.group, Month: "2026-07"})
		if err != nil {
			t.Fatal(err)
		}

		var sum money.Amount
		for _, l := range lines {
			sum += l.Split.Amount
		}

		if sum != money.MustParse(c.want) {
			t.Errorf("%s's parts: %s, want %s", name, sum, c.want)
		}
	}

	s, err := w.reports.Save(ctx, now, h.owner, books, reportbus.IncomeExpensesByMonth, reportbus.Fields{Title: "P&L", Choices: reportbus.IncomeExpensesChoices{Detail: reportbus.EachGroupRow}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.cats.SetHeadingArchived(ctx, now, h.owner, daily.ID, true); err != nil {
		t.Fatal(err)
	}

	run, err := w.reports.Run(ctx, now, h.owner, s.ID, period(t, "2026-07", "2026-08"))
	if err != nil {
		t.Fatal(err)
	}

	if run.Report.Choices.Detail != reportbus.EachGroupRow || !slices.ContainsFunc(run.Report.Sections[0].Expenses, func(r reportbus.Row) bool {
		return r.GroupID == daily.ID && r.Name == "Daily life"
	}) {
		t.Errorf("the saved report, with its group archived: %+v", run.Report.Sections[0].Expenses)
	}
}
