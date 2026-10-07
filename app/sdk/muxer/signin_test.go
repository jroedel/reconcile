package muxer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// browser is one browser on the site: it keeps the cookies it is given and
// sends them back, as a phone would, from an address of its own so that the
// sign-in throttle counts it apart from every other.
type browser struct {
	t       *testing.T
	h       http.Handler
	addr    string
	cookies map[string]string
}

var browsers atomic.Int32

func newBrowser(t *testing.T, h http.Handler) *browser {
	n := browsers.Add(1)

	return &browser{t: t, h: h, addr: fmt.Sprintf("198.51.100.%d:4000", n%250+1), cookies: map[string]string{}}
}

func (b *browser) do(r *http.Request) *httptest.ResponseRecorder {
	b.t.Helper()

	r.RemoteAddr = b.addr

	for name, value := range b.cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, r)

	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}

	return rec
}

func (b *browser) get(path string) *httptest.ResponseRecorder {
	b.t.Helper()

	return b.do(httptest.NewRequest(http.MethodGet, path, nil))
}

// post sends a form as one of the site's own pages would.
func (b *browser) post(path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()

	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")

	return b.do(r)
}

var sixDigits = regexp.MustCompile(`(?m)^\s+(\d{6})\s*$`)

// codeIn is the code in the newest message, and who it went to.
func codeIn(t *testing.T, sent *mail.Recorder) (string, string) {
	t.Helper()

	m, ok := sent.Last()
	if !ok {
		t.Fatal("no message was sent")
	}

	match := sixDigits.FindStringSubmatch(m.Text)
	if match == nil {
		t.Fatalf("no code on a line of its own in:\n%s", m.Text)
	}

	return match[1], m.To
}

func wantRedirect(t *testing.T, rec *httptest.ResponseRecorder, to string) {
	t.Helper()

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != to {
		t.Fatalf("status %d to %q, want a redirect to %q\n%s", rec.Code, rec.Header().Get("Location"), to, rec.Body.String())
	}
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, want ...string) {
	t.Helper()

	for _, w := range want {
		if !strings.Contains(rec.Body.String(), w) {
			t.Errorf("the page lacks %q:\n%s", w, rec.Body.String())
		}
	}
}

// signUp takes a new browser from the front page to signed in.
func signUp(t *testing.T, h http.Handler, sent *mail.Recorder, email string) *browser {
	t.Helper()

	b := newBrowser(t, h)

	wantRedirect(t, b.post("/sign-in", url.Values{"email": {email}}), "/sign-in/code")
	wantBody(t, b.get("/sign-in/code"), email)

	code, to := codeIn(t, sent)
	if to != email {
		t.Fatalf("the code went to %s", to)
	}

	wantRedirect(t, b.post("/sign-in/code", url.Values{"code": {code}}), "/account?welcome=1")

	return b
}

func TestSignUpThenSignInAgain(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)

	b := signUp(t, h, sent, "treasurer@example.org")
	wantBody(t, b.get("/account"), "treasurer@example.org", "You have no backup codes")
	wantBody(t, b.get("/"), `href="/account"`)

	// The message is in the language the page was, and names the site.
	if m, _ := sent.Last(); !strings.Contains(m.Text, base) || m.Subject != "Your Reconcile sign-in code" {
		t.Errorf("the message: %q\n%s", m.Subject, m.Text)
	}

	wantRedirect(t, b.post("/sign-out", nil), "/")
	wantRedirect(t, b.get("/account"), "/sign-in?next=%2Faccount")

	// Coming back goes where it was going, not to the welcome.
	b.post("/sign-in", url.Values{"email": {"treasurer@example.org"}, "next": {"/account"}})
	code, _ := codeIn(t, sent)
	wantRedirect(t, b.post("/sign-in/code", url.Values{"code": {code}, "next": {"/account"}}), "/account")
}

