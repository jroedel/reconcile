package ledgerdb

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// settle finds the pending charge a posted row is by each of its three
// ways -- the row's hash, the bank's identifier, a wording remembered for
// it -- since they are three lookups of their own (settle's comment), and
// leaves a charge that is no longer pending alone.
func TestSettleFindsAPendingChargeEachWay(t *testing.T) {
	// Without the foreign-key pragma, as in the tripwire's test, so that the
	// ledger's tables stand alone.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()
	if err := Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	account := types.NewID()

	// Each on a day of its own, so that the cut-short rule settle falls
	// back on never finds one for another.
	stored := func(day, hash, external string, pending bool) string {
		id := types.NewID().String()
		if _, err := db.ExecContext(ctx, `
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, external_id, hash, occurrence, holder, pending, check_number)
VALUES (?, ?, ?, ?, 'CITY PARKING', -725, ?, ?, 1, '', ?, '')`,
			id, account.String(), types.NewID().String(), day, external, hash, pending); err != nil {
			t.Fatal(err)
		}

		return id
	}

	byHash := stored("2026-09-01", "h-hash", "", true)
	byExternal := stored("2026-09-02", "h-external", "FIT-1", true)
	byAlias := stored("2026-09-03", "h-alias", "", true)
	posted := stored("2026-09-04", "h-posted", "", false)

	if _, err := db.ExecContext(ctx, `INSERT INTO transaction_aliases (account_id, hash, transaction_id, created_at) VALUES (?, 'h-remembered', ?, 0)`,
		account.String(), byAlias); err != nil {
		t.Fatal(err)
	}

	pending := func(id string) bool {
		var p bool
		if err := db.QueryRowContext(ctx, `SELECT pending FROM transactions WHERE id = ?`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}

		return p
	}

	for _, c := range []struct {
		name, hash, external, id string
		posts                    int
	}{
		{"by its hash", "h-hash", "", byHash, 1},
		{"by the bank's identifier", "h-other", "FIT-1", byExternal, 1},
		{"by a remembered wording", "h-remembered", "", byAlias, 1},
		{"already posted", "h-posted", "", posted, 0},
	} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}

		day, _ := types.ParseDate("2026-10-01")
		row := ledgerbus.Transaction{AccountID: account, StatementID: types.NewID(), PostedOn: day,
			Description: "CITY PARKING", Amount: money.MustParse("-7.25"), Hash: c.hash, ExternalID: c.external}

		var st ledgerbus.Statement
		if err := settle(ctx, tx, &st, row); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		if st.Posted != c.posts || pending(c.id) {
			t.Errorf("%s: posted %d, still pending %v", c.name, st.Posted, pending(c.id))
		}
	}
}
