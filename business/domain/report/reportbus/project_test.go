package reportbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// retreats is a house's "Spiritual Exercises": an account its retreats'
// money moves through, every part of it in the project. A June retreat
// left $40 in hand; October's took fees, paid the house and the cook, and
// lent a retreatant $40 for books, repaid thirty days after the cook was
// paid; February's paid the house more than it took, a check nobody has
// sorted, and money from the house's checking to make it up.
type retreats struct {
	house
	project types.ID
}

func newRetreats(w *world) retreats {
	w.t.Helper()

	ctx := w.t.Context()
	r := retreats{house: newHouse(w)}

	p, err := w.ten.CreateProject(ctx, now, r.owner, r.org, tenancybus.ProjectFields{Name: "Spiritual Exercises", BreakEven: true})
	if err != nil {
		w.t.Fatal(err)
	}

	r.project = p.ID

	acct, err := w.ten.CreateAccount(ctx, now, r.owner, r.org, tenancybus.AccountFields{Name: "Retreats checking", Kind: "checking"})
	if err != nil {
		w.t.Fatal(err)
	}

	w.importFile(r.owner, acct.ID, "retreats.csv")

	cat := func(name string, kind categorybus.Kind) types.ID {
		c, err := w.cats.Create(ctx, now, r.owner, types.OrgScope(r.org), name, kind)
		if err != nil {
			w.t.Fatal(err)
		}

		return c.ID
	}

	fees, costs := cat("Retreat fees", categorybus.Income), cat("Retreat costs", categorybus.Expense)
	lent, between := cat("Lent to retreatants", categorybus.PassThrough), cat("Between our accounts", categorybus.Transfer)

	into := map[string]types.ID{
		"RETREAT DEPOSIT": fees, "RETREAT FEES": fees, "RETREAT HOUSE": costs, "RETREAT COOK": costs,
		"BOOKSHOP": lent, "REPAID BY A RETREATANT": lent, "FROM HOUSE CHECKING": between, "CHECK 1043": {},
	}

	for _, month := range []string{"2025-06", "2025-10", "2025-11", "2026-02"} {
		txs, err := w.ledger.Transactions(ctx, r.owner, acct.ID, month)
		if err != nil {
			w.t.Fatal(err)
		}

		for _, tx := range txs {
			if _, err := w.ledger.SetSplits(ctx, now, r.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount, CategoryID: into[tx.Description], ProjectID: r.project}}); err != nil {
				w.t.Fatal(err)
			}
		}
	}

	return r
}

