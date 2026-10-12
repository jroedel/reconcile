package categorydb_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// before is the categories table as it stood before kinds, written out
// rather than derived (CLAUDE.md): every database on a server that ran the
// release before this one looks like this.
const before = `
CREATE TABLE IF NOT EXISTS categories (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    account_id  TEXT    REFERENCES accounts (id),
    name        TEXT    NOT NULL,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER,
    CHECK ((org_id IS NULL) <> (account_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS categories_name ON categories (coalesce(org_id, account_id), name COLLATE NOCASE);
`

// A database from before kinds gets the column, keeps its categories with
// no kind said, and takes one when the owner says.
func TestInitOverTheSchemaBefore(t *testing.T) {
	ctx := t.Context()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return userdb.Init(ctx, db) },
		func() error { return eventdb.Init(ctx, db) },
		func() error { return tenancydb.Init(ctx, db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, before); err != nil {
		t.Fatal(err)
	}

	org, cat := types.NewID(), types.NewID()

	if _, err := db.ExecContext(ctx, `
INSERT INTO orgs (id, name, created_by, created_at) VALUES (?1, 'St. Joseph Parish', ?3, 1);
INSERT INTO categories (id, org_id, name, created_by, created_at) VALUES (?2, ?1, 'Utilities', ?3, 1);`,
		org.String(), cat.String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := categorydb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, categorydb.Expected); err != nil {
		t.Fatal(err)
	}

	store := categorydb.NewStore(db)

	got, err := store.ByID(ctx, cat)
	if err != nil || got.Name != "Utilities" || got.Kind != categorybus.Unsaid {
		t.Fatalf("the old row: %+v, %v", got, err)
	}

	got.Kind = categorybus.Expense

	if err := store.Update(ctx, got, eventbus.New(time.Now(), types.NewID(), types.OrgScope(org), categorybus.KindSet, nil)); err != nil {
		t.Fatal(err)
	}

	if again, _ := store.ByID(ctx, cat); again.Kind != categorybus.Expense {
		t.Errorf("after: %+v", again)
	}
}

// beforeGroups is the categories table as it stood with kinds and before
// groups, written out for the same reason.
const beforeGroups = `
CREATE TABLE IF NOT EXISTS categories (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    account_id  TEXT    REFERENCES accounts (id),
    name        TEXT    NOT NULL,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER,
    kind        TEXT    NOT NULL DEFAULT '',
    CHECK ((org_id IS NULL) <> (account_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS categories_name ON categories (coalesce(org_id, account_id), name COLLATE NOCASE);
`

// A database from before groups gets the table and the column; its
// categories are under no group until somebody puts them under one, and
// one put under a group reads back so.
func TestInitOverTheSchemaBeforeGroups(t *testing.T) {
	ctx := t.Context()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return userdb.Init(ctx, db) },
		func() error { return eventdb.Init(ctx, db) },
		func() error { return tenancydb.Init(ctx, db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, beforeGroups); err != nil {
		t.Fatal(err)
	}

	org, cat, who := types.NewID(), types.NewID(), types.NewID()

	if _, err := db.ExecContext(ctx, `
INSERT INTO orgs (id, name, created_by, created_at) VALUES (?1, 'St. Joseph Parish', ?3, 1);
INSERT INTO categories (id, org_id, name, kind, created_by, created_at) VALUES (?2, ?1, 'Groceries', 'expense', ?3, 1);`,
		org.String(), cat.String(), who.String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := categorydb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, categorydb.Expected); err != nil {
		t.Fatal(err)
	}

	store := categorydb.NewStore(db)

	got, err := store.ByID(ctx, cat)
	if err != nil || got.Kind != categorybus.Expense || !got.GroupID.Zero() {
		t.Fatalf("the old row: %+v, %v", got, err)
	}

	now := time.UnixMilli(2).UTC()
	ev := func() eventbus.Event {
		return eventbus.New(now, who, types.OrgScope(org), categorybus.HeadingAdded, nil)
	}

	food := categorybus.Heading{ID: types.NewID(), Owner: types.OrgScope(org), Kind: categorybus.Expense, Name: "Food", CreatedBy: who, CreatedAt: now}
	if err := store.CreateHeading(ctx, food, ev()); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateHeading(ctx, categorybus.Heading{ID: types.NewID(), Owner: types.OrgScope(org), Kind: categorybus.Income, Name: "food", CreatedBy: who, CreatedAt: now}, ev()); !errors.Is(err, categorybus.ErrDuplicate) {
		t.Errorf("a second group named food: %v", err)
	}

	got.GroupID = food.ID
	if err := store.Update(ctx, got, ev()); err != nil {
		t.Fatal(err)
	}

	if again, _ := store.ByID(ctx, cat); again.GroupID != food.ID {
		t.Errorf("after: %+v", again)
	}

	if hs, err := store.HeadingsOf(ctx, types.OrgScope(org)); err != nil || len(hs) != 1 || hs[0].Name != "Food" || hs[0].Kind != categorybus.Expense {
		t.Errorf("the list's groups: %+v %v", hs, err)
	}
}
