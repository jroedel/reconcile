// Package translationdb stores the interface's strings and their
// translations.
//
// # Two tables
//
// ui_strings is one row per string the code has ever used, keyed by its
// context and its English. first_seen and last_seen are when a binary
// carrying it first and last started: a string whose last_seen falls behind
// is one the code no longer says, and a later screen can offer to tidy it
// away. Nothing deletes one automatically, because a rollback brings old
// strings back.
//
// ui_translations is one row per string per language other than English.
//
// No CHECK on lang. Adding a language is meant to be one line in
// business/types, and a CHECK listing today's languages would make it a table
// rebuild on every database that exists (CLAUDE.md, "When there is a
// database"). The business layer only ever writes a language it knows.
package translationdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of translationbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ translationbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"ui_strings":      {"context", "en", "first_seen", "last_seen"},
	"ui_translations": {"context", "en", "lang", "text", "status", "updated_by", "updated_at"},
}

// Init creates the tables. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS ui_strings (
    context     TEXT    NOT NULL,
    en          TEXT    NOT NULL,
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    PRIMARY KEY (context, en)
) STRICT;

CREATE TABLE IF NOT EXISTS ui_translations (
    context     TEXT    NOT NULL,
    en          TEXT    NOT NULL,
    lang        TEXT    NOT NULL,
    text        TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL CHECK (status IN ('pending', 'draft', 'approved')),

    -- The user who wrote or approved it; NULL for a pending row, and for one
    -- written through the API until it records who holds the key.
    updated_by  TEXT,
    updated_at  INTEGER NOT NULL,

    PRIMARY KEY (context, en, lang),
    FOREIGN KEY (context, en) REFERENCES ui_strings (context, en) ON DELETE CASCADE
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the translation tables: %w", err)
	}

	return nil
}

// Register is one transaction, so that a startup that fails halfway leaves
// the strings as they were rather than half of them seen.
func (s *Store) Register(ctx context.Context, sources []translationbus.Source, langs []types.Lang, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to register strings: %w", err)
	}
	defer tx.Rollback()

	at := now.UnixMilli()

	for _, src := range sources {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO ui_strings (context, en, first_seen, last_seen) VALUES (?, ?, ?, ?)
ON CONFLICT (context, en) DO UPDATE SET last_seen = excluded.last_seen`,
			src.Context, src.EN, at, at); err != nil {
			return fmt.Errorf("registering %q: %w", src.EN, err)
		}

		for _, lang := range langs {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO ui_translations (context, en, lang, status, updated_at) VALUES (?, ?, ?, 'pending', ?)
ON CONFLICT (context, en, lang) DO NOTHING`,
				src.Context, src.EN, string(lang), at); err != nil {
				return fmt.Errorf("opening a %s translation of %q: %w", lang, src.EN, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("registering strings: %w", err)
	}

	return nil
}

// Translated is every row with text.
func (s *Store) Translated(ctx context.Context) ([]translationbus.Translation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT context, en, lang, text, status, updated_at
FROM ui_translations
WHERE status <> 'pending' AND text <> ''`)
	if err != nil {
		return nil, fmt.Errorf("reading translations: %w", err)
	}
	defer rows.Close()

	var out []translationbus.Translation

	for rows.Next() {
		var (
			t         translationbus.Translation
			lang      string
			status    string
			updatedAt int64
		)

		if err := rows.Scan(&t.Context, &t.EN, &lang, &t.Text, &status, &updatedAt); err != nil {
			return nil, fmt.Errorf("reading a translation: %w", err)
		}

		t.Lang = types.Lang(lang)
		t.Status = translationbus.Status(status)
		t.UpdatedAt = time.UnixMilli(updatedAt)
		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading translations: %w", err)
	}

	return out, nil
}
