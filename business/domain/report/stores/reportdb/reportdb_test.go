package reportdb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/domain/report/stores/reportdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Init runs twice without harm and leaves what Expected says; a note
// written twice for one period is one note, the later; every month there
// is has a note of its own.
func TestTheTables(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := eventdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := reportdb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, reportdb.Expected); err != nil {
		t.Fatal(err)
	}

	store := reportdb.NewStore(db)
	now := time.UnixMilli(1_790_000_000_000).UTC()
	who := types.NewID()
	scope := types.OrgScope(types.NewID())
	ev := func() eventbus.Event { return eventbus.New(now, who, scope, reportbus.ReportNoted, nil) }

	r := reportbus.Saved{ID: types.NewID(), Scope: scope, Style: reportbus.IncomeExpensesByMonth, Title: "P&L", Choices: "{}",
		CreatedBy: who, CreatedAt: now, UpdatedBy: who, UpdatedAt: now}
	if err := store.Create(ctx, r, ev()); err != nil {
		t.Fatal(err)
	}

	if got, err := store.ByID(ctx, r.ID); err != nil || got.Title != "P&L" || got.Scope != scope || got.Removed() {
		t.Fatalf("read back: %+v %v", got, err)
	}

	july := reportbus.Period{From: "2026-07", To: "2026-07"}

	for _, text := range []string{"First", "Second"} {
		if err := store.SetNote(ctx, reportbus.Note{ReportID: r.ID, Period: july, Text: text, Figures: "f", By: who, At: now}, ev()); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.SetNote(ctx, reportbus.Note{ReportID: r.ID, Period: reportbus.Period{All: true}, Text: "All of it", By: who, At: now}, ev()); err != nil {
		t.Fatal(err)
	}

	if n, ok, err := store.Note(ctx, r.ID, july); err != nil || !ok || n.Text != "Second" || n.Figures != "f" {
		t.Errorf("July's note: %+v %v %v", n, ok, err)
	}

	if n, ok, err := store.Note(ctx, r.ID, reportbus.Period{All: true}); err != nil || !ok || n.Text != "All of it" {
		t.Errorf("every month's note: %+v %v %v", n, ok, err)
	}

	if err := store.RemoveNote(ctx, r.ID, july, ev()); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := store.Note(ctx, r.ID, july); err != nil || ok {
		t.Errorf("July's note after removing it: %v %v", ok, err)
	}
}
