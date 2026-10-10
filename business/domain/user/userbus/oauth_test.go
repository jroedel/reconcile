package userbus_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
)

// The pair from RFC 7636, appendix B, so the PKCE here is checked against
// somebody else's arithmetic rather than its own.
const (
	verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	claudeAI = "https://claude.ai/oauth/test-client-metadata"
	callback = "https://claude.ai/api/mcp/auth_callback"
)

func TestAGrantBecomesAKeyOnceAndOnlyForItsProgram(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	code, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	// Another program, another redirect, or the wrong verifier gets
	// nothing, and does not spend the code.
	for name, try := range map[string][3]string{
		"another program":  {"https://claude.ai/oauth/other", callback, verifier},
		"another redirect": {claudeAI, "https://evil.example.invalid/cb", verifier},
		"wrong verifier":   {claudeAI, callback, "A" + verifier[1:]},
	} {
		if _, _, err := b.RedeemGrant(t.Context(), start, code, try[0], try[1], try[2]); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}

	k, key, err := b.RedeemGrant(t.Context(), start, code, claudeAI, callback, verifier)
	if err != nil {
		t.Fatal(err)
	}

	if k.Client != claudeAI || k.Name != "Claude" || !slices.Equal(k.Scopes, []userbus.Scope{userbus.BooksRead, userbus.Translate}) {
		t.Errorf("the key: client %q, name %q, scopes %q", k.Client, k.Name, k.Scopes)
	}

	if got, _, err := b.AuthenticateAPIKey(t.Context(), start, key); err != nil || got.ID != u.ID {
		t.Errorf("the program's key does not authenticate: %v", err)
	}

	if _, _, err := b.RedeemGrant(t.Context(), start, code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a code used twice: %v", err)
	}
}

func TestAGrantExpiresAndNeedsS256(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	code, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.RedeemGrant(t.Context(), start.Add(userbus.GrantLife), code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("an expired code: %v", err)
	}

	for _, c := range []string{"", "plain-text-challenge", challenge + "AA"} {
		if _, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", callback, c, []userbus.Scope{userbus.BooksRead, userbus.Translate}); !errors.Is(err, userbus.ErrChallenge) {
			t.Errorf("challenge %q: %v", c, err)
		}
	}

	if _, err := b.GrantAccess(t.Context(), start, u.ID, "", "Claude", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate}); !errors.Is(err, userbus.ErrClient) {
		t.Errorf("no client: %v", err)
	}
}

// Reconnecting replaces the program's key rather than adding one; at the
// limit, the old key stays, because a person who cannot connect again must
// not also lose the connection they had.
func TestConnectingAgainReplacesTheProgramsKey(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	connect := func() (string, error) {
		t.Helper()

		code, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate})
		if err != nil {
			t.Fatal(err)
		}

		_, key, err := b.RedeemGrant(t.Context(), start, code, claudeAI, callback, verifier)

		return key, err
	}

	first, err := connect()
	if err != nil {
		t.Fatal(err)
	}

	second, err := connect()
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.AuthenticateAPIKey(t.Context(), start, first); !errors.Is(err, userbus.ErrDenied) {
		t.Error("the first connection's key still works")
	}

	for i := range userbus.MaxAPIKeys - 1 {
		if _, _, err := b.CreateAPIKey(t.Context(), start, u.ID, "key "+string(rune('a'+i)), []userbus.Scope{userbus.Translate}); err != nil {
			t.Fatal(err)
		}
	}

	// Five keys, one of them the program's: replacing it keeps five.
	if _, err := connect(); err != nil {
		t.Errorf("reconnecting at the limit: %v", err)
	}

	if _, _, err := b.AuthenticateAPIKey(t.Context(), start, second); !errors.Is(err, userbus.ErrDenied) {
		t.Error("the second connection's key still works")
	}

	disable(t, b, u)

	if _, err := connect(); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a disabled person's grant: %v", err)
	}
}
