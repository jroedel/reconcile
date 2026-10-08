package ledgerbus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

func date(t *testing.T, s string) types.Date {
	t.Helper()

	d, err := types.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}

	return d
}

func (w *world) statements(actor, account types.ID) []ledgerbus.Statement {
	w.t.Helper()

	sts, err := w.ledger.Statements(w.t.Context(), actor, account)
	if err != nil {
		w.t.Fatal(err)
	}

	return sts
}

// The treasurer reconciles July against the paper, which runs to the 31st
// though the export stopped on the 28th; July then holds still until
// somebody reopens it and says why.
func TestReconcilingLocksThePeriod(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()
	july := w.statements(e.owner, e.account)[0]

	rv, err := w.ledger.Review(ctx, e.owner, july.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The file balanced row by row, so it states its ends.
	if st := rv.Statement; !st.HasOpening || st.Opening != 0 || !st.HasClosing || st.Closing != 108901 {
		t.Errorf("the balances read from the rows: %v %v, %v %v", st.HasOpening, st.Opening, st.HasClosing, st.Closing)
	}

	if len(rv.Transactions) != 6 || len(rv.Unsorted) != 6 || rv.In != 125000 || rv.Out != -16099 {
		t.Errorf("review: %d transactions, %d unsorted, in %v, out %v", len(rv.Transactions), len(rv.Unsorted), rv.In, rv.Out)
	}

	st, err := w.ledger.Reconcile(ctx, now, e.owner, july.ID, date(t, "2026-07-01"), date(t, "2026-07-31"), "Agrees with the paper.")
	if err != nil {
		t.Fatal(err)
	}

	if s, end := st.Period(); !st.Reconciled() || s.String() != "2026-07-01" || end.String() != "2026-07-31" {
		t.Fatalf("reconciled %v for %s to %s", st.Reconciled(), s, end)
	}

	if _, err := w.ledger.Reconcile(ctx, now, e.owner, july.ID, types.Date{}, types.Date{}, ""); !errors.Is(err, ledgerbus.ErrReconciled) {
		t.Errorf("reconciling twice: %v", err)
	}

	// Its parts hold still, and the page knows why.
	ed, err := w.ledger.Transaction(ctx, e.owner, e.grocery.ID)
	if err != nil {
		t.Fatal(err)
	}

	if ed.CanSort() || ed.Lock.StatementID != july.ID {
		t.Errorf("the editor of a locked transaction: can sort %v, lock %+v", ed.CanSort(), ed.Lock)
	}

	sort := []ledgerbus.Part{{Amount: e.grocery.Amount, CategoryID: e.groceries}}
	if _, err := w.ledger.SetSplits(ctx, now, e.owner, e.grocery.ID, sort); !errors.Is(err, ledgerbus.ErrLocked) {
		t.Errorf("sorting a locked transaction: %v", err)
	}

	// A file that would add a day the paper did not have is refused, and
	// the preview says so before anyone presses import.
	d, file := w.prepare(e.owner, e.account, "checking-july-late.csv")
	if d.Statement.Locked != 1 || d.Ready() {
		t.Errorf("the preview of a late July row: locked %d, ready %v", d.Statement.Locked, d.Ready())
	}

	if _, err := w.ledger.Import(ctx, now, e.owner, e.account, file, ledgerbus.Options{Mapping: d.Mapping}); !errors.Is(err, ledgerbus.ErrLocked) {
		t.Errorf("importing into July: %v", err)
	}

	// August is not July: the overlapping export brings its two new rows.
	aug := w.imports(e.owner, e.account, "checking-july-august.csv")
	if aug.Added != 2 {
		t.Errorf("August added %d", aug.Added)
	}

	if _, err := w.ledger.RemoveStatement(ctx, now, e.owner, july.ID); !errors.Is(err, ledgerbus.ErrLocked) {
		t.Errorf("removing the reconciled statement: %v", err)
	}

	if _, err := w.ledger.RemoveStatement(ctx, now, e.owner, aug.ID); err != nil {
		t.Errorf("removing August, which brought nothing into July: %v", err)
	}

	// Reopening needs a reason, and the history keeps it.
	if _, err := w.ledger.Reopen(ctx, now, e.owner, july.ID, "  "); !errors.Is(err, ledgerbus.ErrReason) {
		t.Errorf("reopening without a reason: %v", err)
	}

	if _, err := w.ledger.Reopen(ctx, now, e.owner, july.ID, "The bank corrected a fee."); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Reopen(ctx, now, e.owner, july.ID, "Again."); !errors.Is(err, ledgerbus.ErrNotReconciled) {
		t.Errorf("reopening twice: %v", err)
	}

	if _, err := w.ledger.SetSplits(ctx, now, e.owner, e.grocery.ID, sort); err != nil {
		t.Errorf("sorting after reopening: %v", err)
	}

	evs, err := w.history.Recent(ctx, types.AccountScope(e.account), 20)
	if err != nil {
		t.Fatal(err)
	}

	i := slices.IndexFunc(evs, func(ev eventbus.Event) bool { return ev.Action == ledgerbus.StatementReopened })
	if i < 0 || evs[i].Detail["reason"] != "The bank corrected a fee." || evs[i].Detail["end"] != "2026-07-31" {
		t.Errorf("no reopening with its reason in the history: %+v", evs)
	}

	if !slices.ContainsFunc(evs, func(ev eventbus.Event) bool { return ev.Action == ledgerbus.StatementReconciled }) {
		t.Error("no reconciling in the history")
	}
}

func TestTheReconciledPeriodAndWhoMayReconcile(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()
	july := w.statements(e.owner, e.account)[0]

	for _, c := range []struct{ from, to string }{
		{"2026-07-02", "2026-07-31"}, // the file starts on the 1st
		{"2026-07-01", "2026-07-27"}, // and ends on the 28th
		{"2026-05-01", "2026-07-31"}, // more than a month early
		{"2026-07-01", "2026-09-30"}, // more than a month late
	} {
		if _, err := w.ledger.Reconcile(ctx, now, e.owner, july.ID, date(t, c.from), date(t, c.to), ""); !errors.Is(err, ledgerbus.ErrPeriod) {
			t.Errorf("%s to %s: %v", c.from, c.to, err)
		}
	}

	if _, err := w.ledger.Reconcile(ctx, now, e.owner, july.ID, types.Date{}, types.Date{}, strings.Repeat("x", ledgerbus.MaxNote+1)); !errors.Is(err, ledgerbus.ErrNote) {
		t.Errorf("a long note: %v", err)
	}

	viewer := w.user("viewer@example.org")
	w.grant(e.owner, types.AccountScope(e.account), "viewer@example.org", tenancybus.Viewer)

	if _, err := w.ledger.Review(ctx, viewer, july.ID); err != nil {
		t.Errorf("a viewer reads the review: %v", err)
	}

	if _, err := w.ledger.Reconcile(ctx, now, viewer, july.ID, types.Date{}, types.Date{}, ""); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer reconciles: %v", err)
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.ledger.Review(ctx, stranger, july.ID); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger reads the review: %v", err)
	}

	if _, err := w.ledger.Reconcile(ctx, now, stranger, july.ID, types.Date{}, types.Date{}, ""); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger reconciles: %v", err)
	}

	// Without dates it is the statement's own period.
	st, err := w.ledger.Reconcile(ctx, now, e.owner, july.ID, types.Date{}, types.Date{}, "")
	if s, end := st.Period(); err != nil || s.String() != "2026-07-01" || end.String() != "2026-07-28" {
		t.Errorf("the statement's own period: %s to %s, %v", s, end, err)
	}

	if _, err := w.ledger.Reopen(ctx, now, viewer, july.ID, "mine"); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer reopens: %v", err)
	}
}

