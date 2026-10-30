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

// before is the people, their keys and the codes that become keys as they
// stood before either had scopes, written out rather than derived
// (CLAUDE.md): a database from before keys had scopes has these, and one
// from before codes had them has the codes.
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

CREATE TABLE IF NOT EXISTS oauth_grants (
    id            TEXT    PRIMARY KEY,
    user_id       TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash          BLOB    NOT NULL,
    client_id     TEXT    NOT NULL,
    client_name   TEXT    NOT NULL,
    redirect_uri  TEXT    NOT NULL,
    challenge     TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    used_at       INTEGER
) STRICT;
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

	grant := types.NewID()

	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO oauth_grants (id, user_id, hash, client_id, client_name, redirect_uri, challenge, created_at, expires_at)
		 VALUES (?, ?, x'00', 'https://claude.ai/oauth/claude-code-client-metadata', 'Claude', 'https://claude.ai/api/mcp/auth_callback', 'x', 1, ?)`,
		grant.String(), user.String(), time.Now().Add(time.Hour).UnixMilli()); err != nil {
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

	// A code from before was a translator's.
	if g, err := store.GrantByID(t.Context(), grant); err != nil || !slices.Equal(g.Scopes, []userbus.Scope{userbus.Translate}) {
		t.Errorf("the old code: %+v, %v", g, err)
	}
}

// A person from before time zones has none, and the defaults for the
// weekly email; the sign-in page's zone is written once and never over
// one already there; and settings saved keep what the email kept.
func TestInitGivesOldPeopleNoTimeZone(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.ExecContext(t.Context(), before); err != nil {
		t.Fatal(err)
	}

	user := types.NewID()

	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO users (id, email, created_at, updated_at) VALUES (?, 'treasurer@example.org', 1, 1)`, user.String()); err != nil {
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
	ctx := t.Context()

	if u, err := store.UserByID(ctx, user); err != nil || u.Zone != "" {
		t.Fatalf("the old person: %+v, %v", u, err)
	}

	if _, ok, err := store.Weekly(ctx, user); err != nil || ok {
		t.Errorf("settings nobody saved: %v, %v", ok, err)
	}

	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	for _, zone := range []string{"America/Sao_Paulo", "Europe/Lisbon"} {
		if err := store.SetZone(ctx, now, user, zone, true); err != nil {
			t.Fatal(err)
		}
	}

	if u, _ := store.UserByID(ctx, user); u.Zone != "America/Sao_Paulo" {
		t.Errorf("the zone set once: %q", u.Zone)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO notification_prefs (user_id, sent_up_to, unsubscribe_hash) VALUES (?, 42, 'h')`, user.String()); err != nil {
		t.Fatal(err)
	}

	want := userbus.Weekly{Enabled: false, Weekday: time.Friday, Hour: 17}
	if err := store.SetWeekly(ctx, user, want); err != nil {
		t.Fatal(err)
	}

	if got, ok, err := store.Weekly(ctx, user); err != nil || !ok || got != want {
		t.Errorf("the settings saved: %+v, %v, %v", got, ok, err)
	}

	var (
		sent int64
		hash string
	)

	if err := db.QueryRowContext(ctx, `SELECT sent_up_to, unsubscribe_hash FROM notification_prefs WHERE user_id = ?`, user.String()).Scan(&sent, &hash); err != nil || sent != 42 || hash != "h" {
		t.Errorf("what the email kept: %d %q, %v", sent, hash, err)
	}
}
