package sqldb_test

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// TestRestrictTakesTheDatabaseAwayFromEverybodyElse is about shared hosting
// rather than about SQLite. The account this runs on sits beside other
// customers' accounts under a world-traversable parent, so a database at 0644
// is one anybody with a shell on the machine can read.
func TestRestrictTakesTheDatabaseAwayFromEverybodyElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.db")

	// 0644 on purpose: this is what SQLite leaves behind under the account's
	// default umask, and it is the state found on the server.
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing the fixture: %v", err)
		}
	}

	if err := sqldb.Restrict(path); err != nil {
		t.Fatalf("Restrict: %v", err)
	}

	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}

		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s is mode %04o, want 0600", filepath.Base(name), got)
		}
	}
}

// A -wal and a -shm exist only while a connection is open, so Restrict runs
// against a database that has neither far more often than not. That is the
// ordinary case and must not be an error -- if it were, every clean startup
// would fail.
func TestRestrictAcceptsAMissingWalAndShm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.db")

	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if err := sqldb.Restrict(path); err != nil {
		t.Fatalf("Restrict with no -wal or -shm: %v", err)
	}
}

// A database that is not there at all is a different thing from one whose mode
// cannot be set, and only the second is worth failing a startup over.
func TestRestrictReportsAFailureThatIsNotAMissingFile(t *testing.T) {
	dir := t.TempDir()

	// A directory in place of the database: chmod succeeds on it, so the
	// interesting case is the one below.
	if err := sqldb.Restrict(filepath.Join(dir, "absent.db")); err != nil {
		t.Errorf("a missing database should not be an error, got %v", err)
	}

	// Inside a directory with no execute bit, the chmod cannot resolve the
	// path, and that is a real failure rather than an absence.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	path := filepath.Join(locked, "reconcile.db")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	// root ignores the missing execute bit, so the assertion only means
	// something as an ordinary user.
	if os.Geteuid() == 0 {
		t.Skip("running as root, which is not subject to the permission being tested")
	}

	err := sqldb.Restrict(path)
	if err == nil {
		t.Fatal("an unreachable database should be an error")
	}

	// The distinction that matters: Restrict swallows "it is not there", and a
	// permission failure reported as an absence would be swallowed with it.
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a permission failure was reported as a missing file: %v", err)
	}
}

// A later column reaching a database that already exists. This is the whole of
// the migration story here, so it is worth a test of its own.
func TestAddColumnReachesADatabaseThatPredatesIt(t *testing.T) {
	db := openTemp(t)

	if _, err := db.ExecContext(t.Context(), `CREATE TABLE runs (id TEXT PRIMARY KEY, created INTEGER NOT NULL DEFAULT 0) STRICT`); err != nil {
		t.Fatalf("the old schema: %v", err)
	}

	if _, err := db.ExecContext(t.Context(), `INSERT INTO runs (id, created) VALUES ('a', 3)`); err != nil {
		t.Fatalf("a row written before the column existed: %v", err)
	}

	if err := sqldb.AddColumn(t.Context(), db, "runs", "skipped", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		t.Fatalf("AddColumn: %v", err)
	}

	// Again, because Init runs at every startup and this has to be the
	// uneventful case rather than the error case.
	if err := sqldb.AddColumn(t.Context(), db, "runs", "skipped", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		t.Fatalf("AddColumn a second time: %v", err)
	}

	var created, skipped int

	if err := db.QueryRowContext(t.Context(), `SELECT created, skipped FROM runs WHERE id = 'a'`).Scan(&created, &skipped); err != nil {
		t.Fatalf("reading the widened row back: %v", err)
	}

	if created != 3 || skipped != 0 {
		t.Errorf("created=%d skipped=%d, want 3 and the default 0", created, skipped)
	}
}

func TestAddColumnRefusesAnUnusableName(t *testing.T) {
	db := openTemp(t)

	if err := sqldb.AddColumn(t.Context(), db, "runs", "oops; DROP TABLE runs", "INTEGER"); err == nil {
		t.Error("a column name carrying SQL was accepted")
	}
}

