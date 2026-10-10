package userdb_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// before is the people and their keys as they stood before a key had
// scopes, written out rather than derived (CLAUDE.md): every database on a
// server that ran the release before this one has these.
const before = `
CREATE TABLE IF NOT EXISTS users (
    id          TEXT    PRIMARY KEY,
    email       TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL DEFAULT '',
    lang        TEXT    NOT NULL DEFAULT '',
    site_admin  INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS api_keys (
    id            TEXT    PRIMARY KEY,
    user_id       TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    hash          BLOB    NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    client        TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS api_keys_user ON api_keys (user_id, expires_at);
`

// A database from before scopes gets them; every key in it was a
// translator's, and says so; and a key with other scopes can be added.
func TestInitOverTheSchemaBefore(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.ExecContext(t.Context(), before); err != nil {
		t.Fatal(err)
	}

	user, key := types.NewID(), types.NewID()

	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO users (id, email, created_at, updated_at) VALUES (?, 'translator@example.org', 1, 1)`, user.String()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO api_keys (id, user_id, name, hash, created_at, expires_at) VALUES (?, ?, 'laptop', x'00', 1, ?)`,
		key.String(), user.String(), time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := userdb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, userdb.Expected); err != nil {
		t.Fatal(err)
	}

	store := userdb.NewStore(db)

	got, err := store.APIKeyByID(t.Context(), key)
	if err != nil || !slices.Equal(got.Scopes, []userbus.Scope{userbus.Translate}) {
		t.Fatalf("the old key: %+v, %v", got, err)
	}

	k := userbus.APIKey{ID: types.NewID(), UserID: user, Name: "gmail script", Hash: []byte{1},
		CreatedAt: got.CreatedAt, ExpiresAt: got.ExpiresAt, Scopes: []userbus.Scope{userbus.Upload}}

	if made, err := store.CreateAPIKey(t.Context(), k, 5); err != nil || !made {
		t.Fatalf("a new key: %v, %v", made, err)
	}

	if got, err := store.APIKeyByID(t.Context(), k.ID); err != nil || !slices.Equal(got.Scopes, k.Scopes) {
		t.Errorf("the new key: %+v, %v", got, err)
	}
}
