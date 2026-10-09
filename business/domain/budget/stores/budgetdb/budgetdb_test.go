package budgetdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/budget/stores/budgetdb"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// One line per category and kind in a budget, NULLs included: two "total
// expenses" lines in one project are refused, though a plain unique index
// would count their NULL categories as different. A budget's lines read
// back as they were written, and another budget's are its own.
func TestOneLinePerCategoryAndKind(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, categorydb.Init, budgetdb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, budgetdb.Expected); err != nil {
		t.Fatal(err)
	}

	// The projects the lines point at are not there; the references are
	// not what this is about. One connection, so the pragma holds.
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	store := budgetdb.NewStore(db)
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	project, other := types.ProjectScope(types.NewID()), types.ProjectScope(types.NewID())

	line := func(scope types.Scope, category types.ID, kind categorybus.Kind, cents int64) budgetbus.Line {
		return budgetbus.Line{
			ID: types.NewID(), Scope: scope, CategoryID: category, Kind: kind, Amount: money.FromCents(cents),
			Currency: "USD", UpdatedBy: types.NewID(), UpdatedAt: now,
		}
	}

	travel := types.NewID()
	want := []budgetbus.Line{line(project, types.ID{}, categorybus.Expense, 100000), line(project, travel, categorybus.Expense, 50000)}

	if err := store.Replace(ctx, project, 0, want, nil); err != nil {
		t.Fatal(err)
	}

	if err := store.Replace(ctx, other, 0, []budgetbus.Line{line(other, types.ID{}, categorybus.Expense, 1)}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Lines(ctx, project, 0)
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the lines: %+v %v", got, err)
	}

	twice := []budgetbus.Line{line(project, types.ID{}, categorybus.Expense, 1), line(project, types.ID{}, categorybus.Expense, 2)}
	if err := store.Replace(ctx, project, 0, twice, nil); err == nil {
		t.Error("two totals of one kind were kept")
	}

	if got, _ := store.Lines(ctx, project, 0); len(got) != 2 {
		t.Errorf("a refused save changed the budget: %+v", got)
	}
}
