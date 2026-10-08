// Package userdb stores users and their credentials in SQLite.
//
// Adapted from mass-intentions. The differences: a sign-in code names an
// address and only sometimes a user, because sign-up is open (userbus); a
// code is claimed against two ceilings in one statement; and an address
// change counts its tries, as a sign-in code does.
//
// # The atomic claims
//
// ClaimCode, TryToken, UseToken, UseBackupCode, ClaimBootstrap,
// TryEmailChange and UseEmailChange are each one statement whose WHERE clause
// is the check, and each reports whether it was the statement that matched.
// That is the contract userbus.Storer documents: a SELECT followed by an
// UPDATE lets two simultaneous requests both see an unused code and both
// succeed, which is the single-use property gone.
package userdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of userbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ userbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"users":         {"id", "email", "name", "lang", "site_admin", "enabled", "created_at", "updated_at"},
	"signin_codes":  {"id", "email", "user_id", "hash", "created_at", "expires_at", "used_at", "attempts"},
	"email_changes": {"id", "user_id", "new_email", "hash", "created_at", "expires_at", "used_at", "cancelled_at", "attempts"},
	"backup_codes":  {"id", "user_id", "hash", "created_at", "used_at"},
	"sessions":      {"id", "user_id", "hash", "created_at", "expires_at"},
	"bootstrap":     {"id", "claimed_at"},
	"api_keys":      {"id", "user_id", "name", "hash", "created_at", "expires_at", "last_used_at", "client"},
	"oauth_grants":  {"id", "user_id", "hash", "client_id", "client_name", "redirect_uri", "challenge", "created_at", "expires_at", "used_at"},
}

