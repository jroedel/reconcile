// Package shapedb stores sightings of statement layouts in SQLite.
package shapedb

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
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
}

// Init creates the tables. They reference nothing: a sighting belongs to
// no account, organization or person, which is the point of it.
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
