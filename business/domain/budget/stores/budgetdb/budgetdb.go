// Package budgetdb stores budgets in SQLite.
package budgetdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of budgetbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ budgetbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"budget_lines": {"id", "project_id", "org_id", "year", "category_id", "kind", "amount", "currency", "updated_by", "updated_at"},
}

// Init creates the table. After tenancydb and categorydb, which it
// references.
//
// A line belongs to a project, or to an organization's budget year, and
// never both (docs/budgets.md). The unique index is over the columns with
// their NULLs made values, because SQLite counts two NULLs as different
// and a plain unique index would let a project have two "total expenses"
// lines. The kind's CHECK is safe to have: income and expenses are the
// two kinds that are operations, and a budget has nothing to say about
// the others.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS budget_lines (
    id          TEXT    PRIMARY KEY,
    project_id  TEXT    REFERENCES projects (id),
    org_id      TEXT    REFERENCES orgs (id),
    year        INTEGER,
    category_id TEXT    REFERENCES categories (id),
    kind        TEXT    NOT NULL CHECK (kind IN ('income', 'expense')),
    amount      INTEGER NOT NULL CHECK (amount > 0),
    currency    TEXT    NOT NULL,
    updated_by  TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK ((project_id IS NOT NULL AND org_id IS NULL AND year IS NULL)
        OR (project_id IS NULL AND org_id IS NOT NULL AND year IS NOT NULL))
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS budget_lines_one ON budget_lines
    (coalesce(project_id, ''), coalesce(org_id, ''), coalesce(year, 0), coalesce(category_id, ''), kind);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the budgets table: %w", err)
	}

	return nil
}

// where is the condition for a budget's lines, and its arguments.
func where(scope types.Scope, year int) (string, []any, error) {
	switch scope.Kind {
	case types.ScopeProject:
		return `project_id = ?`, []any{scope.ID.String()}, nil
	case types.ScopeOrg:
		return `org_id = ? AND year = ?`, []any{scope.ID.String(), year}, nil
	}

	return "", nil, fmt.Errorf("a %s has no budget", scope.Kind)
}

const columns = `id, project_id, org_id, year, category_id, kind, amount, currency, updated_by, updated_at`

// Lines is a budget's lines: a project's, or an organization's year's.
func (s *Store) Lines(ctx context.Context, scope types.Scope, year int) ([]budgetbus.Line, error) {
	cond, args, err := where(scope, year)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM budget_lines WHERE `+cond+` ORDER BY kind, category_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the budget: %w", err)
	}
	defer rows.Close()

	var out []budgetbus.Line

	for rows.Next() {
		var (
			l                      budgetbus.Line
			id, by, kind           string
			project, org, category sql.NullString
			y                      sql.NullInt64
			amount, at             int64
		)

		if err := rows.Scan(&id, &project, &org, &y, &category, &kind, &amount, &l.Currency, &by, &at); err != nil {
			return nil, fmt.Errorf("reading the budget: %w", err)
		}

		var e [5]error

		l.ID, e[0] = types.ParseID(id)
		l.UpdatedBy, e[1] = types.ParseID(by)

		switch {
		case project.Valid:
			var p types.ID
			p, e[2] = types.ParseID(project.String)
			l.Scope = types.ProjectScope(p)
		case org.Valid:
			var o types.ID
			o, e[3] = types.ParseID(org.String)
			l.Scope, l.Year = types.OrgScope(o), int(y.Int64)
		}

		if category.Valid {
			l.CategoryID, e[4] = types.ParseID(category.String)
		}

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a stored budget line is unreadable: %w", err)
		}

		l.Kind, l.Amount, l.UpdatedAt = categorybus.Kind(kind), money.Amount(amount), time.UnixMilli(at).UTC()

		out = append(out, l)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the budget: %w", err)
	}

	return out, nil
}

func nullID(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}

// Replace makes a budget's lines exactly these, with its lines of history,
// in one transaction: a budget is saved whole from one form, and half of
// one saved is a budget nobody set.
func (s *Store) Replace(ctx context.Context, scope types.Scope, year int, lines []budgetbus.Line, events []eventbus.Event) error {
	cond, args, err := where(scope, year)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM budget_lines WHERE `+cond, args...); err != nil {
		return fmt.Errorf("saving the budget: %w", err)
	}

	for _, l := range lines {
		var project, org, y any

		switch l.Scope.Kind {
		case types.ScopeProject:
			project = l.Scope.ID.String()
		case types.ScopeOrg:
			org, y = l.Scope.ID.String(), l.Year
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO budget_lines (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			l.ID.String(), project, org, y, nullID(l.CategoryID), string(l.Kind), int64(l.Amount), l.Currency,
			l.UpdatedBy.String(), l.UpdatedAt.UnixMilli()); err != nil {
			return fmt.Errorf("saving the budget: %w", err)
		}
	}

	for _, e := range events {
		if err := eventdb.Insert(ctx, tx, e); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving the budget: %w", err)
	}

	return nil
}