// Every address gets the same page, and a bad one is the only complaint.
func TestAskingForACodeSaysTheSameForEveryAddress(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	signUp(t, h, sent, "known@example.org")

	for _, email := range []string{"known@example.org", "stranger@example.org"} {
		wantRedirect(t, newBrowser(t, h).post("/sign-in", url.Values{"email": {email}}), "/sign-in/code")
	}

	rec := newBrowser(t, h).post("/sign-in", url.Values{"email": {"not an address"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a malformed address: %d", rec.Code)
	}

	wantBody(t, rec, "does not look like an email address", `value="not an address"`)
}

func TestAWrongCodeIsRefusedAndTheRightOneStillWorks(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	b := newBrowser(t, h)

	b.post("/sign-in", url.Values{"email": {"a@example.org"}})
	code, _ := codeIn(t, sent)

	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}

	rec := b.post("/sign-in/code", url.Values{"code": {wrong}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong code: %d", rec.Code)
	}

	wantBody(t, rec, "That code did not work")

	wantBody(t, b.post("/sign-in/code", url.Values{"code": {"12"}}), "The code is six numbers")

	wantRedirect(t, b.post("/sign-in/code", url.Values{"code": {code}}), "/account?welcome=1")
}

// The code typed into a browser that did not ask: the cookie is missing, and
// the page says so rather than "wrong code".
func TestACodeTypedInAnotherBrowser(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)

	newBrowser(t, h).post("/sign-in", url.Values{"email": {"a@example.org"}})
	code, _ := codeIn(t, sent)

	other := newBrowser(t, h)
	wantRedirect(t, other.get("/sign-in/code"), "/sign-in")
	wantBody(t, other.post("/sign-in/code", url.Values{"code": {code}}), "asked for on another device")
}

func TestProfileAndLanguage(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	b := signUp(t, h, sent, "a@example.org")

	wantRedirect(t, b.post("/account/profile", url.Values{"name": {"Ana Pérez"}, "lang": {"pt"}}), "/account?done=profile")

	rec := b.get("/account?done=profile")
	wantBody(t, rec, `<html lang="pt">`, `value="Ana Pérez"`, `<option value="pt" lang="pt" selected>`)

	// The header names them now.
	wantBody(t, b.get("/"), "Ana Pérez")

	// "The same as this device" clears the cookie, and the browser's own
	// language comes back... unless the account says otherwise, which it no
	// longer does.
	b.post("/account/profile", url.Values{"name": {"Ana Pérez"}, "lang": {""}})

	if _, ok := b.cookies["lang"]; ok {
		t.Error("following the device left a language cookie behind")
	}

	if rec := b.post("/account/profile", url.Values{"name": {strings.Repeat("x", 101)}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a name too long: %d", rec.Code)
	}
}

func TestBackupCodesSignIn(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	b := signUp(t, h, sent, "a@example.org")

	rec := b.post("/account/backup-codes", nil)
	codes := regexp.MustCompile(`<code>([A-Z0-9-]+)</code>`).FindAllStringSubmatch(rec.Body.String(), -1)

	if len(codes) != 10 {
		t.Fatalf("%d codes shown:\n%s", len(codes), rec.Body.String())
	}

	wantBody(t, b.get("/account"), "You have 10 unused backup codes")

	other := newBrowser(t, h)
	wantRedirect(t, other.post("/sign-in/backup", url.Values{"email": {"a@example.org"}, "code": {codes[0][1]}}), "/")

	if rec := newBrowser(t, h).post("/sign-in/backup", url.Values{"email": {"a@example.org"}, "code": {codes[0][1]}}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a backup code worked twice: %d", rec.Code)
	}
}

func TestEmailChangeThroughTheAccountPage(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	b := signUp(t, h, sent, "old@example.org")
	signUp(t, h, sent, "taken@example.org")

	wantBody(t, b.post("/account/email", url.Values{"email": {"taken@example.org"}}), "Another account already signs in with that address")

	wantRedirect(t, b.post("/account/email", url.Values{"email": {"new@example.org"}}), "/account#email")

	code, to := codeIn(t, sent)
	if to != "new@example.org" {
		t.Fatalf("the code went to %s, not the new address", to)
	}

	wantBody(t, b.get("/account"), "We sent a code to new@example.org")
	wantRedirect(t, b.post("/account/email/confirm", url.Values{"code": {code}}), "/account?done=email")

	// The address left behind is told.
	if m, _ := sent.Last(); m.To != "old@example.org" || !strings.Contains(m.Text, "new@example.org") {
		t.Errorf("the notice went to %s:\n%s", m.To, m.Text)
	}

	wantBody(t, b.get("/account"), "You sign in as new@example.org")
}

// The setup secret makes the first administrator once, and the page says so
// afterwards rather than collecting a secret it will refuse.
func TestTheFirstSignIn(t *testing.T) {
	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { c.Bootstrap = secret })

	wantBody(t, newBrowser(t, h).get("/sign-in"), `href="/sign-in/first"`)

	b := newBrowser(t, h)
	if rec := b.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {"wrong"}}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong secret: %d", rec.Code)
	}

	wantRedirect(t, b.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}}), "/account?welcome=1")
	wantBody(t, newBrowser(t, h).get("/sign-in/first"), "already been set up")

	if len(sent.Sent) != 0 {
		t.Error("the first sign-in sent mail; it exists for before mail works")
	}

	// Without a secret configured the page is not there at all.
	h, _ = newSite(t, sqldb.Infrastructure, nil)
	if rec := newBrowser(t, h).get("/sign-in/first"); rec.Code != http.StatusNotFound {
		t.Errorf("with no secret: %d", rec.Code)
	}
}

// A form posted from another site is refused before anything reads it, and a
// body that is not a form is refused too.
func TestWritesMustComeFromThisSitesForms(t *testing.T) {
	h, _ := newSite(t, sqldb.Infrastructure, nil)

	r := httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader("email=a%40example.org"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	if rec := newBrowser(t, h).do(r); rec.Code != http.StatusForbidden {
		t.Errorf("a cross-site post: %d", rec.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader(`{"email":"a@example.org"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")

	if rec := newBrowser(t, h).do(r); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a JSON post: %d", rec.Code)
	}
}

// Asking again and again from one address is throttled, with a Retry-After.
func TestAskingTooOftenIsThrottled(t *testing.T) {
	h, _ := newSite(t, sqldb.Infrastructure, nil)
	b := newBrowser(t, h)

	var last *httptest.ResponseRecorder
	for range 6 {
		last = b.post("/sign-in", url.Values{"email": {"a@example.org"}})
	}

	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" {
		t.Fatalf("the sixth request: %d, Retry-After %q", last.Code, last.Header().Get("Retry-After"))
	}
}

// With no public address there is no sign-in at all.
func TestSignInIsOffWithoutABaseURL(t *testing.T) {
	h, _ := newSite(t, sqldb.Infrastructure, func(c *Config) { c.BaseURL = "" })

	if rec := newBrowser(t, h).get("/sign-in"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /sign-in: %d", rec.Code)
	}
}
