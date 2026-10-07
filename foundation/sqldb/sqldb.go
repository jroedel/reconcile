// Package sqldb opens the one SQLite database this service uses, and answers
// whether its schema is the one the running binary expects.
//
// The driver is modernc.org/sqlite, which is a dependency because the standard
// library has no SQL driver. Pure Go specifically, rather than the cgo one: the
// release build is CGO_ENABLED=0, so that the artefact is a single file with no
// libc on the server to match against. An ordinary build links the builder's
// glibc and dies on the host with GLIBC_2.xx not found.
//
// Taken from mass-intentions, less what nothing here needs yet. The
// transaction helpers come back with the first store that needs several
// statements to be atomic, rather than sitting here untested until then.
package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	// Imported for its side effect of registering the driver, and by name for
	// the error type IsUniqueViolation reads a result code out of.
	"modernc.org/sqlite"
)

// uriPath escapes the three characters that would end or change a path in a
// file: URI, which is how the driver hands the name to SQLite: a "?" starts
// the parameters and a "#" a fragment, either cutting the path short, and a
// "%" starts an escape. Found when a test named "PR #35" opened a database at
// the truncated path, shared with every other test of that prefix. Nothing
// else needs escaping: SQLite reads every other byte of the path as itself.
var uriPath = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// Open returns the shared handle, with the pragmas this service needs set in
// the DSN so that they apply to every connection rather than to whichever one
// happened to run a SET statement.
//
// MaxOpenConns(1) is deliberate and is not a performance oversight. SQLite has
// one writer; allowing several connections means "database is locked" becomes a
// thing that happens under concurrency, and the fix is a retry loop in every
// caller. One connection makes the queue explicit and the failure mode absent.
// A handful of volunteers on a Saturday morning does not need more.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + uriPath.Replace(path) + "?" + strings.Join([]string{
		// Readers do not block the writer and the writer does not block
		// readers. Also the reason a backup copies the database only while
		// the app is stopped: a live copy can catch a torn page set mid
		// transaction.
		"_pragma=journal_mode(WAL)",

		// Wait rather than failing instantly if a write is in progress. With
		// one connection this should never fire; it is here for the backup
		// process, which is a second writer by definition.
		"_pragma=busy_timeout(5000)",

		// Off by default in SQLite, which surprises everybody exactly once.
		"_pragma=foreign_keys(on)",

		// WAL plus NORMAL is the usual pairing: a crash can lose the last
		// transactions but cannot corrupt the file, and the alternative costs
		// an fsync per commit on a shared disk.
		"_pragma=synchronous(normal)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	return db, nil
}

// Restrict takes the database away from everybody but its owner.
//
// SQLite creates its files with 0666 masked by the process umask, which on the
// shared hosting account this runs on means 0644 -- and "other" there is the
// other customers on the machine. This database holds organisations' bank
// transactions and everybody's sign-ins, and there is no reason for a stranger
// with a shell on the box to read either.
//
// It runs at every startup, so a database created before the supervisor's
// umask was in place is corrected at the next restart rather than staying
// wrong until somebody notices. The -wal and -shm files come and go with the
// connection, so a missing one is the ordinary case and not a failure.
func Restrict(path string) error {
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		switch err := os.Chmod(name, 0o600); {
		case err == nil, errors.Is(err, fs.ErrNotExist):
		default:
			return fmt.Errorf("restricting access to %s: %w", name, err)
		}
	}

	return nil
}

// Init creates the infrastructure table. Domain tables belong to their own
// stores; this one is about the database rather than about any domain.
//
// The DDL is idempotent and runs at every startup, which is the migration
// story in both sibling projects and is inherited on purpose. A column added
// later arrives as an AddColumn with a DEFAULT beside the CREATE, so that on a
// fresh database the CREATE already made it and the ALTER finds nothing to do,
// and on an existing one the CREATE is the no-op.
//
// applied_at is Unix milliseconds in an INTEGER, where mass-intentions has
// text. This is a new database, so it starts on the rule in CLAUDE.md rather
// than on the exception that predates it.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_meta (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    version     INTEGER NOT NULL,
    applied_at  INTEGER NOT NULL
) STRICT;

INSERT INTO schema_meta (id, version, applied_at)
VALUES (1, 1, CAST(unixepoch('subsec') * 1000 AS INTEGER))
ON CONFLICT (id) DO NOTHING;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}

	return nil
}

// Expected is the schema the running binary needs: for each table, the columns
// it will read. Each store contributes its own entry.
type Expected map[string][]string

