package userbus_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func newBus(t *testing.T) *userbus.Business {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	if err := userdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	return userbus.NewBusiness(slog.New(slog.NewTextHandler(io.Discard, nil)), userdb.NewStore(db))
}

func addr(t *testing.T, s string) types.Email {
	t.Helper()

	e, err := types.ParseEmail(s)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

// signUp signs an address in for the first time and returns the user.
func signUp(t *testing.T, b *userbus.Business, email string) userbus.User {
	t.Helper()

	req, err := b.RequestSignIn(t.Context(), start, addr(t, email))
	if err != nil || !req.Sendable() {
		t.Fatalf("RequestSignIn = %+v, %v", req, err)
	}

	u, _, _, err := b.SignIn(t.Context(), start, req.Pending, req.Code)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// A wrong code that differs from the right one.
func wrong(code string) string {
	if code == "000000" {
		return "000001"
	}

	return "000000"
}

func TestFirstCorrectCodeMakesTheUser(t *testing.T) {
	b := newBus(t)

	req, err := b.RequestSignIn(t.Context(), start, addr(t, "treasurer@example.org"))
	if err != nil || !req.Sendable() {
		t.Fatalf("RequestSignIn = %+v, %v", req, err)
	}

	if all, _ := b.All(t.Context()); len(all) != 0 {
		t.Fatal("asking for a code made a user; an address nobody can read must never become one")
	}

	u, session, created, err := b.SignIn(t.Context(), start.Add(time.Minute), req.Pending, req.Code)
	if err != nil || !created || session == "" {
		t.Fatalf("SignIn = %+v, %q, %v, %v", u, session, created, err)
	}

	if u.Email.String() != "treasurer@example.org" || !u.Enabled || u.SiteAdmin {
		t.Errorf("new user = %+v", u)
	}

	// The second time is the same user.
	req, _ = b.RequestSignIn(t.Context(), start, u.Email)

	again, _, created, err := b.SignIn(t.Context(), start, req.Pending, req.Code)
	if err != nil || created || again.ID != u.ID {
		t.Fatalf("second sign-in = %+v, %v, %v; want the same user", again, created, err)
	}

	got, err := b.Authenticate(t.Context(), start, session)
	if err != nil || got.ID != u.ID {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
}

// The code is bound to the browser that asked: the right six digits with
// somebody else's pending cookie are refused.
func TestCodeIsBoundToTheBrowserThatAsked(t *testing.T) {
	b := newBus(t)
	email := addr(t, "a@example.org")

	mine, _ := b.RequestSignIn(t.Context(), start, email)
	theirs, _ := b.RequestSignIn(t.Context(), start, email)

	if _, _, _, err := b.SignIn(t.Context(), start, theirs.Pending, mine.Code); !errors.Is(err, userbus.ErrDenied) {
		t.Fatalf("a code redeemed with another browser's cookie: %v", err)
	}
}

func TestCodeRefusals(t *testing.T) {
	b := newBus(t)
	email := addr(t, "a@example.org")

	t.Run("expired", func(t *testing.T) {
		req, _ := b.RequestSignIn(t.Context(), start, email)

		if _, _, _, err := b.SignIn(t.Context(), start.Add(16*time.Minute), req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("an expired code: %v", err)
		}
	})

	t.Run("spent", func(t *testing.T) {
		req, _ := b.RequestSignIn(t.Context(), start, email)

		if _, _, _, err := b.SignIn(t.Context(), start, req.Pending, req.Code); err != nil {
			t.Fatal(err)
		}

		if _, _, _, err := b.SignIn(t.Context(), start, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("a code used twice: %v", err)
		}
	})

	t.Run("out of tries", func(t *testing.T) {
		req, _ := b.RequestSignIn(t.Context(), start, email)

		for range 5 {
			if _, _, _, err := b.SignIn(t.Context(), start, req.Pending, wrong(req.Code)); !errors.Is(err, userbus.ErrDenied) {
				t.Fatalf("a wrong code: %v", err)
			}
		}

		if _, _, _, err := b.SignIn(t.Context(), start, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("the right code after five wrong ones: %v", err)
		}
	})

	t.Run("garbage", func(t *testing.T) {
		req, _ := b.RequestSignIn(t.Context(), start, email)

		for _, pending := range []string{"", "x", "nope.nope", req.Pending + "x"} {
			if _, _, _, err := b.SignIn(t.Context(), start, pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
				t.Errorf("pending %q: %v", pending, err)
			}
		}
	})
}

// Typed the way people type it: spaces, a dash.
func TestCodeToleratesHowItIsTyped(t *testing.T) {
	b := newBus(t)

	req, _ := b.RequestSignIn(t.Context(), start, addr(t, "a@example.org"))
	typed := req.Code[:3] + " - " + req.Code[3:] + " "

	if _, _, _, err := b.SignIn(t.Context(), start, req.Pending, typed); err != nil {
		t.Fatalf("%q was refused: %v", typed, err)
	}
}

// Five live codes per address; the sixth request says the same and sends
// nothing. A code out of tries still counts, or asking again would buy five
// more guesses each time.
func TestLiveCodesPerAddress(t *testing.T) {
	b := newBus(t)
	email := addr(t, "a@example.org")

	for i := range 5 {
		req, err := b.RequestSignIn(t.Context(), start, email)
		if err != nil || !req.Sendable() {
			t.Fatalf("request %d: %+v, %v", i+1, req, err)
		}

		for range 5 {
			b.SignIn(t.Context(), start, req.Pending, wrong(req.Code))
		}
	}

	req, err := b.RequestSignIn(t.Context(), start, email)
	if err != nil || req.Sendable() || req.Pending == "" {
		t.Fatalf("sixth request = %+v, %v; want a pending cookie and nothing to send", req, err)
	}

	// Another address is unaffected, and so is this one once they expire.
	if other, _ := b.RequestSignIn(t.Context(), start, addr(t, "b@example.org")); !other.Sendable() {
		t.Error("one address's codes blocked another's")
	}

	if later, _ := b.RequestSignIn(t.Context(), start.Add(16*time.Minute), email); !later.Sendable() {
		t.Error("the address was still blocked after its codes expired")
	}
}

// The ceiling across every address: the form must not be a way to mail
// strangers by the thousand.
func TestCodesPerHourAcrossTheService(t *testing.T) {
	b := newBus(t)

	for i := range userbus.CodesPerHour {
		req, err := b.RequestSignIn(t.Context(), start, addr(t, fmt.Sprintf("person%d@example.org", i)))
		if err != nil || !req.Sendable() {
			t.Fatalf("request %d: %+v, %v", i+1, req, err)
		}
	}

	if req, _ := b.RequestSignIn(t.Context(), start.Add(time.Minute), addr(t, "one-more@example.org")); req.Sendable() {
		t.Fatal("a code was sent past the hourly ceiling")
	}

	if req, _ := b.RequestSignIn(t.Context(), start.Add(61*time.Minute), addr(t, "one-more@example.org")); !req.Sendable() {
		t.Fatal("the ceiling did not lift after an hour")
	}
}

func TestDisabledUserIsSentNothingAndCannotSignIn(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "a@example.org")

	req, _ := b.RequestSignIn(t.Context(), start, u.Email)
	_, session, _, _ := b.SignIn(t.Context(), start, req.Pending, req.Code)

	disable(t, b, u)

	if req, _ := b.RequestSignIn(t.Context(), start, u.Email); req.Sendable() {
		t.Error("a disabled user was sent a code")
	}

	if _, err := b.Authenticate(t.Context(), start, session); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a disabled user's session still works: %v", err)
	}
}

// Ten requests holding the same code at once: exactly one signs in.
func TestCodeIsSingleUseUnderConcurrency(t *testing.T) {
	b := newBus(t)
	signUp(t, b, "a@example.org")

	req, _ := b.RequestSignIn(t.Context(), start, addr(t, "a@example.org"))

	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)

	for range 10 {
		wg.Go(func() {
			if _, _, _, err := b.SignIn(t.Context(), start, req.Pending, req.Code); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	if ok != 1 {
		t.Fatalf("%d requests signed in with one code", ok)
	}
}

// Two codes for a new address typed in two browsers at once: one user, not
// two, and both browsers are signed in as them.
func TestTwoFirstSignInsMakeOneUser(t *testing.T) {
	b := newBus(t)
	email := addr(t, "new@example.org")

	var reqs [4]userbus.SignInRequest
	for i := range reqs {
		reqs[i], _ = b.RequestSignIn(t.Context(), start, email)
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = map[types.ID]bool{}
	)

	for _, req := range reqs {
		wg.Go(func() {
			u, _, _, err := b.SignIn(t.Context(), start, req.Pending, req.Code)
			if err != nil {
				t.Error(err)

				return
			}

			mu.Lock()
			ids[u.ID] = true
			mu.Unlock()
		})
	}

	wg.Wait()

	if all, _ := b.All(t.Context()); len(all) != 1 || len(ids) != 1 {
		t.Fatalf("%d users made, %d distinct IDs returned", len(all), len(ids))
	}
}

func TestBackupCodes(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "a@example.org")

	codes, err := b.IssueBackupCodes(t.Context(), start, u.ID)
	if err != nil || len(codes) != 10 {
		t.Fatalf("IssueBackupCodes = %v, %v", codes, err)
	}

	got, session, err := b.SignInWithBackupCode(t.Context(), start, u.Email, codes[3])
	if err != nil || got.ID != u.ID || session == "" {
		t.Fatalf("SignInWithBackupCode = %+v, %v", got, err)
	}

	if _, _, err := b.SignInWithBackupCode(t.Context(), start, u.Email, codes[3]); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a backup code worked twice: %v", err)
	}

	if left, _ := b.BackupCodesLeft(t.Context(), u.ID); left != 9 {
		t.Errorf("BackupCodesLeft = %d, want 9", left)
	}

	// Somebody else's address with this user's code.
	other := signUp(t, b, "b@example.org")
	if _, _, err := b.SignInWithBackupCode(t.Context(), start, other.Email, codes[4]); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("one user's code signed in another: %v", err)
	}

	// New codes retire the old ones.
	if _, err := b.IssueBackupCodes(t.Context(), start, u.ID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.SignInWithBackupCode(t.Context(), start, u.Email, codes[4]); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("an old code still works after new ones were issued: %v", err)
	}
}