// Init creates the tables. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
-- The UNIQUE on email uses SQLite's default BINARY collation, so it is
-- case-sensitive. Correct rather than a gap: types.Email folds case before an
-- address reaches this layer, so there is one spelling of any address.
--
-- lang is '' for "follow the browser". No CHECK listing the languages, so
-- that adding one is not a table rebuild (CLAUDE.md, "When there is a
-- database"); the business layer writes only a language it knows.
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

-- A request to sign in. email is where the code went; user_id is the user
-- who held that address then, or NULL for somebody signing up.
--
-- Kept an hour past expiry (userbus.Prune) so that the hourly ceiling in
-- ClaimCode counts every code sent in the last hour.
CREATE TABLE IF NOT EXISTS signin_codes (
    id          TEXT    PRIMARY KEY,
    email       TEXT    NOT NULL,
    user_id     TEXT    REFERENCES users (id) ON DELETE CASCADE,
    hash        BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER,
    attempts    INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX IF NOT EXISTS signin_codes_email ON signin_codes (email, expires_at);
CREATE INDEX IF NOT EXISTS signin_codes_created_at ON signin_codes (created_at);

-- An address somebody has asked to move to, not yet proved.
--
-- Its own table rather than a second use of signin_codes, although the
-- mechanism is the same shape: a code that signs you in and a code that
-- changes where the only way in gets sent are different powers.
--
-- new_email is not unique here and users.email is. Two people may have a
-- pending change to the same address, and the unique index on users settles
-- it when one of them confirms.
CREATE TABLE IF NOT EXISTS email_changes (
    id           TEXT    PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    new_email    TEXT    NOT NULL,
    hash         BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    used_at      INTEGER,
    cancelled_at INTEGER,
    attempts     INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX IF NOT EXISTS email_changes_pending ON email_changes (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS backup_codes (
    id          TEXT    PRIMARY KEY,
    user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash        BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    used_at     INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS backup_codes_user_id ON backup_codes (user_id);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT    PRIMARY KEY,
    user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash        BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at ON sessions (expires_at);

-- One row, ever: "has the bootstrap been spent" is a primary key conflict
-- rather than a count, so claiming it is one statement with no race.
CREATE TABLE IF NOT EXISTS bootstrap (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    claimed_at  INTEGER NOT NULL
) STRICT;

-- A program acting as a person; see userbus/apikey.go. A table of its own
-- rather than a kind of session, because a key has a name and a last use,
-- and is listed and revoked by its owner, none of which a session is.
-- client is the OAuth client_id it was given to, or '' for one made on the
-- profile page.
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

-- The code a person's agreeing hands to a program signing in through OAuth
-- (userbus/oauth.go), traded once for a key. It is a sign-in code in all but
-- who carries it, and is kept the same way: the hash, never the secret, and
-- used_at as the claim.
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

CREATE INDEX IF NOT EXISTS oauth_grants_user ON oauth_grants (user_id, expires_at);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the user tables: %w", err)
	}

	return nil
}

const userColumns = `id, email, name, lang, site_admin, enabled, created_at, updated_at`

// CreateUser inserts a user. A unique violation is returned wrapped, not
// translated: userbus recognises it as two first sign-ins racing.
func (s *Store) CreateUser(ctx context.Context, u userbus.User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID.String(), u.Email.String(), u.Name, string(u.Lang), boolOf(u.SiteAdmin), boolOf(u.Enabled),
		msOf(u.CreatedAt), msOf(u.UpdatedAt))
	if err != nil {
		return fmt.Errorf("inserting the user: %w", err)
	}

	return nil
}

// UpdateUser replaces a user's mutable fields.
func (s *Store) UpdateUser(ctx context.Context, u userbus.User) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE users SET email = ?, name = ?, lang = ?, site_admin = ?, enabled = ?, updated_at = ?
WHERE id = ?`,
		u.Email.String(), u.Name, string(u.Lang), boolOf(u.SiteAdmin), boolOf(u.Enabled), msOf(u.UpdatedAt),
		u.ID.String())

	switch {
	case sqldb.IsUniqueViolation(err):
		// Only an address change racing a sign-up can reach this, since
		// userbus checks first; translated so that the race reads the same as
		// the check.
		return userbus.ErrAddressTaken
	case err != nil:
		return fmt.Errorf("updating the user: %w", err)
	}

	return oneRow(res, "the user")
}

// UserByID finds a user by identifier.
func (s *Store) UserByID(ctx context.Context, id types.ID) (userbus.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id.String()))
}

// UserByEmail finds a user by address.
func (s *Store) UserByEmail(ctx context.Context, email types.Email) (userbus.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email.String()))
}

// Users returns every user, oldest first.
func (s *Store) Users(ctx context.Context) ([]userbus.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("reading the users: %w", err)
	}
	defer rows.Close()

	var out []userbus.User

	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the users: %w", err)
	}

	return out, nil
}

// ClaimCode stores a sign-in code if the address and the service are both
// under their ceilings.
//
// A code out of tries still counts against its address until it expires:
// otherwise asking again would buy five more guesses as often as anybody
// liked.
//
// One INSERT ... SELECT whose WHERE holds both counts, so that a burst of
// requests cannot each read "four outstanding" and all insert a fifth. With
// one connection the statements are serialised anyway; this keeps it true if
// that ever changes, and keeps the rule in one place.
func (s *Store) ClaimCode(ctx context.Context, t userbus.Token, now time.Time, perAddress int, hourAgo time.Time, perHour int) (bool, error) {
	const q = `
INSERT INTO signin_codes (id, email, user_id, hash, created_at, expires_at)
SELECT ?, ?, ?, ?, ?, ?
WHERE (SELECT COUNT(*) FROM signin_codes
       WHERE email = ? AND used_at IS NULL AND expires_at > ?) < ?
  AND (SELECT COUNT(*) FROM signin_codes WHERE created_at > ?) < ?`

	res, err := s.db.ExecContext(ctx, q,
		t.ID.String(), t.Email.String(), nullID(t.UserID), t.Hash, msOf(t.CreatedAt), msOf(t.ExpiresAt),
		t.Email.String(), msOf(now), perAddress,
		msOf(hourAgo), perHour)
	if err != nil {
		return false, fmt.Errorf("saving the sign-in code: %w", err)
	}

	return affected(res)
}

// TokenByID finds a sign-in code by identifier.
func (s *Store) TokenByID(ctx context.Context, id types.ID) (userbus.Token, error) {
	const q = `
SELECT id, email, user_id, hash, created_at, expires_at, used_at, attempts
FROM signin_codes WHERE id = ?`

	var (
		t                   userbus.Token
		rawID, rawEmail     string
		rawUID              sql.NullString
		made, expiry        int64
		used                sql.NullInt64
		hash                []byte
		parseErr, parseErr2 error
	)

	err := s.db.QueryRowContext(ctx, q, id.String()).
		Scan(&rawID, &rawEmail, &rawUID, &hash, &made, &expiry, &used, &t.Attempts)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.Token{}, userbus.ErrNotFound
	case err != nil:
		return userbus.Token{}, fmt.Errorf("reading the sign-in code: %w", err)
	}

	t.ID, parseErr = types.ParseID(rawID)
	t.Email, parseErr2 = types.ParseEmail(rawEmail)

	if err := errors.Join(parseErr, parseErr2); err != nil {
		return userbus.Token{}, fmt.Errorf("a stored sign-in code is unreadable: %w", err)
	}

	if rawUID.Valid {
		if t.UserID, err = types.ParseID(rawUID.String); err != nil {
			return userbus.Token{}, fmt.Errorf("a stored sign-in code names a bad user: %w", err)
		}
	}

	t.Hash = hash
	t.CreatedAt, t.ExpiresAt, t.UsedAt = timeOf(made), timeOf(expiry), timeOfNull(used)

	return t, nil
}

// TryToken counts one code typed against a sign-in request, reporting false
// when it was already spent, has expired, or is out of tries.
func (s *Store) TryToken(ctx context.Context, id types.ID, now time.Time, limit int) (bool, error) {
	const q = `
UPDATE signin_codes SET attempts = attempts + 1
WHERE id = ? AND used_at IS NULL AND expires_at > ? AND attempts < ?`

	res, err := s.db.ExecContext(ctx, q, id.String(), msOf(now), limit)
	if err != nil {
		return false, fmt.Errorf("counting a try at the sign-in code: %w", err)
	}

	return affected(res)
}

// UseToken spends a sign-in code, reporting whether this call spent it.
func (s *Store) UseToken(ctx context.Context, id types.ID, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE signin_codes SET used_at = ? WHERE id = ? AND used_at IS NULL`, msOf(at), id.String())
	if err != nil {
		return false, fmt.Errorf("spending the sign-in code: %w", err)
	}

	return affected(res)
}

