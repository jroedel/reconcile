// Package filedb stores the rows of uploaded files in SQLite.
package filedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of filebus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ filebus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"files": {"id", "sha256", "size", "content_type", "name", "uploaded_by", "uploaded_at"},
}

// Init creates the table. sha256 is indexed and not unique: a row is an
// upload, and the bytes are shared (filebus).
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS files (
    id           TEXT    PRIMARY KEY,
    sha256       TEXT    NOT NULL,
    size         INTEGER NOT NULL,
    content_type TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    uploaded_by  TEXT    NOT NULL,
    uploaded_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS files_sha256 ON files (sha256);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the files table: %w", err)
	}

	return nil
}

// Create inserts a row.
func (s *Store) Create(ctx context.Context, f filebus.File) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO files (id, sha256, size, content_type, name, uploaded_by, uploaded_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.ID.String(), f.SHA256, f.Size, f.ContentType, f.Name, f.UploadedBy.String(), f.UploadedAt.UnixMilli()); err != nil {
		return fmt.Errorf("inserting the file: %w", err)
	}

	return nil
}

// ByID finds one.
func (s *Store) ByID(ctx context.Context, id types.ID) (filebus.File, error) {
	var (
		f       filebus.File
		rid, by string
		at      int64
	)

	err := s.db.QueryRowContext(ctx, `
SELECT id, sha256, size, content_type, name, uploaded_by, uploaded_at FROM files WHERE id = ?`, id.String()).
		Scan(&rid, &f.SHA256, &f.Size, &f.ContentType, &f.Name, &by, &at)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return filebus.File{}, filebus.ErrNotFound
	case err != nil:
		return filebus.File{}, fmt.Errorf("reading the file: %w", err)
	}

	var e1, e2 error
	f.ID, e1 = types.ParseID(rid)
	f.UploadedBy, e2 = types.ParseID(by)

	if err := errors.Join(e1, e2); err != nil {
		return filebus.File{}, fmt.Errorf("a stored file is unreadable: %w", err)
	}

	f.UploadedAt = time.UnixMilli(at).UTC()

	return f, nil
}
