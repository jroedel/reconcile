// Package shapedb stores sightings of statement layouts in SQLite.
package shapedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of shapebus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ shapebus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"shape_sightings": {"signature", "format", "producer", "frame", "sections", "first_at", "last_at"},
	"shape_files":     {"signature", "file_sha256", "balanced"},
	"shape_drafts":    {"id", "text", "created_by", "created_at", "updated_at"},
}

// Init creates the tables. They reference nothing: a sighting belongs to
// no account, organization or person, which is the point of it. A draft
// says who started it, but not as a reference to users: it is the site's,
// not that person's, and outlives their leaving.
//
// The words are kept one to a line. importbus.Word has already collapsed
// every run of spaces, so a word never holds a newline; JSON would say the
// same thing with more to go wrong.
//
// shape_files counts the files of a layout by their content hash, so that
// the same statement previewed five times, or sent by two people, is one
// file. The hash says nothing of what is in the file.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS shape_sightings (
    signature TEXT    PRIMARY KEY,
    format    TEXT    NOT NULL,
    producer  TEXT    NOT NULL,
    frame     TEXT    NOT NULL,
    sections  TEXT    NOT NULL,
    first_at  INTEGER NOT NULL,
    last_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS shape_files (
    signature   TEXT    NOT NULL REFERENCES shape_sightings (signature),
    file_sha256 TEXT    NOT NULL,
    balanced    INTEGER NOT NULL,
    PRIMARY KEY (signature, file_sha256)
) STRICT;

CREATE TABLE IF NOT EXISTS shape_drafts (
    id         TEXT    PRIMARY KEY,
    text       TEXT    NOT NULL,
    created_by TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the sightings tables: %w", err)
	}

	return nil
}

func words(s string) []string {
	if s == "" {
		return nil
	}

	return strings.Split(s, "\n")
}

// Seen records one file of a layout. The sections are joined with those
// already kept, which takes a read between two writes, so it is all one
// transaction -- and the first statement is a write, so that the
// transaction holds the database's one writer's lock before it reads: a
// read first would fail outright, not wait, if another file's sighting
// were written in between (SQLite's WAL). The file's row only ever turns
// balanced on.
func (s *Store) Seen(ctx context.Context, sg shapebus.Sighting, file string, balanced bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO shape_sightings (signature, format, producer, frame, sections, first_at, last_at)
VALUES (?, ?, ?, ?, '', ?, ?)
ON CONFLICT (signature) DO UPDATE SET last_at = MAX(last_at, excluded.last_at)`,
		sg.Signature, sg.Format, sg.Producer, strings.Join(sg.Frame, "\n"), sg.First.UnixMilli(), sg.Last.UnixMilli()); err != nil {
		return fmt.Errorf("recording a sighting: %w", err)
	}

	var kept string
	if err := tx.QueryRowContext(ctx, `SELECT sections FROM shape_sightings WHERE signature = ?`, sg.Signature).Scan(&kept); err != nil {
		return fmt.Errorf("reading a sighting: %w", err)
	}

	sections := words(kept)
	for _, w := range sg.Sections {
		if !slices.Contains(sections, w) {
			sections = append(sections, w)
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE shape_sightings SET sections = ? WHERE signature = ?`,
		strings.Join(sections, "\n"), sg.Signature); err != nil {
		return fmt.Errorf("recording a sighting's sections: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO shape_files (signature, file_sha256, balanced) VALUES (?, ?, ?)
ON CONFLICT (signature, file_sha256) DO UPDATE SET balanced = MAX(balanced, excluded.balanced)`,
		sg.Signature, file, balanced); err != nil {
		return fmt.Errorf("recording a sighting's file: %w", err)
	}

	return tx.Commit()
}

// Sightings is every layout seen, the most files first, then the most
// recently seen.
func (s *Store) Sightings(ctx context.Context) ([]shapebus.Sighting, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.signature, s.format, s.producer, s.frame, s.sections, s.first_at, s.last_at,
       COUNT(f.file_sha256), COALESCE(SUM(f.balanced), 0)
FROM shape_sightings s LEFT JOIN shape_files f ON f.signature = s.signature
GROUP BY s.signature
ORDER BY COUNT(f.file_sha256) DESC, s.last_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing sightings: %w", err)
	}

	defer rows.Close()

	var out []shapebus.Sighting

	for rows.Next() {
		var (
			sg              shapebus.Sighting
			frame, sections string
			first, last     int64
		)

		if err := rows.Scan(&sg.Signature, &sg.Format, &sg.Producer, &frame, &sections, &first, &last, &sg.Files, &sg.Balanced); err != nil {
			return nil, fmt.Errorf("reading a sighting: %w", err)
		}

		sg.Frame, sg.Sections = words(frame), words(sections)
		sg.First, sg.Last = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()

		out = append(out, sg)
	}

	return out, rows.Err()
}

// CreateDraft keeps a new draft.
func (s *Store) CreateDraft(ctx context.Context, d shapebus.Draft) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO shape_drafts (id, text, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		d.ID.String(), d.Text, d.CreatedBy.String(), d.CreatedAt.UnixMilli(), d.UpdatedAt.UnixMilli()); err != nil {
		return fmt.Errorf("keeping a draft: %w", err)
	}

	return nil
}

// UpdateDraft replaces a draft's text.
func (s *Store) UpdateDraft(ctx context.Context, d shapebus.Draft) error {
	res, err := s.db.ExecContext(ctx, `UPDATE shape_drafts SET text = ?, updated_at = ? WHERE id = ?`,
		d.Text, d.UpdatedAt.UnixMilli(), d.ID.String())
	if err != nil {
		return fmt.Errorf("saving a draft: %w", err)
	}

	return changedOne(res)
}

// RemoveDraft deletes one.
func (s *Store) RemoveDraft(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM shape_drafts WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("removing a draft: %w", err)
	}

	return changedOne(res)
}

func changedOne(res sql.Result) error {
	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return err
	case n == 0:
		return shapebus.ErrNotFound
	}

	return nil
}

const draftColumns = `id, text, created_by, created_at, updated_at`

// DraftByID is one draft.
func (s *Store) DraftByID(ctx context.Context, id types.ID) (shapebus.Draft, error) {
	d, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM shape_drafts WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return shapebus.Draft{}, shapebus.ErrNotFound
	}

	return d, err
}

// Drafts is every draft, the most recently changed first.
func (s *Store) Drafts(ctx context.Context) ([]shapebus.Draft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+draftColumns+` FROM shape_drafts ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("listing drafts: %w", err)
	}

	defer rows.Close()

	var out []shapebus.Draft

	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, d)
	}

	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDraft(row scanner) (shapebus.Draft, error) {
	var (
		d                shapebus.Draft
		id, by           string
		created, updated int64
	)

	if err := row.Scan(&id, &d.Text, &by, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return d, err
		}

		return d, fmt.Errorf("reading a draft: %w", err)
	}

	var e [2]error
	d.ID, e[0] = types.ParseID(id)
	d.CreatedBy, e[1] = types.ParseID(by)

	if err := errors.Join(e[:]...); err != nil {
		return d, fmt.Errorf("reading a draft: %w", err)
	}

	d.CreatedAt, d.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()

	return d, nil
}