func TestAProjectsReport(t *testing.T) {
	w := newWorld(t)
	r := newRetreats(w)
	ctx := t.Context()
	scope := types.ProjectScope(r.project)
	m := money.MustParse

	rep, err := w.reports.ProjectReport(ctx, now, r.owner, scope, reportbus.Period{}, reportbus.ProjectChoices{})
	if err != nil {
		t.Fatal(err)
	}

	if !rep.Period.All || rep.Choices.Gap != reportbus.DefaultGap || rep.Name != "Spiritual Exercises" || len(rep.Sections) != 1 {
		t.Fatalf("the project's life: %+v %+v %d sections", rep.Period, rep.Choices, len(rep.Sections))
	}

	s := rep.Sections[0]

	type episode struct {
		from, to   string
		parts      int
		sum, after money.Amount
	}

	got := func(s reportbus.ProjectSection) []episode {
		var out []episode
		for _, e := range s.Episodes {
			out = append(out, episode{e.From.String(), e.To.String(), len(e.Parts), e.Sum(), e.Balance})
		}

		return out
	}

	// The book's repayment on the 28th of November is thirty days after
	// the cook, the last of October: the same run.
	want := []episode{
		{"2025-06-10", "2025-06-20", 2, m("40.00"), m("40.00")},
		{"2025-10-03", "2025-11-28", 5, m("50.00"), m("90.00")},
		{"2026-02-05", "2026-02-20", 4, m("0.00"), m("90.00")},
	}

	if g := got(s); len(g) != len(want) {
		t.Fatalf("episodes: %+v", g)
	} else {
		for i := range want {
			if g[i] != want[i] {
				t.Errorf("episode %d: got %+v, want %+v", i, g[i], want[i])
			}
		}
	}

	october := s.Episodes[1]
	if october.Income != m("1200.00") || october.Expenses != m("-1150.00") || october.PassThroughIn != m("40.00") || october.PassThroughOut != m("-40.00") || october.PassThrough() != 0 {
		t.Errorf("October's figures: %+v", october.Figures)
	}

	if b := october.Parts[2]; b.Description != "BOOKSHOP" || b.Kind != categorybus.PassThrough || b.Balance != m("200.00") {
		t.Errorf("the bookshop: %s %s %s", b.Description, b.Kind, b.Balance)
	}

	february := s.Episodes[2]
	if february.Net() != m("-50.00") || february.Transfers != m("75.00") || february.Unsorted != m("-25.00") || february.Parts[2].Kind != categorybus.Unsaid {
		t.Errorf("February's figures: %+v", february.Figures)
	}

	if s.BroughtForward != 0 || s.Before != 0 || s.Income != m("2200.00") || s.Expenses != m("-2160.00") || s.Sum() != m("90.00") || s.Balance != m("90.00") || s.Count != 11 {
		t.Errorf("the foot: %+v forward %s balance %s", s.Figures, s.BroughtForward, s.Balance)
	}

	// From October: June's $40 is brought forward, and the balances run on
	// from it.
	from := period(t, "2025-10", "2026-02")

	rep, err = w.reports.ProjectReport(ctx, now, r.owner, scope, from, reportbus.ProjectChoices{})
	if err != nil {
		t.Fatal(err)
	}

	if s := rep.Sections[0]; s.BroughtForward != m("40.00") || s.Before != 2 || len(s.Episodes) != 2 || s.Episodes[0].Parts[0].Balance != m("1240.00") || s.Sum() != m("50.00") || s.Balance != m("90.00") {
		t.Errorf("from October: forward %s (%d), %d episodes, balance %s", s.BroughtForward, s.Before, len(s.Episodes), s.Balance)
	}

	if rep.Before != period(t, "2025-05", "2025-09") {
		t.Errorf("the months before: %+v", rep.Before)
	}

	// Seventy days of quiet: October's last part and February's first
	// are sixty-nine apart, so the two are one run; June's is not.
	rep, err = w.reports.ProjectReport(ctx, now, r.owner, scope, reportbus.Period{}, reportbus.ProjectChoices{Gap: 70})
	if err != nil {
		t.Fatal(err)
	}

	if g := got(rep.Sections[0]); len(g) != 2 || g[1].parts != 9 || g[1].from != "2025-10-03" || g[1].to != "2026-02-20" {
		t.Errorf("with seventy days: %+v", g)
	}

	// An ordinary project -- a fund that spends over years -- is not read
	// in runs: its parts are one, whatever the gap, with the same balances.
	if _, err := w.ten.EditProject(ctx, now, r.owner, r.project, tenancybus.ProjectFields{Name: "Spiritual Exercises"}); err != nil {
		t.Fatal(err)
	}

	rep, err = w.reports.ProjectReport(ctx, now, r.owner, scope, reportbus.Period{}, reportbus.ProjectChoices{})
	if err != nil {
		t.Fatal(err)
	}

	if s := rep.Sections[0]; rep.BreakEven || len(s.Episodes) != 1 || len(s.Episodes[0].Parts) != 11 || s.Balance != m("90.00") || s.Episodes[0].Parts[6].Balance != m("90.00") {
		t.Errorf("an ordinary project: %+v", got(s))
	}

	// No budget, none beside the foot; with one, its whole life beside it
	// whatever the months, unless the report leaves it out.
	if rep.HasBudget {
		t.Error("a budget with no lines is beside the foot")
	}

	if err := w.budgets.SetProject(ctx, now, r.owner, r.project, "USD", []budgetbus.Entry{
		{Kind: categorybus.Income, Amount: m("2400.00")}, {Kind: categorybus.Expense, Amount: m("2400.00")},
	}); err != nil {
		t.Fatal(err)
	}

	rep, err = w.reports.ProjectReport(ctx, now, r.owner, scope, from, reportbus.ProjectChoices{})
	if err != nil {
		t.Fatal(err)
	}

	if b := rep.Budget; !rep.HasBudget || b.Income.Total.Budget != m("2400.00") || b.Income.Total.Actual != m("2200.00") || b.Expenses.Total.Actual != m("2160.00") || b.ExpectedNet() != 0 {
		t.Errorf("the budget beside the foot: %v %+v %+v", rep.HasBudget, b.Income.Total, b.Expenses.Total)
	}

	if rep, err = w.reports.ProjectReport(ctx, now, r.owner, scope, from, reportbus.ProjectChoices{HideBudget: true}); err != nil || rep.HasBudget {
		t.Errorf("the budget left out: %v %v", rep.HasBudget, err)
	}

	// The engine reads a project too, and its parts are the report's.
	if _, parts, err := w.reports.Parts(ctx, r.owner, reportbus.Question{Scope: scope, Period: from, By: []reportbus.Dimension{reportbus.ByKind}}, reportbus.Key{Kind: categorybus.PassThrough}); err != nil || len(parts) != 2 {
		t.Errorf("the pass-through from October: %d parts, %v", len(parts), err)
	}

	// The monthly style is not a project's, and a stranger finds neither.
	if _, err := w.reports.IncomeExpenses(ctx, now, r.owner, scope, reportbus.Period{}, reportbus.IncomeExpensesChoices{}); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("income and expenses of a project: %v", err)
	}

	if _, err := w.reports.ProjectReport(ctx, now, w.user("stranger@example.org"), scope, reportbus.Period{}, reportbus.ProjectChoices{}); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.reports.ProjectReport(ctx, now, r.owner, types.OrgScope(r.org), reportbus.Period{}, reportbus.ProjectChoices{}); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("an organization as a project: %v", err)
	}
}