const secret = "0123456789abcdef0123456789abcdef"

func TestBootstrapWorksOnce(t *testing.T) {
	b := newBus(t)
	email := addr(t, "first@example.org")

	for name, presented := range map[string]string{"wrong": "x" + secret[1:], "short": secret[1:], "empty": ""} {
		if _, _, err := b.Bootstrap(t.Context(), start, secret, presented, email); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("%s secret: %v", name, err)
		}
	}

	if spent, _ := b.BootstrapSpent(t.Context()); spent {
		t.Fatal("a refused secret spent the bootstrap")
	}

	u, session, err := b.Bootstrap(t.Context(), start, secret, secret, email)
	if err != nil || !u.SiteAdmin || session == "" {
		t.Fatalf("Bootstrap = %+v, %v", u, err)
	}

	if _, _, err := b.Bootstrap(t.Context(), start, secret, secret, addr(t, "second@example.org")); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("the bootstrap worked twice: %v", err)
	}

	if spent, _ := b.BootstrapSpent(t.Context()); !spent {
		t.Error("BootstrapSpent = false after it was spent")
	}
}

// No configured secret, no bootstrap -- including for an empty presented one.
func TestBootstrapNeedsAConfiguredSecret(t *testing.T) {
	b := newBus(t)

	if _, _, err := b.Bootstrap(t.Context(), start, "", "", addr(t, "a@example.org")); !errors.Is(err, userbus.ErrDenied) {
		t.Fatalf("Bootstrap with nothing configured: %v", err)
	}
}