// Infrastructure is what this package itself requires.
var Infrastructure = Expected{
	"schema_meta": {"id", "version", "applied_at"},
}

// CheckSchema reports whether every expected table exists and carries every
// expected column.
//
// This is what the health endpoint runs, and the reason it is not a ping cost
// a sibling project a silent outage. A rollback restores the binary and not
// the database. The old binary's CREATE TABLE IF NOT EXISTS is a no-op, so it
// starts cleanly; its health check was db.PingContext, so the probe answered
// 200; and every real request then failed on a column that no longer existed.
// A health check that cannot fail is not a health check.
func CheckSchema(ctx context.Context, db *sql.DB, want Expected) error {
	for _, table := range slices.Sorted(maps.Keys(want)) {
		if !safeIdentifier(table) {
			return fmt.Errorf("table name %q is not a plain identifier", table)
		}

		have, err := columns(ctx, db, table)
		if err != nil {
			return err
		}

		if len(have) == 0 {
			return fmt.Errorf("table %s is missing", table)
		}

		for _, col := range want[table] {
			if !slices.Contains(have, col) {
				return fmt.Errorf("table %s has no column %s", table, col)
			}
		}
	}

	return nil
}

// columns lists a table's columns, or none when there is no such table.
//
// A PRAGMA takes no bind parameters, so the name is interpolated. It comes
// from a compiled-in constant rather than from a request, and the caller's
// safeIdentifier check is the belt to that braces.
func columns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('`+table+`')`)
	if err != nil {
		return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
	}
	defer rows.Close()

	var have []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
		}
		have = append(have, name)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the columns of %s: %w", table, err)
	}

	return have, nil
}

// AddColumn adds a column to an existing table, and does nothing if it is
// already there.
//
// This is how a later column reaches a database that already exists. There is
// no migration tool here: a store's Init holds CREATE TABLE IF NOT EXISTS, so a
// fresh database gets every column from the CREATE, and an existing one gets
// the new ones from an AddColumn written beside it. Both calls stay in the
// file, permanently. An index on the new column goes in an Exec *after* this
// call, never in the CREATE block -- see CLAUDE.md for what that cost.
//
// SQLite has no ADD COLUMN IF NOT EXISTS, so "duplicate column name" is read as
// success. Matched on the message because SQLite raises it as a plain
// SQLITE_ERROR with no distinguishing extended code.
//
// decl is SQL and is not escaped, so it must be a literal in the caller. The
// table and column names are checked, because they reach the statement too.
// A column added this way needs a DEFAULT or to be nullable; SQLite refuses a
// NOT NULL column with no default on a table that may already have rows.
func AddColumn(ctx context.Context, db *sql.DB, table, column, decl string) error {
	if !safeIdentifier(table) || !safeIdentifier(column) {
		return fmt.Errorf("adding %s.%s: that is not a usable name", table, column)
	}

	_, err := db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+decl)

	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "duplicate column name"):
		return nil
	default:
		return fmt.Errorf("adding the %s column to %s: %w", column, table, err)
	}
}

func safeIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}

	return true
}

// sqliteConstraintUnique is SQLITE_CONSTRAINT_UNIQUE, raised by a UNIQUE index
// or column. Written out rather than imported from modernc.org/sqlite/lib,
// which is the whole translated library and a heavy import for one integer.
const sqliteConstraintUnique = 2067

// IsUniqueViolation reports whether a write lost to a UNIQUE constraint.
//
// This is how a store makes "is this name free?" and "take it" one statement:
// the insert is the check, so two people adding the same thing at once
// cannot both be told they were first. Read from the numeric code and never
// from the message; the text is not API and has changed between releases.
func IsUniqueViolation(err error) bool {
	e, ok := errors.AsType[*sqlite.Error](err)

	return ok && e.Code() == sqliteConstraintUnique
}

// sqliteConstraintForeignKey is SQLITE_CONSTRAINT_FOREIGNKEY.
const sqliteConstraintForeignKey = 787

// IsForeignKeyViolation reports whether a write was refused by a REFERENCES
// clause: a row pointing at one that does not exist, or a delete of one that
// others still point at.
//
// The second is how "do not delete what is still in use" is one statement: a
// reference with no ON DELETE action makes the delete itself the check, so
// nothing can slip a new reference in between a count and the delete.
func IsForeignKeyViolation(err error) bool {
	e, ok := errors.AsType[*sqlite.Error](err)

	return ok && e.Code() == sqliteConstraintForeignKey
}
