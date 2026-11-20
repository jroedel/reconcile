package ledgerdb

import (
	"context"
	"fmt"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// WithCheck is the account's transactions with the check number, with
// their parts, oldest first.
func (s *Store) WithCheck(ctx context.Context, account types.ID, number string) ([]ledgerbus.Transaction, error) {
	return s.withSplits(ctx, `account_id = ? AND check_number = ?`, `posted_on, rowid`, account.String(), number)
}

// CheckNumbers is every check number the account's transactions have,
// each once.
func (s *Store) CheckNumbers(ctx context.Context, account types.ID) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT check_number FROM transactions WHERE account_id = ? AND check_number <> ''`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the account's check numbers: %w", err)
	}
	defer rows.Close()

	var out []string

	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("reading the account's check numbers: %w", err)
		}

		out = append(out, n)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the account's check numbers: %w", err)
	}

	return out, nil
}

// SetWritten writes a check's memo and the day it was written, and its
// history, together.
func (s *Store) SetWritten(ctx context.Context, id types.ID, memo string, on types.Date, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to write what a check says: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE transactions SET check_memo = ?, check_written_on = ? WHERE id = ?`, memo, on.String(), id.String())
	if err != nil {
		return fmt.Errorf("writing what a check says: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmpErr(err, ledgerbus.ErrNotFound)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	return tx.Commit()
}
