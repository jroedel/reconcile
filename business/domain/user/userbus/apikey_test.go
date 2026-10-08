package userbus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
)

func TestAnAPIKeyActsAsItsPerson(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	k, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "  laptop  ")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(key, userbus.APIKeyPrefix) || k.Name != "laptop" {
		t.Errorf("key %q, name %q", key[:4], k.Name)
	}

	got, err := b.AuthenticateAPIKey(t.Context(), start, key)
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

	_, _ = b.AuthenticateAPIKey(t.Context(), start.Add(30*time.Minute), key)
	if !lastUsed().Equal(start) {
		t.Error("a use within the hour was recorded")
	}

	later := start.Add(61 * time.Minute)
	_, _ = b.AuthenticateAPIKey(t.Context(), later, key)

	if !lastUsed().Equal(later) {
		t.Error("a use after the hour was not recorded")
	}
}

// Every failure is ErrDenied, so that a caller cannot tell a wrong secret
// from a key that never existed.
func TestABadKeyIsTheSameRefusalAsAnyOther(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	_, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "laptop")
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
		if _, err := b.AuthenticateAPIKey(t.Context(), start, presented); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("%s: %v, want ErrDenied", name, err)
		}
	}
}

func TestAKeyEndsOnItsOwnOrWhenRevoked(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")
	other := signUp(t, b, "stranger@example.org")

	k, key, err := b.CreateAPIKey(t.Context(), start, u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := b.AuthenticateAPIKey(t.Context(), start.Add(userbus.APIKeyLife), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("an expired key: %v", err)
	}

	// Somebody else's key is not there to revoke.
	if err := b.RevokeAPIKey(t.Context(), other.ID, k.ID); !errors.Is(err, userbus.ErrNotFound) {
		t.Errorf("revoking somebody else's key: %v", err)
	}

	if err := b.RevokeAPIKey(t.Context(), u.ID, k.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := b.AuthenticateAPIKey(t.Context(), start, key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a revoked key: %v", err)
	}

	// A person who is turned off is turned off for their keys too.
	_, key, err = b.CreateAPIKey(t.Context(), start, u.ID, "desktop")
	if err != nil {
		t.Fatal(err)
	}

	disable(t, b, u)

	if _, err := b.AuthenticateAPIKey(t.Context(), start, key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a disabled person's key: %v", err)
	}
}

func TestAKeyNeedsANameAndThereIsALimit(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	for _, name := range []string{"", "   ", strings.Repeat("x", userbus.MaxKeyName+1)} {
		if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, name); !errors.Is(err, userbus.ErrKeyName) {
			t.Errorf("name %q: %v", name, err)
		}
	}

	for i := range userbus.MaxAPIKeys {
		if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "key "+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "one more"); !errors.Is(err, userbus.ErrTooManyKeys) {
		t.Errorf("one key too many: %v", err)
	}

	// Expired keys do not count.
	if _, _, err := b.CreateAPIKey(t.Context(), start.Add(userbus.APIKeyLife), u.ID, "one more"); err != nil {
		t.Errorf("a key once the others expired: %v", err)
	}
}
