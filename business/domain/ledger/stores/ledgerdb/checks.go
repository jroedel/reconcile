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

// SetPayee writes a transaction's payee and its history together.
func (s *Store) SetPayee(ctx context.Context, id types.ID, payee string, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to write a payee: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE transactions SET payee = ? WHERE id = ?`, payee, id.String())
	if err != nil {
		return fmt.Errorf("writing a payee: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmpErr(err, ledgerbus.ErrNotFound)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	return tx.Commit()
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
