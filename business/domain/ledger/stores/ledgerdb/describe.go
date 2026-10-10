package ledgerdb

import (
	"context"
	"fmt"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// SetOwnDescription writes a transaction's own description and the key it
// came through, and ev with them when there is one: a person who saves
// the same words on the web takes the key's mark off and changes nothing
// the history needs to hear about.
func (s *Store) SetOwnDescription(ctx context.Context, id types.ID, description, via string, ev *eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to write a description: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE transactions SET own_description = ?, own_description_via = ? WHERE id = ?`, description, via, id.String())
	if err != nil {
		return fmt.Errorf("writing a description: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmpErr(err, ledgerbus.ErrNotFound)
	}

	if ev != nil {
		if err := eventdb.Insert(ctx, tx, *ev); err != nil {
			return err
		}
	}

	return tx.Commit()
}