// A fresh database, after Init, is exactly what the infrastructure expects.
// This is the startup path of every new install and every test that follows.
func TestInitSatisfiesInfrastructure(t *testing.T) {
	db := openTemp(t)

	// Twice, because Init runs at every startup.
	for range 2 {
		if err := sqldb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, sqldb.Infrastructure); err != nil {
		t.Errorf("CheckSchema after Init: %v", err)
	}

	// Milliseconds, not seconds and not text. A value in seconds would be in
	// 1970 when read back as milliseconds, which is the mistake this catches.
	var applied int64
	if err := db.QueryRowContext(t.Context(), `SELECT applied_at FROM schema_meta`).Scan(&applied); err != nil {
		t.Fatalf("reading applied_at: %v", err)
	}

	if applied < 1_700_000_000_000 {
		t.Errorf("applied_at = %d, want Unix milliseconds", applied)
	}
}

// The two ways a rolled-back binary can meet a schema it does not match: a
// table that is not there, and a column that is not there. Both must fail, or
// /healthz answers 200 in front of a service that cannot serve.
func TestCheckSchemaNamesWhatIsMissing(t *testing.T) {
	db := openTemp(t)

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatalf("Init: %v", err)
	}

	for name, want := range map[string]sqldb.Expected{
		"a missing table":  {"accounts": {"id"}},
		"a missing column": {"schema_meta": {"id", "later"}},
	} {
		if err := sqldb.CheckSchema(t.Context(), db, want); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Verified against the driver rather than assumed: the code is read from a
// real constraint failure, so a driver upgrade that changed it fails here and
// not as a 500 on somebody's form.
func TestIsUniqueViolationRecognisesTheDriversError(t *testing.T) {
	db := openTemp(t)

	if _, err := db.ExecContext(t.Context(), `CREATE TABLE t (slug TEXT NOT NULL UNIQUE) STRICT`); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := db.ExecContext(t.Context(), `INSERT INTO t VALUES ('checking')`); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	_, err := db.ExecContext(t.Context(), `INSERT INTO t VALUES ('checking')`)
	if !sqldb.IsUniqueViolation(err) {
		t.Errorf("a duplicate was not recognised: %v", err)
	}

	if sqldb.IsUniqueViolation(errors.New("UNIQUE constraint failed")) {
		t.Error("a plain error carrying the words was taken for the driver's")
	}
}

// The same, for a reference. Both directions, because both are used: a row
// naming one that is not there, and a delete of one that is still named.
func TestIsForeignKeyViolationRecognisesTheDriversError(t *testing.T) {
	db := openTemp(t)

	for _, q := range []string{
		`CREATE TABLE parent (id TEXT PRIMARY KEY) STRICT`,
		`CREATE TABLE child (parent_id TEXT NOT NULL REFERENCES parent (id)) STRICT`,
		`INSERT INTO parent VALUES ('a')`,
		`INSERT INTO child VALUES ('a')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	_, err := db.ExecContext(t.Context(), `INSERT INTO child VALUES ('nobody')`)
	if !sqldb.IsForeignKeyViolation(err) {
		t.Errorf("a reference to nothing was not recognised: %v", err)
	}

	_, err = db.ExecContext(t.Context(), `DELETE FROM parent WHERE id = 'a'`)
	if !sqldb.IsForeignKeyViolation(err) {
		t.Errorf("deleting a row still referenced was not recognised: %v", err)
	}

	if sqldb.IsForeignKeyViolation(errors.New("FOREIGN KEY constraint failed")) || sqldb.IsUniqueViolation(err) {
		t.Error("the two codes are confused")
	}
}

func openTemp(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}

// A path is a path, whatever is in it: the database is made exactly where it
// was asked for, and two paths that differ only after a "#" or a "?" are two
// databases. Before the path was escaped, both of these opened the file at
// ".../a", one directory up.
func TestADatabaseIsOpenedAtExactlyItsPath(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"a#1", "a?2", "a%41"} {
		path := filepath.Join(dir, name, "test.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		db, err := sqldb.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := db.ExecContext(t.Context(), `CREATE TABLE marker (name TEXT) STRICT; INSERT INTO marker VALUES (?)`, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		db.Close()

		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: no database at %s: %v", name, path, err)
		}
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("a database was made at %s instead", e.Name())
		}
	}
}
