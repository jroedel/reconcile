package userbus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
)

func TestAnAPIKeyActsAsItsPerson(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	k, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "  laptop  ", []userbus.Scope{userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(key, userbus.APIKeyPrefix) || k.Name != "laptop" {
		t.Errorf("key %q, name %q", key[:4], k.Name)
	}

	got, _, err := b.AuthenticateAPIKey(t.Context(), start, key)
	if err != nil || got.ID != u.ID {
		t.Fatalf("the key does not authenticate: %v", err)
	}

	// The first use is recorded; uses within the hour write nothing more.
	lastUsed := func() time.Time {
		t.Helper()

		keys, err := b.APIKeys(t.Context(), start, u.ID)
		if err != nil || len(keys) != 1 {
			t.Fatalf("keys: %v, %v", keys, err)
		}

		return keys[0].LastUsedAt
	}

	if !lastUsed().Equal(start) {
		t.Fatalf("last used %v, want %v", lastUsed(), start)
	}

	_, _, _ = b.AuthenticateAPIKey(t.Context(), start.Add(30*time.Minute), key)
	if !lastUsed().Equal(start) {
		t.Error("a use within the hour was recorded")
	}

	later := start.Add(61 * time.Minute)
	_, _, _ = b.AuthenticateAPIKey(t.Context(), later, key)

	if !lastUsed().Equal(later) {
		t.Error("a use after the hour was not recorded")
	}
}

// Every failure is ErrDenied, so that a caller cannot tell a wrong secret
// from a key that never existed.
func TestABadKeyIsTheSameRefusalAsAnyOther(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	_, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "laptop", []userbus.Scope{userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	id, _, _ := strings.Cut(strings.TrimPrefix(key, userbus.APIKeyPrefix), ".")

	for name, presented := range map[string]string{
		"empty":            "",
		"no prefix":        strings.TrimPrefix(key, userbus.APIKeyPrefix),
		"another app's":    "stw_" + strings.TrimPrefix(key, userbus.APIKeyPrefix),
		"wrong secret":     userbus.APIKeyPrefix + id + ".AAAAAAAAAAAAAAAAAAAAAAAAAA",
		"no secret":        userbus.APIKeyPrefix + id,
		"unknown id":       userbus.APIKeyPrefix + strings.Repeat("a", 32) + ".AAAAAAAAAAAAAAAAAAAAAAAAAA",
		"one char changed": key[:len(key)-1] + string(key[len(key)-1]^1),
	} {
		if _, _, err := b.AuthenticateAPIKey(t.Context(), start, presented); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("%s: %v, want ErrDenied", name, err)
		}
	}
}

func TestAKeyEndsOnItsOwnOrWhenRevoked(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")
	other := signUp(t, b, "stranger@example.org")

	k, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "laptop", []userbus.Scope{userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.AuthenticateAPIKey(t.Context(), start.Add(userbus.APIKeyLife), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("an expired key: %v", err)
	}

	// Somebody else's key is not there to revoke.
	if err := b.RevokeAPIKey(t.Context(), other.ID, k.ID); !errors.Is(err, userbus.ErrNotFound) {
		t.Errorf("revoking somebody else's key: %v", err)
	}

	if err := b.RevokeAPIKey(t.Context(), u.ID, k.ID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.AuthenticateAPIKey(t.Context(), start, key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a revoked key: %v", err)
	}

	// A person who is turned off is turned off for their keys too.
	_, key, err = b.CreateAPIKey(t.Context(), start, u.ID, "desktop", []userbus.Scope{userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	disable(t, b, u)

	if _, _, err := b.AuthenticateAPIKey(t.Context(), start, key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a disabled person's key: %v", err)
	}
}

func TestAKeyNeedsANameAndThereIsALimit(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	for _, name := range []string{"", "   ", strings.Repeat("x", userbus.MaxKeyName+1)} {
		if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, name, []userbus.Scope{userbus.Translate}); !errors.Is(err, userbus.ErrKeyName) {
			t.Errorf("name %q: %v", name, err)
		}
	}

	for i := range userbus.MaxAPIKeys {
		if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "key "+string(rune('a'+i)), []userbus.Scope{userbus.Translate}); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "one more", []userbus.Scope{userbus.Translate}); !errors.Is(err, userbus.ErrTooManyKeys) {
		t.Errorf("one key too many: %v", err)
	}

	// Expired keys do not count.
	if _, _, err := b.CreateAPIKey(t.Context(), start.Add(userbus.APIKeyLife), u.ID, "one more", []userbus.Scope{userbus.Translate}); err != nil {
		t.Errorf("a key once the others expired: %v", err)
	}
}

// A key says what it may be used for, and it comes back from the store
// saying so: the scopes are the whole of what a route asks of it.
func TestAKeyKeepsItsScopes(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	// Unknown words are dropped, repeats kept once, and the order is the
	// list's, so two keys for the same purposes read alike.
	k, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "claude", []userbus.Scope{"books:write", "everything", userbus.Translate, "books:write"})
	if err != nil {
		t.Fatal(err)
	}

	if want := []userbus.Scope{userbus.BooksWrite, userbus.Translate}; !slices.Equal(k.Scopes, want) {
		t.Errorf("scopes %q, want %q", k.Scopes, want)
	}

	_, got, err := b.AuthenticateAPIKey(t.Context(), start, key)
	if err != nil || !slices.Equal(got.Scopes, k.Scopes) {
		t.Fatalf("authenticated with scopes %q: %v", got.Scopes, err)
	}

	// Keeping the books includes reading them; nothing includes uploading.
	for s, want := range map[userbus.Scope]bool{userbus.BooksRead: true, userbus.BooksWrite: true, userbus.Translate: true, userbus.Upload: false} {
		if got.Allows(s) != want {
			t.Errorf("allows %s: %v", s, !want)
		}
	}

	// A key for nothing is refused.
	for _, scopes := range [][]userbus.Scope{nil, {"everything"}} {
		if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "nothing", scopes); !errors.Is(err, userbus.ErrKeyScope) {
			t.Errorf("a key for %q: %v", scopes, err)
		}
	}
}

// A key that may only upload lasts a year, since it lives in a script;
// any other, ninety days, uploading with it or not.
func TestAnUploadKeyLastsAYear(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	up, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "gmail script", []userbus.Scope{userbus.Upload})
	if err != nil {
		t.Fatal(err)
	}

	both, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "both", []userbus.Scope{userbus.Upload, userbus.BooksRead})
	if err != nil {
		t.Fatal(err)
	}

	if up.ExpiresAt != start.Add(userbus.UploadKeyLife) || both.ExpiresAt != start.Add(userbus.APIKeyLife) {
		t.Errorf("an upload key ends %v, one that also reads %v", up.ExpiresAt, both.ExpiresAt)
	}
}
