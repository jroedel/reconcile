package ledgerdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// payeesFirst is the payees as the first release of them made them,
// written out as it stood: no mark of a key on a spelling, and none on a
// transaction's payee.
const payeesFirst = `
CREATE TABLE payees (
    id              TEXT    PRIMARY KEY,
    org_id          TEXT    REFERENCES orgs (id),
    account_id      TEXT    REFERENCES accounts (id),
    name            TEXT    NOT NULL,
    reported        INTEGER NOT NULL DEFAULT 0 CHECK (reported IN (0, 1)),
    details_on_file INTEGER NOT NULL DEFAULT 0 CHECK (details_on_file IN (0, 1)),
    note            TEXT    NOT NULL DEFAULT '',
    via             TEXT    NOT NULL DEFAULT '',
    joined_into     TEXT    REFERENCES payees (id),
    created_by      TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    archived_at     INTEGER,
    CHECK ((org_id IS NULL) <> (account_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX payees_name ON payees (coalesce(org_id, account_id), name COLLATE NOCASE) WHERE joined_into IS NULL;

CREATE TABLE payee_names (
    owner    TEXT NOT NULL,
    name     TEXT NOT NULL COLLATE NOCASE,
    payee_id TEXT NOT NULL REFERENCES payees (id),
    PRIMARY KEY (owner, name)
) STRICT;

CREATE INDEX payee_names_payee ON payee_names (payee_id);

CREATE TABLE payee_not_same (
    a       TEXT    NOT NULL REFERENCES payees (id),
    b       TEXT    NOT NULL REFERENCES payees (id),
    said_by TEXT    NOT NULL,
    said_at INTEGER NOT NULL,
    PRIMARY KEY (a, b),
    CHECK (a < b)
) STRICT;

ALTER TABLE transactions ADD COLUMN payee_id TEXT REFERENCES payees (id);
CREATE INDEX transactions_payee ON transactions (payee_id) WHERE payee_id IS NOT NULL;`

// A database from the first release of payees gets the marks a key leaves
// on a spelling and on a payment's payee, every payee and payment it had
// unmarked, and its payees and payments kept.
func TestInitBringsPayeesTheirMarks(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{step5Schema, payeeBefore, payeesFirst} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	org, checking, payee, tx, by := types.NewID(), types.NewID(), types.NewID(), types.NewID(), types.NewID()

	if _, err := db.ExecContext(ctx, `
INSERT INTO orgs (id, name, created_by, created_at) VALUES (?1, 'St. Joseph Parish', ?5, 1);
INSERT INTO accounts (id, org_id, name, kind, created_by, created_at) VALUES (?2, ?1, 'Parish checking', 'checking', ?5, 1);
INSERT INTO payees (id, org_id, name, created_by, created_at) VALUES (?3, ?1, 'Hilltop Plumbing', ?5, 1);
INSERT INTO payee_names (owner, name, payee_id) VALUES (?1, 'Hilltop Plumbing', ?3);
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, hash, occurrence, payee, payee_id)
VALUES (?4, ?2, ?4, '2026-07-01', 'CHECK', -100, ?4, 1, 'Hilltop Plumbing', ?3);`,
		org.String(), checking.String(), payee.String(), tx.String(), by.String()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := ledgerdb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, ledgerdb.Expected); err != nil {
		t.Fatalf("the schema after Init: %v", err)
	}

	store := ledgerdb.NewStore(db)

	got, err := store.TransactionByID(ctx, tx)
	if err != nil || got.PayeeID != payee || got.PayeeVia != "" || got.Payee != "Hilltop Plumbing" {
		t.Errorf("the payment: %+v, %v", got, err)
	}

	p, err := store.PayeeByID(ctx, payee)
	if err != nil || p.Marked() || p.Payments != 1 || len(p.Spellings) != 1 {
		t.Errorf("the payee: %+v, %v", p, err)
	}
}
