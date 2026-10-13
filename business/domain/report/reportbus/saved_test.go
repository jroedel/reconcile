package reportbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// The house saves its report as "P&L": run without a period it is the
// year so far; July's note is on July's run and no other; sorting the debit
// nobody had explained changes July's figures, and the run says so until
// the note is written again; an empty note removes it.
func TestASavedReportAndItsNotes(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()
	scope := types.OrgScope(h.org)

	s, err := w.reports.Save(ctx, now, h.owner, scope, reportbus.IncomeExpensesByMonth, reportbus.Fields{
		Title: "  P&L ", Description: "Our true income and expenses.\r\nCard charges count when made.", Period: reportbus.YearToDate,
	})
	if err != nil {
		t.Fatal(err)
	}

	if s.Title != "P&L" || s.Description != "Our true income and expenses.\nCard charges count when made." {
		t.Errorf("as saved: %q %q", s.Title, s.Description)
	}

	if list, err := w.reports.SavedOf(ctx, h.owner, scope); err != nil || len(list) != 1 || list[0].ID != s.ID {
		t.Errorf("the house's reports: %+v %v", list, err)
	}

	run, err := w.reports.Run(ctx, now, h.owner, s.ID, reportbus.Period{})
	if err != nil || !run.Known || run.Report.Period != period(t, "2026-01", "2026-07") || run.HasNote {
		t.Fatalf("run with no period: %+v %v", run.Report.Period, err)
	}

	july := period(t, "2026-07", "2026-07")

	if err := w.reports.SetNote(ctx, now, h.owner, s.ID, july, "The national office's flight is under Travel."); err != nil {
		t.Fatal(err)
	}

	runOf := func(p reportbus.Period) reportbus.SavedRun {
		t.Helper()

		run, err := w.reports.Run(ctx, now, h.owner, s.ID, p)
		if err != nil {
			t.Fatal(err)
		}

		return run
	}

	if r := runOf(july); !r.HasNote || r.Note.Text != "The national office's flight is under Travel." || r.Note.By != h.owner || r.Changed {
		t.Errorf("July: %+v changed %v", r.Note, r.Changed)
	}

	if r := runOf(period(t, "2026-07", "2026-08")); r.HasNote {
		t.Error("July's note is on July and August's run")
	}

	// The debit nobody had explained turns out to be groceries.
	txs, err := w.ledger.Transactions(ctx, h.owner, h.checking, "2026-07")
	if err != nil {
		t.Fatal(err)
	}

	for _, tx := range txs {
		if tx.Description == "DEBIT 0042" {
			if _, err := w.ledger.SetSplits(ctx, now, h.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount, CategoryID: h.groceries}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if r := runOf(july); !r.Changed {
		t.Error("the figures changed under July's note and the run does not say so")
	}

	later := now.Add(time.Hour)
	if err := w.reports.SetNote(ctx, later, h.owner, s.ID, july, "Everything is sorted now."); err != nil {
		t.Fatal(err)
	}

	if r := runOf(july); r.Changed || r.Note.Text != "Everything is sorted now." || !r.Note.At.Equal(later) {
		t.Errorf("written again: %+v changed %v", r.Note, r.Changed)
	}

	if err := w.reports.SetNote(ctx, later, h.owner, s.ID, july, "  "); err != nil {
		t.Fatal(err)
	}

	if r := runOf(july); r.HasNote {
		t.Error("an empty note did not remove it")
	}

	if err := w.reports.SetNote(ctx, now, h.owner, s.ID, reportbus.Period{}, "For no months"); err == nil {
		t.Error("a note for no period was written")
	}
}

// Saved with the card and the national office's account, the report is
// of the card, and names the national account as left out -- it is for
// reference -- and counts an account that no longer exists.
func TestASavedReportsAccountsThatAreNotTheBooks(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()

	s, err := w.reports.Save(ctx, now, h.owner, types.OrgScope(h.org), reportbus.IncomeExpensesByMonth, reportbus.Fields{
		Title: "The card", Choices: reportbus.IncomeExpensesChoices{Accounts: []types.ID{h.card, h.national, types.NewID()}, PassThrough: reportbus.EachCategory},
	})
	if err != nil {
		t.Fatal(err)
	}

	run, err := w.reports.Run(ctx, now, h.owner, s.ID, period(t, "2026-07", "2026-07"))
	if err != nil {
		t.Fatal(err)
	}

	if len(run.Report.Chosen) != 1 || run.Report.Chosen[0].ID != h.card || run.Report.Choices.PassThrough != reportbus.EachCategory {
		t.Errorf("the report's accounts: %+v", run.Report.Chosen)
	}

	if len(run.LeftOut) != 1 || run.LeftOut[0].ID != h.national || run.Missing != 1 {
		t.Errorf("left out %+v, missing %d", run.LeftOut, run.Missing)
	}
}

// A viewer runs a saved report and cannot save, change, note or remove
// one; a stranger finds none. A title is needed, and a period is one of
// those offered. A changed title is in the history; a removed report is
// gone from the list and from its address.
func TestWhoKeepsASavedReport(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()
	scope := types.OrgScope(h.org)

	viewer := w.user("viewer@example.org")
	e, _ := types.ParseEmail("viewer@example.org")

	if _, _, err := w.ten.Grant(ctx, now, h.owner, scope, e, tenancybus.Viewer); err != nil {
		t.Fatal(err)
	}

	stranger := w.user("stranger@example.org")

	for name, f := range map[string]reportbus.Fields{
		"title":   {Title: " "},
		"default": {Title: "P&L", Period: "fortnight"},
	} {
		_, err := w.reports.Save(ctx, now, h.owner, scope, reportbus.IncomeExpensesByMonth, f)
		if invalid, ok := errors.AsType[reportbus.Invalid](err); !ok || invalid.Field != name {
			t.Errorf("%s: %v", name, err)
		}
	}

	if _, err := w.reports.Save(ctx, now, h.owner, scope, "weekly", reportbus.Fields{Title: "Weekly"}); err == nil {
		t.Error("a style nobody wrote was saved")
	}

	s, err := w.reports.Save(ctx, now, h.owner, scope, reportbus.IncomeExpensesByMonth, reportbus.Fields{Title: "P&L"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.reports.Run(ctx, now, viewer, s.ID, reportbus.Period{}); err != nil {
		t.Errorf("the viewer running it: %v", err)
	}

	for who, want := range map[types.ID]error{viewer: reportbus.ErrForbidden, stranger: reportbus.ErrNotFound} {
		if _, err := w.reports.Save(ctx, now, who, scope, reportbus.IncomeExpensesByMonth, reportbus.Fields{Title: "Mine"}); !errors.Is(err, want) {
			t.Errorf("Save: %v, want %v", err, want)
		}

		if _, err := w.reports.Change(ctx, now, who, s.ID, reportbus.Fields{Title: "Mine"}); !errors.Is(err, want) {
			t.Errorf("Change: %v, want %v", err, want)
		}

		if err := w.reports.SetNote(ctx, now, who, s.ID, period(t, "2026-07", "2026-07"), "Mine"); !errors.Is(err, want) {
			t.Errorf("SetNote: %v, want %v", err, want)
		}

		if err := w.reports.Remove(ctx, now, who, s.ID); !errors.Is(err, want) {
			t.Errorf("Remove: %v, want %v", err, want)
		}
	}

	if _, err := w.reports.Run(ctx, now, stranger, s.ID, reportbus.Period{}); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("a stranger running it: %v", err)
	}

	if c, err := w.reports.Change(ctx, now, h.owner, s.ID, reportbus.Fields{Title: "Profit and loss", Period: reportbus.LastMonth}); err != nil || c.Title != "Profit and loss" {
		t.Fatalf("renamed: %+v %v", c, err)
	}

	if run, err := w.reports.Run(ctx, now, h.owner, s.ID, reportbus.Period{}); err != nil || run.Report.Period != period(t, "2026-07", "2026-07") {
		t.Errorf("its new default period: %+v %v", run.Report.Period, err)
	}

	if err := w.reports.Remove(ctx, now, h.owner, s.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := w.reports.Run(ctx, now, h.owner, s.ID, reportbus.Period{}); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("a removed report: %v", err)
	}

	if list, err := w.reports.SavedOf(ctx, h.owner, scope); err != nil || len(list) != 0 {
		t.Errorf("the list after removing it: %+v %v", list, err)
	}
}

// A report saved with a style this code no longer has runs as that and
// nothing else; one saved with a choice since removed reads without it.
func TestASavedReportOfAnotherRelease(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()

	gone := reportbus.Saved{
		ID: types.NewID(), Scope: types.OrgScope(h.org), Style: "weekly", Title: "Weekly",
		Choices: `{"passthrough":"each","rounding":"dollars"}`, CreatedBy: h.owner, CreatedAt: now, UpdatedBy: h.owner, UpdatedAt: now,
	}

	if err := w.store.Create(ctx, gone, eventbus.New(now, h.owner, gone.Scope, reportbus.ReportSaved, nil)); err != nil {
		t.Fatal(err)
	}

	run, err := w.reports.Run(ctx, now, h.owner, gone.ID, reportbus.Period{})
	if err != nil || run.Known || run.Frame.Name != "Holy Family House" {
		t.Errorf("a style no longer here: known %v, %v", run.Known, err)
	}

	if c := gone.IncomeExpensesChoices(); c.PassThrough != reportbus.EachCategory || len(c.Accounts) != 0 {
		t.Errorf("its choices: %+v", c)
	}

	if c := (reportbus.Saved{Choices: "not json"}).IncomeExpensesChoices(); c.PassThrough != reportbus.OneTotal {
		t.Errorf("choices that cannot be read: %+v", c)
	}
}
