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

// payeeBefore is how the release before payees left a transaction's payee:
// the text a person read off a check's image, a later column on the table
// step5Schema made, with no payee it is.
const payeeBefore = `ALTER TABLE transactions ADD COLUMN payee TEXT NOT NULL DEFAULT ''`

// A database from before payees gives every transaction that says whom it
// was paid to the payee of its list written so: one per way of writing
// it, whatever its case, joining none, and each list its own -- once,
// however often Init runs.
func TestInitGivesOldPaymentsTheirPayees(t *testing.T) {
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

	for _, q := range []string{step5Schema, payeeBefore} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	org, checking, card, own, by := types.NewID(), types.NewID(), types.NewID(), types.NewID(), types.NewID()

	if _, err := db.ExecContext(ctx, `
INSERT INTO orgs (id, name, created_by, created_at) VALUES (?1, 'St. Joseph Parish', ?5, 1);
INSERT INTO accounts (id, org_id, name, kind, created_by, created_at) VALUES (?2, ?1, 'Parish checking', 'checking', ?5, 1);
INSERT INTO accounts (id, org_id, name, kind, created_by, created_at) VALUES (?3, ?1, 'Parish card', 'card', ?5, 1);
INSERT INTO accounts (id, org_id, name, kind, created_by, created_at) VALUES (?4, NULL, 'Youth group', 'checking', ?5, 1);`,
		org.String(), checking.String(), card.String(), own.String(), by.String()); err != nil {
		t.Fatal(err)
	}

	type row struct {
		account types.ID
		payee   string
	}

	rows := []row{
		{checking, "Hilltop Plumbing"},
		{card, "HILLTOP PLUMBING"},
		{checking, "Hilltop Plumbing LLC"},
		{own, "Hilltop Plumbing"},
		{checking, ""},
	}

	ids := make([]types.ID, len(rows))

	for i, r := range rows {
		ids[i] = types.NewID()

		if _, err := db.ExecContext(ctx, `
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, hash, occurrence, payee)
VALUES (?, ?, ?, '2026-07-01', 'CHECK', -100, ?, 1, ?)`,
			ids[i].String(), r.account.String(), types.NewID().String(), ids[i].String(), r.payee); err != nil {
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

	if err := sqldb.CheckSchema(ctx, db, ledgerdb.Expected); err != nil {
		t.Fatalf("the schema after Init: %v", err)
	}

	store := ledgerdb.NewStore(db)

	payeeOf := func(i int) types.ID {
		t.Helper()

		tx, err := store.TransactionByID(ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}

		if tx.Payee != rows[i].payee {
			t.Errorf("transaction %d's text changed: %q", i, tx.Payee)
		}

		return tx.PayeeID
	}

	switch {
	case payeeOf(0).Zero() || payeeOf(0) != payeeOf(1):
		t.Error("one spelling in two cases is not one payee")
	case payeeOf(2).Zero() || payeeOf(2) == payeeOf(0):
		t.Error("a second spelling was not a payee of its own")
	case payeeOf(3).Zero() || payeeOf(3) == payeeOf(0):
		t.Error("an account with no organization does not have its own list")
	case !payeeOf(4).Zero():
		t.Error("a transaction paid to nobody was given a payee")
	}

	list, err := store.Payees(ctx, types.OrgScope(org))
	if err != nil || len(list) != 2 {
		t.Fatalf("the parish's payees: %+v, %v", list, err)
	}

	for _, p := range list {
		if p.Name == "Hilltop Plumbing" && (p.Payments != 2 || len(p.Spellings) != 1 || p.CreatedBy != by) {
			t.Errorf("the plumber: %+v", p)
		}
	}

	if mine, err := store.Payees(ctx, types.AccountScope(own)); err != nil || len(mine) != 1 || mine[0].Payments != 1 {
		t.Errorf("the youth group's payees: %+v, %v", mine, err)
	}
}
