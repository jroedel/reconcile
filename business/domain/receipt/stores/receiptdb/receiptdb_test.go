package receiptdb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
	"github.com/jroedel/reconcile/business/domain/receipt/stores/receiptdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// receiptsBeforeChecks is the receipts table as the release before check
// images made it: written out, not derived from Init, because the point
// is what a database already holds.
const receiptsBeforeChecks = `
CREATE TABLE receipts (
    id          TEXT    PRIMARY KEY,
    account_id  TEXT    REFERENCES accounts (id),
    project_id  TEXT    REFERENCES projects (id),
    uploaded_by TEXT    NOT NULL,
    spent_on    TEXT    NOT NULL DEFAULT '',
    amount      INTEGER,
    merchant    TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    removed_at  INTEGER,
    CHECK ((account_id IS NULL) <> (project_id IS NULL))
) STRICT;
`

// A database from before check images gains receipts.check_number, and a
// receipt already there is the image of no check.
func TestInitGivesOldReceiptsNoCheck(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ledgerdb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, receiptsBeforeChecks); err != nil {
		t.Fatal(err)
	}

	id := types.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO receipts (id, account_id, uploaded_by, merchant, created_at) VALUES (?, ?, ?, 'Corner Grocery', 1)`,
		id.String(), types.NewID().String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := receiptdb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	r, err := receiptdb.NewStore(db).ByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if r.Check != "" || r.Merchant != "Corner Grocery" {
		t.Errorf("the old receipt: check %q, merchant %q", r.Check, r.Merchant)
	}

	if err := sqldb.CheckSchema(ctx, db, receiptdb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}

// receiptsBeforeCheckImages is the receipts table as the release before
// check images could wait made it: a check image then always had its
// number, and nothing else said it was one.
const receiptsBeforeCheckImages = `
CREATE TABLE receipts (
    id           TEXT    PRIMARY KEY,
    account_id   TEXT    REFERENCES accounts (id),
    project_id   TEXT    REFERENCES projects (id),
    uploaded_by  TEXT    NOT NULL,
    spent_on     TEXT    NOT NULL DEFAULT '',
    amount       INTEGER,
    merchant     TEXT    NOT NULL DEFAULT '',
    note         TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    removed_at   INTEGER,
    check_number TEXT    NOT NULL DEFAULT '',
    CHECK ((account_id IS NULL) <> (project_id IS NULL))
) STRICT;
`

// A database from before gains receipts.check_image: a receipt with a
// check's number is a check image, and one without is not.
func TestInitMarksOldCheckImages(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ledgerdb.Init} {
		if err := init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, receiptsBeforeCheckImages); err != nil {
		t.Fatal(err)
	}

	check, grocery := types.NewID(), types.NewID()

	for id, number := range map[types.ID]string{check: "1176", grocery: ""} {
		if _, err := db.ExecContext(ctx, `INSERT INTO receipts (id, account_id, uploaded_by, check_number, created_at) VALUES (?, ?, ?, ?, 1)`,
			id.String(), types.NewID().String(), types.NewID().String(), number); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := receiptdb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	store := receiptdb.NewStore(db)

	if r, err := store.ByID(ctx, check); err != nil || !r.CheckImage || r.Check != "1176" {
		t.Errorf("the old check image: %+v, %v", r, err)
	}

	if r, err := store.ByID(ctx, grocery); err != nil || r.CheckImage {
		t.Errorf("the old receipt: %+v, %v", r, err)
	}

	if err := sqldb.CheckSchema(ctx, db, receiptdb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}
