package userbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/types"
)

// An API key is how a program acts as a person: their own Claude, filling
// the interface's translations through /api/v1 instead of a screen
// (docs/translations.md). It is a session without a browser -- the same
// identifier and hashed secret, the same refusal when the account is turned
// off -- with three differences that come from who holds it.
//
// It has a name, because a person may have one on a laptop and one on a
// desktop and needs to know which to revoke. It is shown once, on the page
// that made it, and never again: only the hash is kept, as for a session.
// And it says when it was last used, to the hour, so a key nobody remembers
// using is visible as one.
//
// What a key reaches is not this package's business but the routes': only
// /api/v1 and /mcp accept one, and nothing there reads anybody's books
// (docs/translations.md, "The rules"). Who may make one is the caller's
// question too, asked of the translation domain.

const (
	// APIKeyLife is ninety days, absolute, as a session is. A key lives in a
	// shell profile or a password manager on somebody's laptop, which is
	// exactly the kind of place a credential is forgotten in; it ending on
	// its own is the backstop for the person who forgets to revoke it.
	APIKeyLife = 90 * 24 * time.Hour

	// MaxAPIKeys is how many live keys one person may have: a laptop, a
	// desktop, and room to make a new one before revoking the old.
	MaxAPIKeys = 5

	// APIKeyPrefix starts every key, so one pasted into the wrong place is
	// recognisable -- by a person, and by a secret scanner -- as this app's.
	APIKeyPrefix = "rcn_"

	// touchEvery is how stale "last used" may be. Recording every use would
	// make every API read a write on a single-writer database; to the hour
	// is enough to answer "is this key still in use?".
	touchEvery = time.Hour

	// MaxKeyName is the longest name a key may have.
	MaxKeyName = 60
)

// APIKey is one key, without its secret.
type APIKey struct {
	ID         types.ID
	UserID     types.ID
	Name       string
	Hash       []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time // zero means never

	// Client is the program the key was given to through OAuth, as its
	// client_id (oauth.go); "" for a key a person made on the keys screen.
	Client string
}

// The errors making a key can give, for a page to word.
var (
	// ErrKeyName is a key with no name, or one longer than MaxKeyName.
	ErrKeyName = errors.New("a key needs a name, such as the computer it is for")

	// ErrTooManyKeys is a person who has MaxAPIKeys live keys already.
	ErrTooManyKeys = errors.New("there are as many keys as allowed; revoke one first")
)

// CreateAPIKey makes a key for a person and returns it with the secret, the
// only time the secret exists outside the request.
func (b *Business) CreateAPIKey(ctx context.Context, now time.Time, userID types.ID, name string) (APIKey, string, error) {
	name = strings.TrimSpace(name)

	if name == "" || utf8.RuneCountInString(name) > MaxKeyName {
		return APIKey{}, "", ErrKeyName
	}

	cred := mintCredential()

	k := APIKey{
		ID: cred.id, UserID: userID, Name: name, Hash: cred.hash,
		CreatedAt: now, ExpiresAt: now.Add(APIKeyLife),
	}

	made, err := b.store.CreateAPIKey(ctx, k, MaxAPIKeys)

	switch {
	case err != nil:
		return APIKey{}, "", fmt.Errorf("saving the API key: %w", err)
	case !made:
		return APIKey{}, "", ErrTooManyKeys
	}

	b.log.Info("API key created", "user_id", userID.String(), "key_id", k.ID.String())

	return k, APIKeyPrefix + cred.String(), nil
}

// APIKeys is a person's live keys, newest first.
func (b *Business) APIKeys(ctx context.Context, now time.Time, userID types.ID) ([]APIKey, error) {
	return b.store.APIKeys(ctx, userID, now)
}

// RevokeAPIKey deletes one of a person's own keys. A key that is not theirs
// is ErrNotFound, the same as one that does not exist: the screen only ever
// offers a person their own.
func (b *Business) RevokeAPIKey(ctx context.Context, userID, id types.ID) error {
	if err := b.store.DeleteAPIKey(ctx, userID, id); err != nil {
		return err
	}

	b.log.Info("API key revoked", "user_id", userID.String(), "key_id", id.String())

	return nil
}

// AuthenticateAPIKey turns a presented key into the person it belongs to,
// with the same single answer for every failure as a session (ErrDenied).
func (b *Business) AuthenticateAPIKey(ctx context.Context, now time.Time, presented string) (User, error) {
	rest, ok := strings.CutPrefix(presented, APIKeyPrefix)
	if !ok {
		return User{}, ErrDenied
	}

	id, secret, err := splitCredential(rest)
	if err != nil {
		return User{}, ErrDenied
	}

	k, err := b.store.APIKeyByID(ctx, id)

	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("reading the API key: %w", err)
	}

	if !verifySecret(k.Hash, secret) || !now.Before(k.ExpiresAt) {
		return User{}, ErrDenied
	}

	u, err := b.store.UserByID(ctx, k.UserID)

	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("reading the key's user: %w", err)
	case !u.Enabled:
		return User{}, ErrDenied
	}

	// One statement that only writes when the stamp is an hour old. A
	// failure is logged, not returned: the key is good, and the caller's
	// request should not fail over a bookkeeping column.
	if err := b.store.TouchAPIKey(ctx, k.ID, now, now.Add(-touchEvery)); err != nil {
		b.log.Error("an API key's last use could not be recorded", "key_id", k.ID.String(), "error", err)
	}

	return u, nil
}
