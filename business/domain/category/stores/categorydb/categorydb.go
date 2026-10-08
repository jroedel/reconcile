// Package categorydb stores category lists in SQLite.
package categorydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of categorybus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ categorybus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"categories": {"id", "org_id", "account_id", "name", "created_by", "created_at", "archived_at"},
}

// Init creates the table. After tenancydb, which it references.
//
// Two owner columns rather than a kind and an ID, so that each is a foreign
// key; exactly one is set. The name is unique within a list whatever its
// case and whether or not it is archived -- the unique index decides, not a
// read beforehand -- so that "Utilities" and "utilities" are never two
// lines in the accountant's package.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS categories (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    account_id  TEXT    REFERENCES accounts (id),
    name        TEXT    NOT NULL,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER,
    CHECK ((org_id IS NULL) <> (account_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS categories_name ON categories (coalesce(org_id, account_id), name COLLATE NOCASE);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the categories table: %w", err)
	}

	return nil
}

func owner(s types.Scope) (org, account any) {
	if s.Kind == types.ScopeOrg {
		return s.ID.String(), nil
	}

	return nil, s.ID.String()
}

// Create inserts a category and its line of history.
func (s *Store) Create(ctx context.Context, c categorybus.Category, ev eventbus.Event) error {
	org, account := owner(c.Owner)

	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO categories (id, org_id, account_id, name, created_by, created_at, archived_at) VALUES (?, ?, ?, ?, ?, ?, NULL)`,
			c.ID.String(), org, account, c.Name, c.CreatedBy.String(), c.CreatedAt.UnixMilli())

		return err
	})
}

// Update writes a category's name and archived time.
func (s *Store) Update(ctx context.Context, c categorybus.Category, ev eventbus.Event) error {
	var archived any
	if c.Archived() {
		archived = c.ArchivedAt.UnixMilli()
	}

	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE categories SET name = ?, archived_at = ? WHERE id = ?`, c.Name, archived, c.ID.String())

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
			return categorybus.ErrDuplicate
		}

		return fmt.Errorf("changing a category: %w", err)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving a change: %w", err)
	}

	return nil
}

const columns = `id, org_id, account_id, name, created_by, created_at, archived_at`

// ByID finds one.
func (s *Store) ByID(ctx context.Context, id types.ID) (categorybus.Category, error) {
	c, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM categories WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return categorybus.Category{}, categorybus.ErrNotFound
	}

	return c, err
}

// Of is one owner's list, by name, archived ones last.
func (s *Store) Of(ctx context.Context, o types.Scope) ([]categorybus.Category, error) {
	column := "account_id"
	if o.Kind == types.ScopeOrg {
		column = "org_id"
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM categories WHERE `+column+` = ?
ORDER BY archived_at IS NOT NULL, name COLLATE NOCASE`, o.ID.String())
	if err != nil {
		return nil, fmt.Errorf("reading the categories: %w", err)
	}
	defer rows.Close()

	var out []categorybus.Category

	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, c)
	}

	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (categorybus.Category, error) {
	var (
		c            categorybus.Category
		id, by       string
		org, account sql.NullString
		at           int64
		archived     sql.NullInt64
	)

	if err := row.Scan(&id, &org, &account, &c.Name, &by, &at, &archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, err
		}

		return c, fmt.Errorf("reading a category: %w", err)
	}

	var e [3]error
	c.ID, e[0] = types.ParseID(id)
	c.CreatedBy, e[1] = types.ParseID(by)

	if org.Valid {
		c.Owner.Kind = types.ScopeOrg
		c.Owner.ID, e[2] = types.ParseID(org.String)
	} else {
		c.Owner.Kind = types.ScopeAccount
		c.Owner.ID, e[2] = types.ParseID(account.String)
	}

	if err := errors.Join(e[:]...); err != nil {
		return c, fmt.Errorf("a stored category is unreadable: %w", err)
	}

	c.CreatedAt = time.UnixMilli(at).UTC()
	if archived.Valid {
		c.ArchivedAt = time.UnixMilli(archived.Int64).UTC()
	}

	return c, nil
}