// Saved with a longer gap, a project's report keeps it; a gap that is not
// one is refused; a note's figures include what was brought forward, so
// sorting a part before the period says the figures changed.
func TestASavedProjectReport(t *testing.T) {
	w := newWorld(t)
	r := newRetreats(w)
	ctx := t.Context()
	scope := types.ProjectScope(r.project)

	if _, err := w.reports.Save(ctx, now, r.owner, scope, reportbus.IncomeExpensesByMonth, reportbus.Fields{Title: "By month"}); problem(err) != "style" {
		t.Errorf("the monthly style on a project: %v", err)
	}

	if _, err := w.reports.Save(ctx, now, r.owner, scope, reportbus.ProjectStyle, reportbus.Fields{Title: "Retreats", Project: reportbus.ProjectChoices{Gap: 400}}); problem(err) != "gap" {
		t.Errorf("a gap of 400 days: %v", err)
	}

	s, err := w.reports.Save(ctx, now, r.owner, scope, reportbus.ProjectStyle, reportbus.Fields{Title: "Retreats", Project: reportbus.ProjectChoices{Gap: 70, HideAccounts: true}})
	if err != nil {
		t.Fatal(err)
	}

	from := period(t, "2025-10", "2026-02")

	run, err := w.reports.Run(ctx, now, r.owner, s.ID, reportbus.Period{})
	if err != nil || !run.Known || !run.Frame.Period.All || run.Project.Choices != (reportbus.ProjectChoices{Gap: 70, HideAccounts: true}) || len(run.Project.Sections[0].Episodes) != 2 {
		t.Fatalf("run: %+v %+v %v", run.Frame.Period, run.Project.Choices, err)
	}

	if err := w.reports.SetNote(ctx, now, r.owner, s.ID, from, "February is short until the house's transfer."); err != nil {
		t.Fatal(err)
	}

	if run, err := w.reports.Run(ctx, now, r.owner, s.ID, from); err != nil || !run.HasNote || run.Changed {
		t.Fatalf("with its note: %v %v %v", run.HasNote, run.Changed, err)
	}

	// June's cost was really the house's, and moves out of the project:
	// nothing in October to February moved, but what came into it did.
	txs, err := w.ledger.Transactions(ctx, r.owner, r.accountNamed(w, "Retreats checking"), "2025-06")
	if err != nil {
		t.Fatal(err)
	}

	for _, tx := range txs {
		if tx.Description == "RETREAT HOUSE" {
			if _, err := w.ledger.SetSplits(ctx, now, r.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if run, err := w.reports.Run(ctx, now, r.owner, s.ID, from); err != nil || !run.Changed || run.Project.Sections[0].BroughtForward != money.MustParse("100.00") {
		t.Errorf("after June changed: changed %v, %v", run.Changed, err)
	}

	if _, err := w.reports.Change(ctx, now, r.owner, s.ID, reportbus.Fields{Title: "Retreats", Project: reportbus.ProjectChoices{Gap: -1}}); problem(err) != "gap" {
		t.Errorf("changed to a gap of -1: %v", err)
	}
}

// problem is the field an error refuses, or "".
func problem(err error) string {
	if invalid, ok := errors.AsType[reportbus.Invalid](err); ok {
		return invalid.Field
	}

	return ""
}

// accountNamed is the house's account of a name.
func (r retreats) accountNamed(w *world, name string) types.ID {
	w.t.Helper()

	accounts, _, err := w.ten.Contents(w.t.Context(), r.owner, r.org)
	if err != nil {
		w.t.Fatal(err)
	}

	for _, a := range accounts {
		if a.Name == name {
			return a.ID
		}
	}

	w.t.Fatalf("no account %s", name)

	return types.ID{}
}
