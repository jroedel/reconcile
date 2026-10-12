// Package reportdb stores saved reports and their notes in SQLite. Nothing
// a report adds up is stored here: only what a person said of it.
package reportdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of reportbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ reportbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"reports": {"id", "scope_kind", "scope_id", "style", "title", "description", "choices", "period",
		"created_by", "created_at", "updated_by", "updated_at", "removed_at"},
	"report_notes": {"report_id", "period_from", "period_to", "note", "figures", "updated_by", "updated_at"},
}

// Init creates the tables. After eventdb, whose history it writes.
//
// A report's scope is a kind and an ID rather than a reference to orgs or
// accounts, as the history's is: it belongs to an organization or an
// account on its own, and later a project, and one column per kind would
// be two NULLs on every row. A note's period is its first and last month as
// the address writes them, both ” for every month there is; the two with
// the report are its key, so that writing one twice is a correction.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS reports (
    id          TEXT    PRIMARY KEY,
    scope_kind  TEXT    NOT NULL,
    scope_id    TEXT    NOT NULL,
    style       TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    choices     TEXT    NOT NULL DEFAULT '{}',
    period      TEXT    NOT NULL DEFAULT '',
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_by  TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    removed_at  INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS reports_scope ON reports (scope_kind, scope_id);

CREATE TABLE IF NOT EXISTS report_notes (
    report_id   TEXT    NOT NULL REFERENCES reports (id),
    period_from TEXT    NOT NULL,
    period_to   TEXT    NOT NULL,
    note        TEXT    NOT NULL,
    figures     TEXT    NOT NULL DEFAULT '',
    updated_by  TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (report_id, period_from, period_to)
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the report tables: %w", err)
	}

	return nil
}

// inTx runs fn and writes ev in one transaction.
func (s *Store) inTx(ctx context.Context, ev eventbus.Event, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving the report: %w", err)
	}

	return nil
}

const columns = `id, scope_kind, scope_id, style, title, description, choices, period, created_by, created_at, updated_by, updated_at, removed_at`

func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}

	return t.UnixMilli()
}

// Create inserts a saved report.
func (s *Store) Create(ctx context.Context, r reportbus.Saved, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reports (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID.String(), string(r.Scope.Kind), r.Scope.ID.String(), r.Style, r.Title, r.Description, r.Choices, r.Period,
			r.CreatedBy.String(), r.CreatedAt.UnixMilli(), r.UpdatedBy.String(), r.UpdatedAt.UnixMilli(), nullMS(r.RemovedAt)); err != nil {
			return fmt.Errorf("saving the report: %w", err)
		}

		return nil
	})
}

// Update writes what can change about a saved report: not its scope or its
// style, which are what it is.
func (s *Store) Update(ctx context.Context, r reportbus.Saved, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
UPDATE reports SET title = ?, description = ?, choices = ?, period = ?, updated_by = ?, updated_at = ?, removed_at = ?
WHERE id = ?`,
			r.Title, r.Description, r.Choices, r.Period, r.UpdatedBy.String(), r.UpdatedAt.UnixMilli(), nullMS(r.RemovedAt), r.ID.String())
		if err != nil {
			return fmt.Errorf("saving the report: %w", err)
		}

		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return reportbus.ErrNotFound
		}

		return nil
	})
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (reportbus.Saved, error) {
	var (
		r                       reportbus.Saved
		id, kind, scope, cb, ub string
		made, updated           int64
		removed                 sql.NullInt64
	)

	if err := row.Scan(&id, &kind, &scope, &r.Style, &r.Title, &r.Description, &r.Choices, &r.Period, &cb, &made, &ub, &updated, &removed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return reportbus.Saved{}, reportbus.ErrNotFound
		}

		return reportbus.Saved{}, fmt.Errorf("reading a report: %w", err)
	}

	var e [5]error

	r.ID, e[0] = types.ParseID(id)
	r.Scope.ID, e[1] = types.ParseID(scope)
	r.Scope.Kind, e[2] = types.ParseScopeKind(kind)
	r.CreatedBy, e[3] = types.ParseID(cb)
	r.UpdatedBy, e[4] = types.ParseID(ub)

	if err := errors.Join(e[:]...); err != nil {
		return reportbus.Saved{}, fmt.Errorf("a stored report is unreadable: %w", err)
	}

	r.CreatedAt, r.UpdatedAt = time.UnixMilli(made).UTC(), time.UnixMilli(updated).UTC()
	if removed.Valid {
		r.RemovedAt = time.UnixMilli(removed.Int64).UTC()
	}

	return r, nil
}

// ByID is one saved report, removed or not.
func (s *Store) ByID(ctx context.Context, id types.ID) (reportbus.Saved, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM reports WHERE id = ?`, id.String()))
}

// Of is a scope's saved reports that are not removed, by title.
func (s *Store) Of(ctx context.Context, scope types.Scope) ([]reportbus.Saved, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM reports
WHERE scope_kind = ? AND scope_id = ? AND removed_at IS NULL ORDER BY title COLLATE NOCASE, id`, string(scope.Kind), scope.ID.String())
	if err != nil {
		return nil, fmt.Errorf("reading the reports: %w", err)
	}
	defer rows.Close()

	var out []reportbus.Saved

	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	return out, rows.Err()
}

// periodKey is a period as a note's key.
func periodKey(p reportbus.Period) (string, string) {
	if p.All {
		return "", ""
	}

	return p.From, p.To
}

// Note is a report's note for a period, if there is one.
func (s *Store) Note(ctx context.Context, reportID types.ID, p reportbus.Period) (reportbus.Note, bool, error) {
	from, to := periodKey(p)

	var (
		n  = reportbus.Note{ReportID: reportID, Period: p}
		by string
		at int64
	)

	err := s.db.QueryRowContext(ctx, `
SELECT note, figures, updated_by, updated_at FROM report_notes WHERE report_id = ? AND period_from = ? AND period_to = ?`,
		reportID.String(), from, to).Scan(&n.Text, &n.Figures, &by, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return reportbus.Note{}, false, nil
	}

	if err != nil {
		return reportbus.Note{}, false, fmt.Errorf("reading the report's note: %w", err)
	}

	if n.By, err = types.ParseID(by); err != nil {
		return reportbus.Note{}, false, fmt.Errorf("a stored note is unreadable: %w", err)
	}

	n.At = time.UnixMilli(at).UTC()

	return n, true, nil
}

// SetNote writes a note, over the period's note if it has one: one
// statement, so that two people saving at once leave one note, the
// later.
func (s *Store) SetNote(ctx context.Context, n reportbus.Note, ev eventbus.Event) error {
	from, to := periodKey(n.Period)

	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO report_notes (report_id, period_from, period_to, note, figures, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (report_id, period_from, period_to) DO UPDATE SET
    note = excluded.note, figures = excluded.figures, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			n.ReportID.String(), from, to, n.Text, n.Figures, n.By.String(), n.At.UnixMilli()); err != nil {
			return fmt.Errorf("saving the note: %w", err)
		}

		return nil
	})
}

// RemoveNote removes a period's note.
func (s *Store) RemoveNote(ctx context.Context, reportID types.ID, p reportbus.Period, ev eventbus.Event) error {
	from, to := periodKey(p)

	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM report_notes WHERE report_id = ? AND period_from = ? AND period_to = ?`,
			reportID.String(), from, to); err != nil {
			return fmt.Errorf("removing the note: %w", err)
		}

		return nil
	})
}