// ReplaceBackupCodes swaps a user's whole set for a new one, in a transaction
// so that a failure cannot leave somebody with none.
func (s *Store) ReplaceBackupCodes(ctx context.Context, userID types.ID, codes []userbus.BackupCode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replacing the backup codes: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM backup_codes WHERE user_id = ?`, userID.String()); err != nil {
		return fmt.Errorf("removing the old backup codes: %w", err)
	}

	for _, c := range codes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO backup_codes (id, user_id, hash, created_at) VALUES (?, ?, ?, ?)`,
			c.ID.String(), userID.String(), c.Hash, msOf(c.CreatedAt)); err != nil {
			return fmt.Errorf("inserting a backup code: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replacing the backup codes: %w", err)
	}

	return nil
}

// BackupCodes returns every code a user holds, spent or not.
func (s *Store) BackupCodes(ctx context.Context, userID types.ID) ([]userbus.BackupCode, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, hash, created_at, used_at
FROM backup_codes WHERE user_id = ? ORDER BY created_at, id`, userID.String())
	if err != nil {
		return nil, fmt.Errorf("reading the backup codes: %w", err)
	}
	defer rows.Close()

	var out []userbus.BackupCode

	for rows.Next() {
		var (
			c             userbus.BackupCode
			rawID, rawUID string
			made          int64
			used          sql.NullInt64
		)

		if err := rows.Scan(&rawID, &rawUID, &c.Hash, &made, &used); err != nil {
			return nil, fmt.Errorf("reading a backup code: %w", err)
		}

		if c.ID, err = types.ParseID(rawID); err != nil {
			return nil, fmt.Errorf("a stored backup code has a bad identifier: %w", err)
		}

		if c.UserID, err = types.ParseID(rawUID); err != nil {
			return nil, fmt.Errorf("a stored backup code names a bad user: %w", err)
		}

		c.CreatedAt, c.UsedAt = timeOf(made), timeOfNull(used)
		out = append(out, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the backup codes: %w", err)
	}

	return out, nil
}

// UseBackupCode spends one code, reporting whether this call spent it.
func (s *Store) UseBackupCode(ctx context.Context, id types.ID, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE backup_codes SET used_at = ? WHERE id = ? AND used_at IS NULL`, msOf(at), id.String())
	if err != nil {
		return false, fmt.Errorf("spending the backup code: %w", err)
	}

	return affected(res)
}

