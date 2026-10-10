package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// The statement inbox (ledgerbus, inbox.go).

// initInbox creates the table of files waiting in a person's inbox. One
// line per person and content, so that the same statement sent twice is
// one line, whatever it was called and however many emails it came in;
// the unique key is what makes adding one a single statement.
func initInbox(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS statement_inbox (
    id            TEXT    PRIMARY KEY,
    user_id       TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    file_id       TEXT    NOT NULL REFERENCES files (id),
    sha256        TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    source        TEXT    NOT NULL DEFAULT '',
    received_at   INTEGER NOT NULL,
    closed_at     INTEGER,
    dismissed_at  INTEGER,
    UNIQUE (user_id, sha256)
) STRICT;

CREATE INDEX IF NOT EXISTS statement_inbox_waiting ON statement_inbox (user_id, received_at)
    WHERE closed_at IS NULL AND dismissed_at IS NULL;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the statement inbox: %w", err)
	}

	return nil
}

const inboxColumns = `id, user_id, file_id, sha256, name, source, received_at, closed_at, dismissed_at`

// AddToInbox records a file in a person's inbox unless that content is in
// it already, and says whether it did.
func (s *Store) AddToInbox(ctx context.Context, f ledgerbus.InboxFile) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO statement_inbox (id, user_id, file_id, sha256, name, source, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (user_id, sha256) DO NOTHING`,
		f.ID.String(), f.UserID.String(), f.FileID.String(), f.SHA256, f.Name, f.Source, f.ReceivedAt.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("adding to the statement inbox: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("adding to the statement inbox: %w", err)
	}

	return n == 1, nil
}

// InboxFileBySHA is the person's line for that content, in whatever state.
func (s *Store) InboxFileBySHA(ctx context.Context, userID types.ID, sha string) (ledgerbus.InboxFile, error) {
	f, err := scanInboxFile(s.db.QueryRowContext(ctx,
		`SELECT `+inboxColumns+` FROM statement_inbox WHERE user_id = ? AND sha256 = ?`, userID.String(), sha))
	if errors.Is(err, sql.ErrNoRows) {
		return ledgerbus.InboxFile{}, ledgerbus.ErrNotFound
	}

	return f, err
}

// Inbox is the person's waiting files, oldest first.
func (s *Store) Inbox(ctx context.Context, userID types.ID) ([]ledgerbus.InboxFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+inboxColumns+` FROM statement_inbox
WHERE user_id = ? AND closed_at IS NULL AND dismissed_at IS NULL
ORDER BY received_at, rowid`, userID.String())
	if err != nil {
		return nil, fmt.Errorf("reading the statement inbox: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.InboxFile

	for rows.Next() {
		f, err := scanInboxFile(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the statement inbox: %w", err)
	}

	return out, nil
}

// CloseInboxFiles closes the person's waiting lines for those files; a file
// that is not in their inbox, or not waiting, is left as it is.
func (s *Store) CloseInboxFiles(ctx context.Context, userID types.ID, fileIDs []types.ID, at time.Time) error {
	if len(fileIDs) == 0 {
		return nil
	}

	args := []any{at.UnixMilli(), userID.String()}
	for _, id := range fileIDs {
		args = append(args, id.String())
	}

	if _, err := s.db.ExecContext(ctx, `
UPDATE statement_inbox SET closed_at = ?
WHERE user_id = ? AND closed_at IS NULL AND dismissed_at IS NULL
  AND file_id IN (?`+strings.Repeat(", ?", len(fileIDs)-1)+`)`, args...); err != nil {
		return fmt.Errorf("closing statements in the inbox: %w", err)
	}

	return nil
}

// DismissInboxFile takes one of the person's waiting lines off the list.
func (s *Store) DismissInboxFile(ctx context.Context, userID, id types.ID, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE statement_inbox SET dismissed_at = ?
WHERE id = ? AND user_id = ? AND closed_at IS NULL AND dismissed_at IS NULL`,
		at.UnixMilli(), id.String(), userID.String())
	if err != nil {
		return fmt.Errorf("dismissing a statement in the inbox: %w", err)
	}

	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("dismissing a statement in the inbox: %w", err)
	case n == 0:
		return ledgerbus.ErrNotFound
	}

	return nil
}

func scanInboxFile(row interface{ Scan(...any) error }) (ledgerbus.InboxFile, error) {
	var (
		f                 ledgerbus.InboxFile
		id, user, file    string
		received          int64
		closed, dismissed sql.NullInt64
	)

	if err := row.Scan(&id, &user, &file, &f.SHA256, &f.Name, &f.Source, &received, &closed, &dismissed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ledgerbus.InboxFile{}, err
		}

		return ledgerbus.InboxFile{}, fmt.Errorf("reading the statement inbox: %w", err)
	}

	var err error
	if f.ID, err = types.ParseID(id); err != nil {
		return ledgerbus.InboxFile{}, fmt.Errorf("an inbox line has a bad identifier: %w", err)
	}

	if f.UserID, err = types.ParseID(user); err != nil {
		return ledgerbus.InboxFile{}, fmt.Errorf("an inbox line names a bad person: %w", err)
	}

	if f.FileID, err = types.ParseID(file); err != nil {
		return ledgerbus.InboxFile{}, fmt.Errorf("an inbox line names a bad file: %w", err)
	}

	f.ReceivedAt = time.UnixMilli(received).UTC()

	if closed.Valid {
		f.ClosedAt = time.UnixMilli(closed.Int64).UTC()
	}

	if dismissed.Valid {
		f.DismissedAt = time.UnixMilli(dismissed.Int64).UTC()
	}

	return f, nil
}
