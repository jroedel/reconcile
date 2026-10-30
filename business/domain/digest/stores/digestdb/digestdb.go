// Package digestdb keeps what each person's last weekly email saw, in
// SQLite.
package digestdb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jroedel/reconcile/business/domain/digest/digestbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of digestbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ digestbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"digest_seen": {"user_id", "transaction_id", "list"},
}

// Init creates the table. After userdb and ledgerdb, which it references.
//
// A row is a transaction a person's last email saw in one of the
// accountant's two lists (digestbus, "What is new"). It goes with the
// person, and with the transaction when its statement is removed. The
// CHECK is the two lists, which change only with docs/accountant.md.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS digest_seen (
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    transaction_id TEXT NOT NULL REFERENCES transactions (id) ON DELETE CASCADE,
    list           TEXT NOT NULL CHECK (list IN ('needed', 'added')),
    PRIMARY KEY (user_id, transaction_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS digest_seen_transaction ON digest_seen (transaction_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the weekly email's memory: %w", err)
	}

	return nil
}

// Seen is the transactions in each list when the person's last email was
// sent.
func (s *Store) Seen(ctx context.Context, userID types.ID) (map[types.ID]ledgerbus.ForAccountant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT transaction_id, list FROM digest_seen WHERE user_id = ?`, userID.String())
	if err != nil {
		return nil, fmt.Errorf("reading what the last email saw: %w", err)
	}
	defer rows.Close()

	out := map[types.ID]ledgerbus.ForAccountant{}

	for rows.Next() {
		var raw, list string
		if err := rows.Scan(&raw, &list); err != nil {
			return nil, fmt.Errorf("reading what the last email saw: %w", err)
		}

		id, err := types.ParseID(raw)
		if err != nil {
			return nil, fmt.Errorf("a transaction is unreadable: %w", err)
		}

		out[id] = ledgerbus.ForAccountant(list)
	}

	return out, rows.Err()
}

// SetSeen replaces what the person's last email saw.
func (s *Store) SetSeen(ctx context.Context, userID types.ID, seen map[types.ID]ledgerbus.ForAccountant) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := replace(ctx, tx, userID, seen); err != nil {
		_ = tx.Rollback()

		return err
	}

	return tx.Commit()
}

func replace(ctx context.Context, tx *sql.Tx, userID types.ID, seen map[types.ID]ledgerbus.ForAccountant) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM digest_seen WHERE user_id = ?`, userID.String()); err != nil {
		return fmt.Errorf("forgetting what the last email saw: %w", err)
	}

	insert, err := tx.PrepareContext(ctx, `INSERT INTO digest_seen (user_id, transaction_id, list) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("keeping what the email saw: %w", err)
	}
	defer insert.Close()

	for id, list := range seen {
		if _, err := insert.ExecContext(ctx, userID.String(), id.String(), string(list)); err != nil {
			return fmt.Errorf("keeping what the email saw: %w", err)
		}
	}

	return nil
}
