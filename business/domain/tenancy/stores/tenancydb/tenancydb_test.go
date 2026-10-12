package tenancydb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// orgsBeforeBudgets is the orgs table as the release before budget years
// made it: written out, not derived from Init, because the point is a
// database made by a different binary.
const orgsBeforeBudgets = `
CREATE TABLE IF NOT EXISTS orgs (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;
`

// An organization made before budget years has one that starts in
// January, however often Init runs.
func TestInitGivesOldOrganizationsAJanuaryYear(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := userdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, orgsBeforeBudgets); err != nil {
		t.Fatal(err)
	}

	id := types.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO orgs (id, name, created_by, created_at) VALUES (?, 'St. Joseph Parish', ?, 1)`,
		id.String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := tenancydb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	o, err := tenancydb.NewStore(db).OrgByID(ctx, id)
	if err != nil || o.FiscalStart != 1 || o.Name != "St. Joseph Parish" {
		t.Errorf("the organization: %+v %v", o, err)
	}

	if err := sqldb.CheckSchema(ctx, db, tenancydb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}

// projectsBeforeVia is the projects table as the release before projects
// made through a key (issue #77) made it, written out for the same reason.
const projectsBeforeVia = `
CREATE TABLE IF NOT EXISTS projects (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    name        TEXT    NOT NULL,
    starts_on   TEXT    NOT NULL DEFAULT '',
    ends_on     TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;
`

// A project made before projects could be made through a key was made by
// a person, and carries no program's mark, however often Init runs.
func TestInitGivesOldProjectsNoProgramsMark(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := userdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, ddl := range []string{orgsBeforeBudgets, projectsBeforeVia} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	id := types.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, created_by, created_at) VALUES (?, 'Building fund', ?, 1)`,
		id.String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := tenancydb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	p, err := tenancydb.NewStore(db).ProjectByID(ctx, id)
	if err != nil || p.Via != "" || p.Name != "Building fund" {
		t.Errorf("the project: %+v %v", p, err)
	}

	if err := sqldb.CheckSchema(ctx, db, tenancydb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}

// accountsBeforeReference is the accounts table as the release before
// accounts for reference (docs/plan.md, "Whose books") made it, written out
// for the same reason.
const accountsBeforeReference = `
CREATE TABLE IF NOT EXISTS accounts (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    name        TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    last4       TEXT    NOT NULL DEFAULT '',
    currency    TEXT    NOT NULL DEFAULT 'USD',
    opened_on   TEXT    NOT NULL DEFAULT '',
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;
`

// An account made before accounts for reference is part of its books, as
// it was, however often Init runs; and the mark, once set, reads back.
func TestInitKeepsOldAccountsInTheirBooks(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := userdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, ddl := range []string{orgsBeforeBudgets, accountsBeforeReference} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	id := types.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, name, kind, created_by, created_at) VALUES (?, 'Parish checking', 'checking', ?, 1)`,
		id.String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := tenancydb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	// Marking it writes its history line beside it.
	if err := eventdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := tenancydb.NewStore(db)

	a, err := store.AccountByID(ctx, id)
	if err != nil || a.Reference || a.Name != "Parish checking" {
		t.Fatalf("the account: %+v %v", a, err)
	}

	if err := sqldb.CheckSchema(ctx, db, tenancydb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}

	a.Reference = true
	if err := store.UpdateAccount(ctx, a, eventbus.New(time.UnixMilli(2), types.NewID(), a.Scope(), tenancybus.AccountReference, nil)); err != nil {
		t.Fatal(err)
	}

	if a, err := store.AccountByID(ctx, id); err != nil || !a.Reference {
		t.Errorf("after marking it: %+v %v", a, err)
	}
}
