// Package ruledb stores sorting rules in SQLite.
package ruledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of rulebus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ rulebus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"sort_rules": {"id", "account_id", "match", "match_key", "direction", "category_id", "project_id", "created_by", "created_at", "updated_at"},
}

// Init creates the table. After tenancydb and categorydb, which it
// references.
//
// match_key is the text as compared (rulebus.Key), unique per account, so
// that the same rule made twice at once is one rule and a refusal, not two
// rules of which the first silently wins forever. The direction's CHECK is
// safe to have: it is the three ways money can move, not a list that grows.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS sort_rules (
    id          TEXT    PRIMARY KEY,
    account_id  TEXT    NOT NULL REFERENCES accounts (id),
    match       TEXT    NOT NULL,
    match_key   TEXT    NOT NULL,
    direction   TEXT    NOT NULL CHECK (direction IN ('out', 'in', 'any')),
    category_id TEXT    REFERENCES categories (id),
    project_id  TEXT    REFERENCES projects (id),
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK (category_id IS NOT NULL OR project_id IS NOT NULL)
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS sort_rules_match ON sort_rules (account_id, match_key);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the sorting rules table: %w", err)
	}

	return nil
}

func nullID(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}

// Create inserts a rule and its line of history.
func (s *Store) Create(ctx context.Context, r rulebus.Rule, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO sort_rules (id, account_id, match, match_key, direction, category_id, project_id, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID.String(), r.AccountID.String(), r.Match, rulebus.Key(r.Match), string(r.Direction),
			nullID(r.CategoryID), nullID(r.ProjectID), r.CreatedBy.String(), r.CreatedAt.UnixMilli(), r.UpdatedAt.UnixMilli())

		return err
	})
}

// Update rewrites a rule.
func (s *Store) Update(ctx context.Context, r rulebus.Rule, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
UPDATE sort_rules SET match = ?, match_key = ?, direction = ?, category_id = ?, project_id = ?, updated_at = ? WHERE id = ?`,
			r.Match, rulebus.Key(r.Match), string(r.Direction), nullID(r.CategoryID), nullID(r.ProjectID), r.UpdatedAt.UnixMilli(), r.ID.String())

		return err
	})
}

// Delete removes a rule.
func (s *Store) Delete(ctx context.Context, id types.ID, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sort_rules WHERE id = ?`, id.String())

		return err
	})
}

func (s *Store) inTx(ctx context.Context, ev eventbus.Event, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		if sqldb.IsUniqueViolation(err) {
			return rulebus.ErrDuplicate
		}

		return fmt.Errorf("changing a rule: %w", err)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving a change: %w", err)
	}

	return nil
}

const columns = `id, account_id, match, direction, category_id, project_id, created_by, created_at, updated_at`

// ByID finds one.
func (s *Store) ByID(ctx context.Context, id types.ID) (rulebus.Rule, error) {
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM sort_rules WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return rulebus.Rule{}, rulebus.ErrNotFound
	}

	return r, err
}

// Of is an account's rules, by their text.
func (s *Store) Of(ctx context.Context, accountID types.ID) ([]rulebus.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM sort_rules WHERE account_id = ? ORDER BY match_key`, accountID.String())
	if err != nil {
		return nil, fmt.Errorf("reading the rules: %w", err)
	}
	defer rows.Close()

	var out []rulebus.Rule

	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (rulebus.Rule, error) {
	var (
		r                    rulebus.Rule
		id, account, dir, by string
		category, project    sql.NullString
		created, updated     int64
	)

	if err := row.Scan(&id, &account, &r.Match, &dir, &category, &project, &by, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, err
		}

		return r, fmt.Errorf("reading a rule: %w", err)
	}

	var e [5]error
	r.ID, e[0] = types.ParseID(id)
	r.AccountID, e[1] = types.ParseID(account)
	r.CreatedBy, e[2] = types.ParseID(by)

	if category.Valid {
		r.CategoryID, e[3] = types.ParseID(category.String)
	}

	if project.Valid {
		r.ProjectID, e[4] = types.ParseID(project.String)
	}

	if err := errors.Join(e[:]...); err != nil {
		return r, fmt.Errorf("a stored rule is unreadable: %w", err)
	}

	r.Direction = rulebus.Direction(dir)
	r.CreatedAt, r.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()

	return r, nil
}