// CreateSession records a signed-in browser.
func (s *Store) CreateSession(ctx context.Context, sess userbus.Session) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		sess.ID.String(), sess.UserID.String(), sess.Hash, msOf(sess.CreatedAt), msOf(sess.ExpiresAt)); err != nil {
		return fmt.Errorf("inserting the session: %w", err)
	}

	return nil
}

// SessionByID finds a session: the read every signed-in request makes.
func (s *Store) SessionByID(ctx context.Context, id types.ID) (userbus.Session, error) {
	var (
		sess          userbus.Session
		rawID, rawUID string
		made, expiry  int64
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, hash, created_at, expires_at FROM sessions WHERE id = ?`, id.String()).
		Scan(&rawID, &rawUID, &sess.Hash, &made, &expiry)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.Session{}, userbus.ErrNotFound
	case err != nil:
		return userbus.Session{}, fmt.Errorf("reading the session: %w", err)
	}

	if sess.ID, err = types.ParseID(rawID); err != nil {
		return userbus.Session{}, fmt.Errorf("a stored session has a bad identifier: %w", err)
	}

	if sess.UserID, err = types.ParseID(rawUID); err != nil {
		return userbus.Session{}, fmt.Errorf("a stored session names a bad user: %w", err)
	}

	sess.CreatedAt, sess.ExpiresAt = timeOf(made), timeOf(expiry)

	return sess, nil
}

// DeleteSession ends one session. One that is not there is not an error.
func (s *Store) DeleteSession(ctx context.Context, id types.ID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id.String()); err != nil {
		return fmt.Errorf("ending the session: %w", err)
	}

	return nil
}

// DeleteUserSessions ends every session a user has.
func (s *Store) DeleteUserSessions(ctx context.Context, userID types.ID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID.String()); err != nil {
		return fmt.Errorf("ending the sessions: %w", err)
	}

	return nil
}

// ClaimBootstrap records that the bootstrap secret has been spent, reporting
// false if it already had been.
func (s *Store) ClaimBootstrap(ctx context.Context, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO bootstrap (id, claimed_at) VALUES (1, ?) ON CONFLICT (id) DO NOTHING`, msOf(at))
	if err != nil {
		return false, fmt.Errorf("recording the bootstrap: %w", err)
	}

	return affected(res)
}

// BootstrapSpent reports whether the secret has been redeemed. A read, not a
// claim: it decides whether to show the form, and ClaimBootstrap still
// decides everything else.
func (s *Store) BootstrapSpent(ctx context.Context) (bool, error) {
	var spent bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM bootstrap WHERE id = 1)`).Scan(&spent); err != nil {
		return false, fmt.Errorf("reading the bootstrap: %w", err)
	}

	return spent, nil
}

// CreateEmailChange records an address somebody has asked to move to.
func (s *Store) CreateEmailChange(ctx context.Context, c userbus.EmailChange) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO email_changes (id, user_id, new_email, hash, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID.String(), c.UserID.String(), c.NewEmail.String(), []byte(c.Hash),
		msOf(c.CreatedAt), msOf(c.ExpiresAt)); err != nil {
		return fmt.Errorf("saving the address change: %w", err)
	}

	return nil
}

const emailChangeColumns = `id, user_id, new_email, hash, created_at, expires_at, used_at, cancelled_at, attempts`

// EmailChangeByID finds one by identifier.
func (s *Store) EmailChangeByID(ctx context.Context, id types.ID) (userbus.EmailChange, error) {
	return scanEmailChange(s.db.QueryRowContext(ctx,
		`SELECT `+emailChangeColumns+` FROM email_changes WHERE id = ?`, id.String()))
}

// PendingEmailChange is a user's newest live change, if there is one.
func (s *Store) PendingEmailChange(ctx context.Context, userID types.ID, now time.Time) (userbus.EmailChange, error) {
	return scanEmailChange(s.db.QueryRowContext(ctx, `
