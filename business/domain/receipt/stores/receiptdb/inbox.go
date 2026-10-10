package receiptdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
)

// A person's own check inbox (receiptbus, inbox.go).

// initInbox creates the table of check images waiting in a person's own
// inbox. A table of its own rather than a third kind of receipt home: a
// receipt's home is an account or a project, held by a CHECK that SQLite
// cannot change without rebuilding the table, and every page that names
// a receipt's inbox would learn a kind that nobody but its uploader may
// see. An item here is a file and whose it is; filed, it becomes a
// receipt in an account, and receipt_id says which.
//
// No unique key on the content, as the statement inbox has: a check
// removed by mistake may be shared again as well as brought back, which
// receiptbus asks about first (InboxHolds), as it does for an inbox's
// receipts.
func initInbox(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS check_inbox (
    id         TEXT    PRIMARY KEY,
    user_id    TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    file_id    TEXT    NOT NULL REFERENCES files (id),
    added_at   INTEGER NOT NULL,
    removed_at INTEGER,
    filed_at   INTEGER,
    receipt_id TEXT    REFERENCES receipts (id)
) STRICT;

CREATE INDEX IF NOT EXISTS check_inbox_user ON check_inbox (user_id, added_at);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the check inbox: %w", err)
	}

	return nil
}

// AddInboxChecks stores new items of a person's check inbox.
func (s *Store) AddInboxChecks(ctx context.Context, checks []receiptbus.InboxCheck) error {
	return s.inTx(ctx, nil, func(tx *sql.Tx) error {
		for _, c := range checks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO check_inbox (id, user_id, file_id, added_at) VALUES (?, ?, ?, ?)`,
				c.ID.String(), c.UserID.String(), c.File.ID.String(), c.AddedAt.UnixMilli()); err != nil {
				return fmt.Errorf("adding to the check inbox: %w", err)
			}
		}

		return nil
	})
}

// InboxChecks is everything in a person's check inbox, newest first:
// waiting, filed and removed.
func (s *Store) InboxChecks(ctx context.Context, user types.ID) ([]receiptbus.InboxCheck, error) {
	return s.inboxChecks(ctx, `c.user_id = ?`, user.String())
}

// InboxCheckByID finds one item of somebody's check inbox.
func (s *Store) InboxCheckByID(ctx context.Context, id types.ID) (receiptbus.InboxCheck, error) {
	cs, err := s.inboxChecks(ctx, `c.id = ?`, id.String())
	if err != nil {
		return receiptbus.InboxCheck{}, err
	}

	if len(cs) == 0 {
		return receiptbus.InboxCheck{}, receiptbus.ErrNotFound
	}

	return cs[0], nil
}

// InboxHolds reports whether an item of the person's check inbox, not
// removed, has a file with these bytes; one filed counts, since its image
// is then in an account.
func (s *Store) InboxHolds(ctx context.Context, user types.ID, sha256 string) (bool, error) {
	var one int

	err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM check_inbox c JOIN files f ON f.id = c.file_id
WHERE c.user_id = ? AND c.removed_at IS NULL AND f.sha256 = ?
LIMIT 1`, user.String(), sha256).Scan(&one)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("looking for a check's bytes: %w", err)
	}

	return true, nil
}

// SetInboxCheckRemoved marks an item that is not filed removed, at a
// time, or not removed, for a zero one.
func (s *Store) SetInboxCheckRemoved(ctx context.Context, id types.ID, at time.Time) error {
	var removed any
	if !at.IsZero() {
		removed = at.UnixMilli()
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE check_inbox SET removed_at = ? WHERE id = ? AND filed_at IS NULL`, removed, id.String()); err != nil {
		return fmt.Errorf("changing an item of the check inbox: %w", err)
	}

	return nil
}

// FileInboxCheck makes an item of a person's check inbox the receipt r,
// with its history, in one transaction. Claiming the item is one
// statement that only a waiting item satisfies, so that two filings of
// one check -- a person's and their Claude's at the same moment -- make
// one receipt, and the second is ErrFiled.
func (s *Store) FileInboxCheck(ctx context.Context, id types.ID, r receiptbus.Receipt, ev eventbus.Event) error {
	return s.inTx(ctx, &ev, func(tx *sql.Tx) error {
		// The receipt first: the item's receipt_id refers to it.
		if err := insert(ctx, tx, r); err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `
UPDATE check_inbox SET filed_at = ?, receipt_id = ? WHERE id = ? AND filed_at IS NULL AND removed_at IS NULL`,
			r.CreatedAt.UnixMilli(), r.ID.String(), id.String())
		if err != nil {
			return fmt.Errorf("filing a check from the check inbox: %w", err)
		}

		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("filing a check from the check inbox: %w", err)
		} else if n == 0 {
			return receiptbus.ErrFiled
		}

		return nil
	})
}

func (s *Store) inboxChecks(ctx context.Context, where string, args ...any) ([]receiptbus.InboxCheck, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.user_id, c.added_at, c.removed_at, c.filed_at, c.receipt_id,
       f.id, f.sha256, f.size, f.content_type, f.name, f.uploaded_by, f.uploaded_at
FROM check_inbox c JOIN files f ON f.id = c.file_id
WHERE `+where+` ORDER BY c.added_at DESC, c.rowid DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the check inbox: %w", err)
	}
	defer rows.Close()

	var out []receiptbus.InboxCheck

	for rows.Next() {
		var (
			c                 receiptbus.InboxCheck
			id, user, fid, by string
			added, uploaded   int64
			removed, filed    sql.NullInt64
			receipt           sql.NullString
		)

		if err := rows.Scan(&id, &user, &added, &removed, &filed, &receipt,
			&fid, &c.File.SHA256, &c.File.Size, &c.File.ContentType, &c.File.Name, &by, &uploaded); err != nil {
			return nil, fmt.Errorf("reading the check inbox: %w", err)
		}

		var e [5]error
		c.ID, e[0] = types.ParseID(id)
		c.UserID, e[1] = types.ParseID(user)
		c.File.ID, e[2] = types.ParseID(fid)
		c.File.UploadedBy, e[3] = types.ParseID(by)

		if receipt.Valid {
			c.ReceiptID, e[4] = types.ParseID(receipt.String)
		}

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("an item of the check inbox is unreadable: %w", err)
		}

		c.AddedAt = time.UnixMilli(added).UTC()
		c.File.UploadedAt = time.UnixMilli(uploaded).UTC()

		if removed.Valid {
			c.RemovedAt = time.UnixMilli(removed.Int64).UTC()
		}

		if filed.Valid {
			c.FiledAt = time.UnixMilli(filed.Int64).UTC()
		}

		out = append(out, c)
	}

	return out, rows.Err()
}
