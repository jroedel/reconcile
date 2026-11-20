package userbus_test

import (
	"errors"
	"slices"
	"strings"
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

	code, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", "", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate})
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

	code, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", "", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.RedeemGrant(t.Context(), start.Add(userbus.GrantLife), code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("an expired code: %v", err)
	}

	for _, c := range []string{"", "plain-text-challenge", challenge + "AA"} {
		if _, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", "", callback, c, []userbus.Scope{userbus.BooksRead, userbus.Translate}); !errors.Is(err, userbus.ErrChallenge) {
			t.Errorf("challenge %q: %v", c, err)
		}
	}

	if _, err := b.GrantAccess(t.Context(), start, u.ID, "", "Claude", "", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate}); !errors.Is(err, userbus.ErrClient) {
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

		code, err := b.GrantAccess(t.Context(), start, u.ID, claudeAI, "Claude", "", callback, challenge, []userbus.Scope{userbus.BooksRead, userbus.Translate})
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

// A program with a key on each computer: connecting a second computer
// keeps the first's key, connecting the first again replaces only its
// own, and the key lasts as long as its scopes say -- a year for one that
// may only send checks.
func TestAKeyForEachComputer(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "treasurer@example.org")

	const (
		program  = "reconcile-checks"
		loopback = "http://127.0.0.1:50123/callback"
	)

	connect := func(device string) (userbus.APIKey, string) {
		t.Helper()

		code, err := b.GrantAccess(t.Context(), start, u.ID, program, "Reconcile Checks — "+device, device, loopback, challenge, []userbus.Scope{userbus.Checks})
		if err != nil {
			t.Fatal(err)
		}

		k, key, err := b.RedeemGrant(t.Context(), start, code, program, loopback, verifier)
		if err != nil {
			t.Fatal(err)
		}

		return k, key
	}

	office, officeKey := connect("Office")
	_, homeKey := connect("Rectory")

	if office.Device != "Office" || office.Name != "Reconcile Checks — Office" || office.ExpiresAt != start.Add(userbus.UploadKeyLife) {
		t.Errorf("the office's key: %+v", office)
	}

	for name, key := range map[string]string{"office": officeKey, "rectory": homeKey} {
		if _, _, err := b.AuthenticateAPIKey(t.Context(), start, key); err != nil {
			t.Errorf("the %s's key after both connected: %v", name, err)
		}
	}

	_, again := connect("Office")

	if _, _, err := b.AuthenticateAPIKey(t.Context(), start, officeKey); !errors.Is(err, userbus.ErrDenied) {
		t.Error("the office's first key still works after it connected again")
	}

	for name, key := range map[string]string{"office": again, "rectory": homeKey} {
		if _, _, err := b.AuthenticateAPIKey(t.Context(), start, key); err != nil {
			t.Errorf("the %s's key now: %v", name, err)
		}
	}

	for typed, want := range map[string]string{"": "this computer", "  Office   PC ": "Office PC", strings.Repeat("x", 50): strings.Repeat("x", userbus.MaxDeviceName)} {
		if got := userbus.DeviceName(typed); got != want {
			t.Errorf("DeviceName(%q) = %q, want %q", typed, got, want)
		}
	}
}
