package userbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// What a key may do is its scopes (below), which the person chose when they
// made it; which route needs which scope is the routes' business, and what
// the person may do once there is still asked of tenancybus every time, so a
// key never reaches past its person. Who may hold a translating key is the
// caller's question, asked of the translation domain.

// Scope is one thing a key may be used for (docs/books-api.md, "Scopes").
// The words are OAuth's way of writing scopes, so that a key given to a
// program through OAuth and one made on the keys screen are described alike.
type Scope string

const (
	// Translate is the interface's translations: only the site
	// administrator and translators may hold it.
	Translate Scope = "translate"

	// Upload is putting files in the person's statement inbox, and
	// nothing else -- not even reading the inbox back. It is the key a
	// script in somebody's Google account holds, where whoever else can
	// open that account can read it, so it must not be a key to the books.
	Upload Scope = "upload"

	// BooksRead is reading what the person's roles let them read.
	BooksRead Scope = "books:read"

	// BooksWrite is keeping the books as far as the person's roles let
	// them. It includes BooksRead: nobody sorts a month blind.
	BooksWrite Scope = "books:write"
)

// Scopes is every scope, in the order a page lists them.
var Scopes = []Scope{Upload, BooksRead, BooksWrite, Translate}

// Allows reports whether a key with these scopes may do what needs s.
func Allows(scopes []Scope, s Scope) bool {
	return slices.Contains(scopes, s) || (s == BooksRead && slices.Contains(scopes, BooksWrite))
}

// lifeOf is how long a key with these scopes lasts: a year for a key that
// may only upload, which lives in a script that nobody should have to open
// every three months and whose worst use is a file waiting in a queue;
// APIKeyLife for any other.
func lifeOf(scopes []Scope) time.Duration {
	if len(scopes) == 1 && scopes[0] == Upload {
		return UploadKeyLife
	}

	return APIKeyLife
}

// tidyScopes is the scopes given, known ones only, each once, in the order
// of Scopes; nil when none is known.
func tidyScopes(given []Scope) []Scope {
	var out []Scope

	for _, s := range Scopes {
		if slices.Contains(given, s) {
			out = append(out, s)
		}
	}

	return out
}

const (
	// APIKeyLife is ninety days, absolute, as a session is. A key lives in a
	// shell profile or a password manager on somebody's laptop, which is
	// exactly the kind of place a credential is forgotten in; it ending on
	// its own is the backstop for the person who forgets to revoke it.
	APIKeyLife = 90 * 24 * time.Hour

	// UploadKeyLife is how long a key that may only upload lasts (lifeOf).
	UploadKeyLife = 365 * 24 * time.Hour

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

	// Scopes is what it may be used for, at least one.
	Scopes []Scope
}

// Allows reports whether the key may do what needs s.
func (k APIKey) Allows(s Scope) bool { return Allows(k.Scopes, s) }

// The errors making a key can give, for a page to word.
var (
	// ErrKeyName is a key with no name, or one longer than MaxKeyName.
	ErrKeyName = errors.New("a key needs a name, such as the computer it is for")

	// ErrTooManyKeys is a person who has MaxAPIKeys live keys already.
	ErrTooManyKeys = errors.New("there are as many keys as allowed; revoke one first")

	// ErrKeyScope is a key asked for with nothing it may be used for.
	ErrKeyScope = errors.New("say what the key is for")
)

// CreateAPIKey makes a key for a person, for the scopes given, and returns
// it with the secret, the only time the secret exists outside the request.
// Whether the person may hold each scope -- Translate is the translation
// domain's to say -- is the caller's question, asked before this.
func (b *Business) CreateAPIKey(ctx context.Context, now time.Time, userID types.ID, name string, scopes []Scope) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	scopes = tidyScopes(scopes)

	switch {
	case name == "" || utf8.RuneCountInString(name) > MaxKeyName:
		return APIKey{}, "", ErrKeyName
	case len(scopes) == 0:
		return APIKey{}, "", ErrKeyScope
	}

	cred := mintCredential()

	k := APIKey{
		ID: cred.id, UserID: userID, Name: name, Hash: cred.hash,
		CreatedAt: now, ExpiresAt: now.Add(lifeOf(scopes)), Scopes: scopes,
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

// AuthenticateAPIKey turns a presented key into the person it belongs to
// and the key itself, whose scopes say what it may do, with the same single
// answer for every failure as a session (ErrDenied).
func (b *Business) AuthenticateAPIKey(ctx context.Context, now time.Time, presented string) (User, APIKey, error) {
	rest, ok := strings.CutPrefix(presented, APIKeyPrefix)
	if !ok {
		return User{}, APIKey{}, ErrDenied
	}

	id, secret, err := splitCredential(rest)
	if err != nil {
		return User{}, APIKey{}, ErrDenied
	}

	k, err := b.store.APIKeyByID(ctx, id)

	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, APIKey{}, ErrDenied
	case err != nil:
		return User{}, APIKey{}, fmt.Errorf("reading the API key: %w", err)
	}

	if !verifySecret(k.Hash, secret) || !now.Before(k.ExpiresAt) {
		return User{}, APIKey{}, ErrDenied
	}

	u, err := b.store.UserByID(ctx, k.UserID)

	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, APIKey{}, ErrDenied
	case err != nil:
		return User{}, APIKey{}, fmt.Errorf("reading the key's user: %w", err)
	case !u.Enabled:
		return User{}, APIKey{}, ErrDenied
	}

	// One statement that only writes when the stamp is an hour old. A
	// failure is logged, not returned: the key is good, and the caller's
	// request should not fail over a bookkeeping column.
	if err := b.store.TouchAPIKey(ctx, k.ID, now, now.Add(-touchEvery)); err != nil {
		b.log.Error("an API key's last use could not be recorded", "key_id", k.ID.String(), "error", err)
	}

	return u, k, nil
}
