package ledgerdb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The preview's second pass refuses a file when a rule of the import is
// not stable: here, a second pass that words its rows otherwise, which is
// what a hash depending on something it should not would look like.
func TestTheSecondPassCatchesAnUnstableImport(t *testing.T) {
	// Without the foreign-key pragma, so that the ledger's tables stand
	// alone, without the accounts and files they point at.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	if err := Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	s := NewStore(db)
	account := types.NewID()
	day, _ := types.ParseDate("2026-09-02")

	st := ledgerbus.Statement{ID: types.NewID(), AccountID: account, FileID: types.NewID(), Format: ledgerbus.CSV,
		Start: day, End: day, Checked: ledgerbus.Unchecked, ImportedBy: types.NewID(), ImportedAt: time.Now()}

	tx := ledgerbus.Transaction{ID: types.NewID(), AccountID: account, StatementID: st.ID, PostedOn: day,
		Description: "SQ *COFFEE CART", Amount: money.MustParse("-3.50"), Hash: "h1", Occurrence: 1}
	tx.Splits = []ledgerbus.Split{{ID: types.NewID(), TransactionID: tx.ID, Amount: tx.Amount}}

	if _, err := s.Import(t.Context(), st, []ledgerbus.Transaction{tx}, nil, nil, eventbus.Event{}, false); err != nil {
		t.Fatalf("a stable import: %v", err)
	}

	stable := secondPass
	t.Cleanup(func() { secondPass = stable })

	secondPass = func(st ledgerbus.Statement, txs []ledgerbus.Transaction) (ledgerbus.Statement, []ledgerbus.Transaction) {
		st, txs = stable(st, txs)
		for i := range txs {
			txs[i].Hash += "-unstable"
		}

		return st, txs
	}

	if _, err := s.Import(t.Context(), st, []ledgerbus.Transaction{tx}, nil, nil, eventbus.Event{}, false); !errors.Is(err, ledgerbus.ErrUnstable) {
		t.Errorf("an unstable import: %v, want ErrUnstable", err)
	}
}