// statement is one made by hand, for the coverage table.
func statement(t *testing.T, start, end, reconciledTo string) ledgerbus.Statement {
	t.Helper()

	st := ledgerbus.Statement{ID: types.NewID(), Start: date(t, start), End: date(t, end)}
	if reconciledTo != "" {
		st.Reconciliation = ledgerbus.Reconciliation{StatementID: st.ID, Start: st.Start, End: date(t, reconciledTo)}
	}

	return st
}

func TestMonthByMonth(t *testing.T) {
	sts := []ledgerbus.Statement{
		statement(t, "2026-08-02", "2026-08-30", ""),           // August, read from its rows
		statement(t, "2026-06-15", "2026-07-01", "2026-07-01"), // starts mid-June: the account's records begin
		statement(t, "2026-07-02", "2026-07-28", "2026-07-31"), // July, widened to the paper
		statement(t, "2026-09-01", "2026-09-30", ""),           // September, all of it
		statement(t, "2026-09-10", "2026-09-12", "2026-09-12"), // and a scrap of it reconciled
	}

	months := []ledgerbus.Month{{Month: "2026-07", Count: 6, Unsorted: 2}}

	got := ledgerbus.BuildCoverage(sts, months, date(t, "2026-11-07"))

	want := []struct {
		month string
		state ledgerbus.Cover
		gaps  string
		n     int
	}{
		{"2026-11", ledgerbus.Current, "", 0},
		{"2026-10", ledgerbus.Missing, "", 0},
		{"2026-09", ledgerbus.Imported, "", 2},
		{"2026-08", ledgerbus.Partial, "2026-08-01..2026-08-01 2026-08-31..2026-08-31", 1},
		{"2026-07", ledgerbus.Reconciled, "", 2},
		{"2026-06", ledgerbus.Reconciled, "", 1},
	}

	if len(got) != len(want) {
		t.Fatalf("%d months, want %d: %+v", len(got), len(want), got)
	}

	for i, w := range want {
		g := got[i]

		var gaps []string
		for _, s := range g.Gaps {
			gaps = append(gaps, s.From.String()+".."+s.To.String())
		}

		if g.Month != w.month || g.State != w.state || strings.Join(gaps, " ") != w.gaps || len(g.Statements) != w.n {
			t.Errorf("%s: %s, gaps %q, %d statements; want %s %s, %q, %d", g.Month, g.State, gaps, len(g.Statements), w.month, w.state, w.gaps, w.n)
		}
	}

	if got[4].Count != 6 || got[4].Unsorted != 2 {
		t.Errorf("July's sums: %+v", got[4])
	}

	if ledgerbus.BuildCoverage(nil, nil, date(t, "2026-11-07")) != nil {
		t.Error("an account with no statements has months")
	}
}

func TestCoverageAsksForTheAccount(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)

	got, err := w.ledger.Coverage(t.Context(), e.owner, e.account, date(t, "2026-08-15"))
	if err != nil || len(got) != 2 || got[0].State != ledgerbus.Current || got[1].State != ledgerbus.Partial {
		t.Errorf("coverage: %+v, %v", got, err)
	}

	if _, err := w.ledger.Coverage(t.Context(), w.user("stranger@example.org"), e.account, date(t, "2026-08-15")); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger's coverage: %v", err)
	}
}
