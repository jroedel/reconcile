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
	"errors"
	"fmt"
	"strings"
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
	"ui_strings":      {"context", "en", "first_seen", "last_seen", "pages"},
	"ui_translations": {"context", "en", "lang", "text", "status", "updated_by", "updated_at", "origin", "note"},
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

	// Later columns (docs/translations.md, "Storage"), beside the CREATE
	// rather than in it, so that a database made before them gets them too:
	//
	//   - ui_strings.pages: the template files a string is written in,
	//     space-separated, rewritten at every Register.
	//   - ui_translations.origin: who wrote the text, 'claude' or
	//     'person', or '' for a pending row and any written before.
	//   - ui_translations.note: a reviewer's reason for sending it back.
	for _, c := range []struct{ table, column, decl string }{
		{"ui_strings", "pages", "TEXT NOT NULL DEFAULT ''"},
		{"ui_translations", "origin", "TEXT NOT NULL DEFAULT ''"},
		{"ui_translations", "note", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := sqldb.AddColumn(ctx, db, c.table, c.column, c.decl); err != nil {
			return err
		}
	}

	return nil
}

// Register is one transaction, so that a startup that fails halfway leaves
// the strings as they were rather than half of them seen.
func (s *Store) Register(ctx context.Context, uses []translationbus.Use, langs []types.Lang, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to register strings: %w", err)
	}
	defer tx.Rollback()

	at := now.UnixMilli()

	for _, src := range uses {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO ui_strings (context, en, first_seen, last_seen, pages) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (context, en) DO UPDATE SET last_seen = excluded.last_seen, pages = excluded.pages`,
			src.Context, src.EN, at, at, strings.Join(src.Pages, " ")); err != nil {
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

// columns is a translation with its string's files, read by scan.
const columns = `t.context, t.en, t.lang, t.text, t.status, t.origin, t.note, t.updated_by, t.updated_at, s.pages
FROM ui_translations t JOIN ui_strings s ON s.context = t.context AND s.en = t.en`

func scan(row interface{ Scan(...any) error }) (translationbus.Translation, error) {
	var (
		t                       translationbus.Translation
		lang, status, origin, p string
		by                      sql.NullString
		at                      int64
	)

	if err := row.Scan(&t.Context, &t.EN, &lang, &t.Text, &status, &origin, &t.Note, &by, &at, &p); err != nil {
		return translationbus.Translation{}, err
	}

	t.Lang, t.Status, t.Origin = types.Lang(lang), translationbus.Status(status), translationbus.Origin(origin)
	t.UpdatedAt = time.UnixMilli(at)
	t.Pages = strings.Fields(p)

	if by.Valid {
		id, err := types.ParseID(by.String)
		if err != nil {
			return translationbus.Translation{}, fmt.Errorf("a translation names a bad user: %w", err)
		}

		t.UpdatedBy = id
	}

	return t, nil
}

func scanAll(rows *sql.Rows) ([]translationbus.Translation, error) {
	defer rows.Close()

	var out []translationbus.Translation

	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("reading a translation: %w", err)
		}

		out = append(out, t)
	}

	return out, rows.Err()
}

// waiting is what Claude is asked for: no text yet, or sent back.
const waiting = `(t.status = 'pending' OR t.note <> '') AND t.lang = ? AND s.last_seen >= ?`

// Pending is the sent-back first, then the rest, in the order of their
// English.
func (s *Store) Pending(ctx context.Context, lang types.Lang, since time.Time, limit int) ([]translationbus.Translation, int, error) {
	var total int

	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ui_translations t JOIN ui_strings s ON s.context = t.context AND s.en = t.en
WHERE `+waiting, string(lang), since.UnixMilli()).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting the translations waiting: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` WHERE `+waiting+`
ORDER BY t.note = '', t.en, t.context LIMIT ?`, string(lang), since.UnixMilli(), limit)
	if err != nil {
		return nil, 0, fmt.Errorf("reading the translations waiting: %w", err)
	}

	out, err := scanAll(rows)

	return out, total, err
}

// Get is one translation.
func (s *Store) Get(ctx context.Context, src translationbus.Source, lang types.Lang) (translationbus.Translation, error) {
	t, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` WHERE t.context = ? AND t.en = ? AND t.lang = ?`,
		src.Context, src.EN, string(lang)))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return translationbus.Translation{}, translationbus.ErrNotFound
	case err != nil:
		return translationbus.Translation{}, fmt.Errorf("reading a translation: %w", err)
	}

	return t, nil
}

// Put writes a translation and clears its note.
func (s *Store) Put(ctx context.Context, t translationbus.Translation) error {
	var by any
	if !t.UpdatedBy.Zero() {
		by = t.UpdatedBy.String()
	}

	res, err := s.db.ExecContext(ctx, `
UPDATE ui_translations SET text = ?, status = ?, origin = ?, note = '', updated_by = ?, updated_at = ?
WHERE context = ? AND en = ? AND lang = ?`,
		t.Text, string(t.Status), string(t.Origin), by, t.UpdatedAt.UnixMilli(), t.Context, t.EN, string(t.Lang))
	if err != nil {
		return fmt.Errorf("writing a translation: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, translationbus.ErrNotFound)
	}

	return nil
}

// List is a page of a language's translations.
func (s *Store) List(ctx context.Context, lang types.Lang, status translationbus.Status, limit, offset int) ([]translationbus.Translation, int, error) {
	where := `t.lang = ? AND (? = '' OR t.status = ?)`
	args := []any{string(lang), string(status), string(status)}

	var total int

	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ui_translations t WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting translations: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` WHERE `+where+` ORDER BY t.en, t.context LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing translations: %w", err)
	}

	out, err := scanAll(rows)

	return out, total, err
}