SELECT `+emailChangeColumns+` FROM email_changes
WHERE user_id = ? AND used_at IS NULL AND cancelled_at IS NULL AND expires_at > ?
ORDER BY created_at DESC LIMIT 1`, userID.String(), msOf(now)))
}

// TryEmailChange counts one code typed against an address change.
func (s *Store) TryEmailChange(ctx context.Context, id types.ID, now time.Time, limit int) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE email_changes SET attempts = attempts + 1
WHERE id = ? AND used_at IS NULL AND cancelled_at IS NULL AND expires_at > ? AND attempts < ?`,
		id.String(), msOf(now), limit)
	if err != nil {
		return false, fmt.Errorf("counting a try at the address change: %w", err)
	}

	return affected(res)
}

// UseEmailChange spends one, reporting whether this call spent it.
func (s *Store) UseEmailChange(ctx context.Context, id types.ID, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE email_changes SET used_at = ? WHERE id = ? AND used_at IS NULL AND cancelled_at IS NULL`,
		msOf(at), id.String())
	if err != nil {
		return false, fmt.Errorf("spending the address change: %w", err)
	}

	return affected(res)
}

// CancelEmailChanges withdraws every change a user has outstanding.
func (s *Store) CancelEmailChanges(ctx context.Context, userID types.ID, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
UPDATE email_changes SET cancelled_at = ?
WHERE user_id = ? AND used_at IS NULL AND cancelled_at IS NULL`, msOf(at), userID.String()); err != nil {
		return fmt.Errorf("withdrawing the outstanding address changes: %w", err)
	}

	return nil
}

// PruneExpired removes codes, changes and sessions that expired before the
// cutoff. Backup codes are not here: they do not expire, and a spent one is
// kept so that the count left is honest until they are replaced.
func (s *Store) PruneExpired(ctx context.Context, before time.Time) error {
	for _, q := range []string{
		`DELETE FROM signin_codes WHERE expires_at < ?`,
		`DELETE FROM email_changes WHERE expires_at < ?`,
		`DELETE FROM sessions WHERE expires_at < ?`,
		`DELETE FROM api_keys WHERE expires_at < ?`,
		`DELETE FROM oauth_grants WHERE expires_at < ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, msOf(before)); err != nil {
			return fmt.Errorf("removing expired credentials: %w", err)
		}
	}

	return nil
}

// ------------------------------------------------------------------ API keys

// CreateAPIKey records a key unless the person already has limit live ones:
// the count and the insert in one statement, as for links.
func (s *Store) CreateAPIKey(ctx context.Context, k userbus.APIKey, limit int) (bool, error) {
	const q = `
INSERT INTO api_keys (id, user_id, name, hash, created_at, expires_at)
SELECT ?, ?, ?, ?, ?, ?
WHERE (SELECT count(*) FROM api_keys WHERE user_id = ? AND expires_at > ?) < ?`

	res, err := s.db.ExecContext(ctx, q,
		k.ID.String(), k.UserID.String(), k.Name, k.Hash, msOf(k.CreatedAt), msOf(k.ExpiresAt),
		k.UserID.String(), msOf(k.CreatedAt), limit)
	if err != nil {
		return false, fmt.Errorf("inserting the API key: %w", err)
	}

	return affected(res)
}

// ReplaceAPIKey records a key given to a program through OAuth, removing any
// key the person gave the same program before, and keeps the limit as
// CreateAPIKey does. In one transaction, so that a refusal at the limit
// leaves the old key where it was: a person who cannot connect again must
// not also lose the connection they had.
func (s *Store) ReplaceAPIKey(ctx context.Context, k userbus.APIKey, limit int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("starting to replace the API key: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM api_keys WHERE user_id = ? AND client = ?`, k.UserID.String(), k.Client); err != nil {
		return false, fmt.Errorf("removing the earlier API key: %w", err)
	}

	const q = `
INSERT INTO api_keys (id, user_id, name, hash, created_at, expires_at, client)
SELECT ?, ?, ?, ?, ?, ?, ?
WHERE (SELECT count(*) FROM api_keys WHERE user_id = ? AND expires_at > ?) < ?`

	res, err := tx.ExecContext(ctx, q,
		k.ID.String(), k.UserID.String(), k.Name, k.Hash, msOf(k.CreatedAt), msOf(k.ExpiresAt), k.Client,
		k.UserID.String(), msOf(k.CreatedAt), limit)
	if err != nil {
		return false, fmt.Errorf("inserting the API key: %w", err)
	}

	made, err := affected(res)
	if err != nil || !made {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("replacing the API key: %w", err)
	}

	return true, nil
}

