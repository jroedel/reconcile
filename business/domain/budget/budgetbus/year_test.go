package budgetbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

func TestBudgetYears(t *testing.T) {
	day := func(s string) time.Time {
		d, _ := time.Parse(time.DateOnly, s)

		return d
	}

	for _, c := range []struct {
		day         string
		month, year int
		label       string
		start, end  string
	}{
		{"2026-08-15", 1, 2026, "2026", "2026-01-01", "2027-01-01"},
		{"2026-08-15", 7, 2026, "2026–27", "2026-07-01", "2027-07-01"},
		{"2026-06-30", 7, 2025, "2025–26", "2025-07-01", "2026-07-01"},
		{"2026-01-01", 9, 2025, "2025–26", "2025-09-01", "2026-09-01"},
		{"2099-12-31", 12, 2099, "2099–00", "2099-12-01", "2100-12-01"},
	} {
		year := budgetbus.YearOf(day(c.day), c.month)
		start, end := budgetbus.Span(year, c.month)

		if year != c.year || budgetbus.Label(year, c.month) != c.label || start.String() != c.start || end.String() != c.end {
			t.Errorf("%s from month %d: %d %q %s %s", c.day, c.month, year, budgetbus.Label(year, c.month), start, end)
		}
	}
}

// A parish whose year runs July to June: the pilgrimage's July is in
// 2026–27, set against the year's budget with the pace beside it; next
// year starts as a copy.
func TestAnOrganizationsYear(t *testing.T) {
	w := newWorld(t)
	p := newPilgrimage(w)
	ctx := t.Context()
	m := money.MustParse

	if _, err := w.ten.SetFiscalStart(ctx, now, p.owner, p.org, 7); err != nil {
		t.Fatal(err)
	}

	if err := w.budgets.SetOrgYear(ctx, now, p.owner, p.org, 0, "USD", []budgetbus.Entry{
		{CategoryID: p.fees, Kind: categorybus.Income, Amount: m("2000.00")},
		{CategoryID: p.travel, Kind: categorybus.Expense, Amount: m("1000.00")},
	}); err != nil {
		t.Fatal(err)
	}

	f, err := w.budgets.OrgYear(ctx, now, p.owner, p.org, 0)
	if err != nil {
		t.Fatal(err)
	}

	c := f.Comparison

	// 15 August is 45 days into a year of 365.
	if c.Year != 2026 || c.Label != "2026–27" || c.Start.String() != "2026-07-01" || c.Pace != 12 || !c.Set || c.CanCopy {
		t.Errorf("the year: %d %q %s, pace %d", c.Year, c.Label, c.Start, c.Pace)
	}

	if c.Income.Total.Actual != m("900.00") || c.Expenses.Rows[0].Actual != m("350.00") || len(c.Expenses.NotInBudget) != 1 || c.Unsorted != m("-20.00") {
		t.Errorf("the comparison: %+v", c)
	}

	if len(f.IncomeCategories) != 1 || len(f.ExpenseCategories) != 3 {
		t.Errorf("the form's categories: %d income, %d expense", len(f.IncomeCategories), len(f.ExpenseCategories))
	}

	events, _ := w.history.Recent(ctx, types.OrgScope(p.org), 1)
	if events[0].Action != budgetbus.Set || events[0].Detail["year"] != "2026–27" {
		t.Errorf("history: %+v", events[0])
	}

	// The year before had none of July; the year after starts empty, and
	// as a copy -- once.
	if f, _ := w.budgets.OrgYear(ctx, now, p.owner, p.org, 2025); f.Income.Total.Actual != 0 || f.CanCopy {
		t.Errorf("2025–26: %+v", f.Comparison)
	}

	next, _ := w.budgets.OrgYear(ctx, now, p.owner, p.org, 2027)
	if next.Set || !next.CanCopy || next.Pace != 0 {
		t.Errorf("2027–28 before copying: %+v", next.Comparison)
	}

	if n, err := w.budgets.CopyYear(ctx, now, p.owner, p.org, 2027); err != nil || n != 2 {
		t.Fatalf("copied %d: %v", n, err)
	}

	if next, _ := w.budgets.OrgYear(ctx, now, p.owner, p.org, 2027); !next.Set || next.Expenses.Total.Budget != m("1000.00") {
		t.Errorf("2027–28 after copying: %+v", next.Comparison)
	}

	if _, err := w.budgets.CopyYear(ctx, now, p.owner, p.org, 2027); !errors.Is(err, budgetbus.ErrNothingToCopy) {
		t.Errorf("copying twice: %v", err)
	}

	events, _ = w.history.Recent(ctx, types.OrgScope(p.org), 1)
	if events[0].Action != budgetbus.Copied || events[0].Detail["from"] != "2026–27" || events[0].Detail["year"] != "2027–28" {
		t.Errorf("history: %+v", events[0])
	}

	// A year out of range is nowhere.
	if _, err := w.budgets.OrgYear(ctx, now, p.owner, p.org, 1066); !errors.Is(err, budgetbus.ErrNotFound) {
		t.Errorf("1066: %v", err)
	}

	// A bookkeeper of the organization reads it and cannot set it; a
	// stranger finds nothing.
	clerk := w.user("clerk@example.org")
	w.grant(p.owner, types.OrgScope(p.org), "clerk@example.org", tenancybus.Bookkeeper)
	stranger := w.user("stranger@example.org")

	if f, err := w.budgets.OrgYear(ctx, now, clerk, p.org, 0); err != nil || f.CanSet() || f.Income.Total.Actual != m("900.00") {
		t.Errorf("the clerk reading: %v", err)
	}

	for who, want := range map[types.ID]error{clerk: budgetbus.ErrForbidden, stranger: budgetbus.ErrNotFound} {
		if err := w.budgets.SetOrgYear(ctx, now, who, p.org, 0, "USD", nil); !errors.Is(err, want) {
			t.Errorf("SetOrgYear: %v, want %v", err, want)
		}

		if _, err := w.budgets.CopyYear(ctx, now, who, p.org, 2028); !errors.Is(err, want) {
			t.Errorf("CopyYear: %v, want %v", err, want)
		}
	}

	if _, err := w.budgets.OrgYear(ctx, now, stranger, p.org, 0); !errors.Is(err, budgetbus.ErrNotFound) {
		t.Errorf("a stranger reading: %v", err)
	}

	if _, err := w.ten.SetFiscalStart(ctx, now, clerk, p.org, 1); !errors.Is(err, tenancybus.ErrForbidden) {
		t.Errorf("the clerk moving the year: %v", err)
	}
}
