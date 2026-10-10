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

// step5Schema is the ledger's tables as the first release with statements
// made them, before splits: written out, not derived from Init, because the
// point is a database that was made by a different binary.
const step5Schema = `
CREATE TABLE IF NOT EXISTS statements (
    id           TEXT    PRIMARY KEY,
    account_id   TEXT    NOT NULL REFERENCES accounts (id),
    file_id      TEXT    NOT NULL REFERENCES files (id),
    format       TEXT    NOT NULL,
    period_start TEXT    NOT NULL,
    period_end   TEXT    NOT NULL,
    opening      INTEGER,
    closing      INTEGER,
    checked      TEXT    NOT NULL,
    added        INTEGER NOT NULL DEFAULT 0,
    already      INTEGER NOT NULL DEFAULT 0,
    imported_by  TEXT    NOT NULL,
    imported_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS statements_account ON statements (account_id, period_end);

CREATE TABLE IF NOT EXISTS transactions (
    id           TEXT    PRIMARY KEY,
    account_id   TEXT    NOT NULL REFERENCES accounts (id),
    statement_id TEXT    NOT NULL REFERENCES statements (id),
    posted_on    TEXT    NOT NULL,
    description  TEXT    NOT NULL,
    amount       INTEGER NOT NULL,
    balance      INTEGER,
    external_id  TEXT    NOT NULL DEFAULT '',
    hash         TEXT    NOT NULL,
    occurrence   INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS transactions_external ON transactions (account_id, external_id) WHERE external_id <> '';
CREATE INDEX IF NOT EXISTS transactions_hash ON transactions (account_id, hash);
CREATE INDEX IF NOT EXISTS transactions_posted ON transactions (account_id, posted_on);
CREATE INDEX IF NOT EXISTS transactions_statement ON transactions (statement_id);

CREATE TABLE IF NOT EXISTS csv_mappings (
    account_id  TEXT    NOT NULL REFERENCES accounts (id),
    fingerprint TEXT    NOT NULL,
    mapping     TEXT    NOT NULL,
    updated_by  TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (account_id, fingerprint)
) STRICT;
`

// A database from before splits gains the table, and every transaction in
// it one part for its whole amount -- once, however often Init runs.
func TestInitGivesOldTransactionsTheirPart(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	// The tables the ledger's point at, as main makes them before it.
	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	// The rows below point at accounts and statements that are not there,
	// so the references are not checked while they are written. One
	// connection, so the pragma holds.
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, step5Schema); err != nil {
		t.Fatal(err)
	}

	ids := []types.ID{types.NewID(), types.NewID()}
	for i, id := range ids {
		if _, err := db.ExecContext(ctx, `
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, hash, occurrence)
VALUES (?, ?, ?, '2026-07-01', 'CORNER GROCERY', ?, ?, 1)`,
			id.String(), types.NewID().String(), types.NewID().String(), -100*(i+1), id.String()); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := ledgerdb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	store := ledgerdb.NewStore(db)

	for i, id := range ids {
		tx, err := store.TransactionByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		if len(tx.Splits) != 1 || tx.Splits[0].Amount != tx.Amount || tx.Amount.Cents() != int64(-100*(i+1)) {
			t.Errorf("transaction %d's parts: %+v", i, tx.Splits)
		}
	}

	if err := sqldb.CheckSchema(ctx, db, ledgerdb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}

// splitsBeforeRules is the splits table as the release before sorting rules
// made it, written out for the same reason as step5Schema.
const splitsBeforeRules = `
CREATE TABLE IF NOT EXISTS splits (
    id             TEXT    PRIMARY KEY,
    transaction_id TEXT    NOT NULL REFERENCES transactions (id) ON DELETE CASCADE,
    position       INTEGER NOT NULL,
    amount         INTEGER NOT NULL,
    category_id    TEXT    REFERENCES categories (id),
    project_id     TEXT    REFERENCES projects (id),
    memo           TEXT    NOT NULL DEFAULT ''
) STRICT;
`

// A database from before sorting rules gains splits.rule_id, and a part
// already there reads as one no rule sorted. From before cardholders,
// pending charges and check numbers too, it gains transactions.holder,
// pending and check_number, and the row names nobody, has posted, and paid
// no check.
func TestInitGivesOldPartsNoRule(t *testing.T) {
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

	if _, err := db.ExecContext(ctx, step5Schema+splitsBeforeRules); err != nil {
		t.Fatal(err)
	}

	id := types.NewID()
	if _, err := db.ExecContext(ctx, `
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, hash, occurrence)
VALUES (?, ?, ?, '2026-07-01', 'CORNER GROCERY', -100, ?, 1)`,
		id.String(), types.NewID().String(), types.NewID().String(), id.String()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO splits (id, transaction_id, position, amount, memo) VALUES (?, ?, 0, -100, 'bread')`,
		types.NewID().String(), id.String()); err != nil {
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

	tx, err := ledgerdb.NewStore(db).TransactionByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if len(tx.Splits) != 1 || tx.Splits[0].Memo != "bread" || !tx.Splits[0].RuleID.Zero() || tx.ByRule() {
		t.Errorf("the part: %+v", tx.Splits)
	}

	if tx.Holder != "" || tx.Hash != id.String() || tx.Pending || tx.CheckNumber != "" {
		t.Errorf("the old row: holder %q, hash %q, pending %v, check %q", tx.Holder, tx.Hash, tx.Pending, tx.CheckNumber)
	}

	if err := sqldb.CheckSchema(ctx, db, ledgerdb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}