const apiKeyColumns = `id, user_id, name, hash, created_at, expires_at, last_used_at, client`

// APIKeyByID finds a key by identifier.
func (s *Store) APIKeyByID(ctx context.Context, id types.ID) (userbus.APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRowContext(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return userbus.APIKey{}, userbus.ErrNotFound
	}

	return k, err
}

// APIKeys is a person's unexpired keys, newest first.
func (s *Store) APIKeys(ctx context.Context, userID types.ID, now time.Time) ([]userbus.APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+apiKeyColumns+` FROM api_keys WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC, id`,
		userID.String(), msOf(now))
	if err != nil {
		return nil, fmt.Errorf("listing API keys: %w", err)
	}
	defer rows.Close()

	var keys []userbus.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing API keys: %w", err)
	}

	return keys, nil
}

// DeleteAPIKey removes a key, only if it is the person's own.
func (s *Store) DeleteAPIKey(ctx context.Context, userID, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ? AND user_id = ?`, id.String(), userID.String())
	if err != nil {
		return fmt.Errorf("deleting the API key: %w", err)
	}

	ok, err := affected(res)

	switch {
	case err != nil:
		return err
	case !ok:
		return userbus.ErrNotFound
	}

	return nil
}

// TouchAPIKey records a use where the last one is older than notAfter.
func (s *Store) TouchAPIKey(ctx context.Context, id types.ID, at, notAfter time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		msOf(at), id.String(), msOf(notAfter))
	if err != nil {
		return fmt.Errorf("recording the API key's use: %w", err)
	}

	return nil
}

func scanAPIKey(row rowScanner) (userbus.APIKey, error) {
	var (
		k                userbus.APIKey
		rawID, rawUser   string
		created, expires int64
		used             sql.NullInt64
	)

	if err := row.Scan(&rawID, &rawUser, &k.Name, &k.Hash, &created, &expires, &used, &k.Client); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return userbus.APIKey{}, err
		}

		return userbus.APIKey{}, fmt.Errorf("reading an API key: %w", err)
	}

	var err error
	if k.ID, err = types.ParseID(rawID); err != nil {
		return userbus.APIKey{}, fmt.Errorf("a stored API key has a bad identifier: %w", err)
	}

	if k.UserID, err = types.ParseID(rawUser); err != nil {
		return userbus.APIKey{}, fmt.Errorf("a stored API key names a bad account: %w", err)
	}

	k.CreatedAt, k.ExpiresAt, k.LastUsedAt = timeOf(created), timeOf(expires), timeOfNull(used)

	return k, nil
}

// ------------------------------------------------------------------ OAuth grants

// CreateGrant records a code unless the person already has limit live
// ones: the count and the insert in one statement, as for links.
func (s *Store) CreateGrant(ctx context.Context, g userbus.Grant, limit int) (bool, error) {
	const q = `
INSERT INTO oauth_grants (id, user_id, hash, client_id, client_name, redirect_uri, challenge, created_at, expires_at)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE (SELECT count(*) FROM oauth_grants WHERE user_id = ? AND expires_at > ? AND used_at IS NULL) < ?`

	res, err := s.db.ExecContext(ctx, q,
		g.ID.String(), g.UserID.String(), g.Hash, g.ClientID, g.ClientName, g.RedirectURI, g.Challenge, msOf(g.CreatedAt), msOf(g.ExpiresAt),
		g.UserID.String(), msOf(g.CreatedAt), limit)
	if err != nil {
		return false, fmt.Errorf("inserting the OAuth grant: %w", err)
	}

	return affected(res)
}

// GrantByID finds a code by identifier.
func (s *Store) GrantByID(ctx context.Context, id types.ID) (userbus.Grant, error) {
	var (
		g                userbus.Grant
		rawID, rawUser   string
		created, expires int64
		used             sql.NullInt64
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, hash, client_id, client_name, redirect_uri, challenge, created_at, expires_at, used_at FROM oauth_grants WHERE id = ?`,
		id.String()).Scan(&rawID, &rawUser, &g.Hash, &g.ClientID, &g.ClientName, &g.RedirectURI, &g.Challenge, &created, &expires, &used)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.Grant{}, userbus.ErrNotFound
	case err != nil:
		return userbus.Grant{}, fmt.Errorf("reading the OAuth grant: %w", err)
	}

	if g.ID, err = types.ParseID(rawID); err != nil {
		return userbus.Grant{}, fmt.Errorf("a stored OAuth grant has a bad identifier: %w", err)
	}

	if g.UserID, err = types.ParseID(rawUser); err != nil {
		return userbus.Grant{}, fmt.Errorf("a stored OAuth grant names a bad account: %w", err)
	}

	g.CreatedAt, g.ExpiresAt, g.UsedAt = timeOf(created), timeOf(expires), timeOfNull(used)

	return g, nil
}