// An existing user is made the administrator; no second account appears.
func TestBootstrapPromotesAnExistingUser(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "first@example.org")

	got, _, err := b.Bootstrap(t.Context(), start, secret, secret, u.Email)
	if err != nil || got.ID != u.ID || !got.SiteAdmin {
		t.Fatalf("Bootstrap = %+v, %v", got, err)
	}
}

func TestSessions(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "a@example.org")

	sessions := make([]string, 2)
	for i := range sessions {
		req, _ := b.RequestSignIn(t.Context(), start, u.Email)
		_, sessions[i], _, _ = b.SignIn(t.Context(), start, req.Pending, req.Code)
	}

	if _, err := b.Authenticate(t.Context(), start.Add(userbus.SessionLife), sessions[0]); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a session outlived SessionLife: %v", err)
	}

	if _, err := b.Authenticate(t.Context(), start.Add(userbus.SessionLife-time.Millisecond), sessions[0]); err != nil {
		t.Errorf("a session ended early: %v", err)
	}

	if err := b.SignOut(t.Context(), sessions[0]); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Authenticate(t.Context(), start, sessions[0]); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a signed-out session still works: %v", err)
	}

	if _, err := b.Authenticate(t.Context(), start, sessions[1]); err != nil {
		t.Errorf("signing one browser out signed out another: %v", err)
	}

	if err := b.SignOutEverywhere(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Authenticate(t.Context(), start, sessions[1]); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a session survived signing out everywhere: %v", err)
	}

	if err := b.SignOut(t.Context(), "garbage"); err != nil {
		t.Errorf("SignOut of garbage = %v; the caller wanted no session and has none", err)
	}
}

func TestSetProfile(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "a@example.org")

	got, err := b.SetProfile(t.Context(), start, u.ID, "  Ana Pérez  ", types.Spanish)
	if err != nil || got.Name != "Ana Pérez" || got.Lang != types.Spanish {
		t.Fatalf("SetProfile = %+v, %v", got, err)
	}

	if again, _ := b.ByID(t.Context(), u.ID); again.Name != "Ana Pérez" || again.Lang != types.Spanish || again.Named() != "Ana Pérez" {
		t.Errorf("stored = %+v", again)
	}

	if _, err := b.SetProfile(t.Context(), start, u.ID, "", "xx"); !errors.Is(err, userbus.ErrInvalid) {
		t.Errorf("an unknown language: %v", err)
	}

	long := make([]rune, userbus.MaxName+1)
	for i := range long {
		long[i] = 'é'
	}

	if _, err := b.SetProfile(t.Context(), start, u.ID, string(long), ""); !errors.Is(err, userbus.ErrInvalid) {
		t.Errorf("a name too long: %v", err)
	}
}

// disable is what the site administrator's screen does.
func disable(t *testing.T, b *userbus.Business, u userbus.User) {
	t.Helper()

	if _, err := b.SetEnabled(t.Context(), start, u.ID, false); err != nil {
		t.Fatal(err)
	}
}