// UseGrant spends a code, reporting false if it was already spent.
func (s *Store) UseGrant(ctx context.Context, id types.ID, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE oauth_grants SET used_at = ? WHERE id = ? AND used_at IS NULL`, msOf(at), id.String())
	if err != nil {
		return false, fmt.Errorf("spending the OAuth grant: %w", err)
	}

	return affected(res)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (userbus.User, error) {
	var (
		u                       userbus.User
		rawID, rawEmail, lang   string
		admin, enabled          int64
		made, updated           int64
		parseErr, parseErrEmail error
	)

	err := row.Scan(&rawID, &rawEmail, &u.Name, &lang, &admin, &enabled, &made, &updated)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.User{}, userbus.ErrNotFound
	case err != nil:
		return userbus.User{}, fmt.Errorf("reading the user: %w", err)
	}

	// Parsed rather than trusted: an address that cannot be parsed must not
	// become a user who can sign in.
	u.ID, parseErr = types.ParseID(rawID)
	u.Email, parseErrEmail = types.ParseEmail(rawEmail)

	if err := errors.Join(parseErr, parseErrEmail); err != nil {
		return userbus.User{}, fmt.Errorf("a stored user is unreadable: %w", err)
	}

	// A language this binary no longer speaks is "follow the browser", not a
	// user who cannot sign in.
	if l, err := types.ParseLang(lang); err == nil {
		u.Lang = l
	}

	u.SiteAdmin, u.Enabled = admin != 0, enabled != 0
	u.CreatedAt, u.UpdatedAt = timeOf(made), timeOf(updated)

	return u, nil
}

func scanEmailChange(row rowScanner) (userbus.EmailChange, error) {
	var (
		c               userbus.EmailChange
		rawID, rawUID   string
		addr            string
		hash            []byte
		made, expiry    int64
		used, cancelled sql.NullInt64
	)

	err := row.Scan(&rawID, &rawUID, &addr, &hash, &made, &expiry, &used, &cancelled, &c.Attempts)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.EmailChange{}, userbus.ErrNotFound
	case err != nil:
		return userbus.EmailChange{}, fmt.Errorf("reading the address change: %w", err)
	}

	var e1, e2, e3 error

	c.ID, e1 = types.ParseID(rawID)
	c.UserID, e2 = types.ParseID(rawUID)
	c.NewEmail, e3 = types.ParseEmail(addr)

	if err := errors.Join(e1, e2, e3); err != nil {
		return userbus.EmailChange{}, fmt.Errorf("a stored address change is unreadable: %w", err)
	}

	c.Hash = hash
	c.CreatedAt, c.ExpiresAt = timeOf(made), timeOf(expiry)
	c.UsedAt, c.CancelledAt = timeOfNull(used), timeOfNull(cancelled)

	return c, nil
}

// msOf is an instant as Unix milliseconds, the form every time here is kept
// in (CLAUDE.md).
func msOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}

	return t.UnixMilli()
}

func timeOf(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}

	return time.UnixMilli(ms).UTC()
}

func timeOfNull(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}

	return timeOf(n.Int64)
}

func nullID(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}

func boolOf(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

func affected(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking what changed: %w", err)
	}

	return n == 1, nil
}

func oneRow(res sql.Result, what string) error {
	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("checking what changed: %w", err)
	case n == 0:
		return fmt.Errorf("%w: %s", userbus.ErrNotFound, what)
	}

	return nil
}
