package muxer

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/translation/stores/translationdb"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

func newHandlerWanting(t *testing.T, want sqldb.Expected) http.Handler {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := translationdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	translations := translationbus.NewBusiness(translationdb.NewStore(db), nil)

	render, err := page.NewRenderer(log, translations, Templates()...)
	if err != nil {
		t.Fatal(err)
	}

	// As main does: every app's strings register, which is the test that
	// every template in the binary can be read.
	if err := translations.Register(t.Context(), render.Strings()); err != nil {
		t.Fatal(err)
	}

	h, err := New(Config{Log: log, DB: db, Expected: want, Render: render})
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()

	return newHandlerWanting(t, sqldb.Infrastructure)
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// deploy/deploy.sh tells the app's answer from Apache's by this header, and it
// asks for "/", which has no route yet. So the policy has to be on a 404 too,
// and its first directive has to be exactly this.
func TestEveryAnswerCarriesThePolicyTheDeployLooksFor(t *testing.T) {
	for _, path := range []string{"/healthz", "/", "/no-such-page"} {
		rec := httptest.NewRecorder()
		newHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'none'") {
			t.Errorf("%s: Content-Security-Policy = %q", path, csp)
		}

		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", path, got)
		}
	}
}

// A binary rolled back onto a database it does not understand must say so,
// because the deploy decides whether to keep a release on this answer.
func TestHealthzIsUnhealthyOnASchemaItDoesNotKnow(t *testing.T) {
	rec := httptest.NewRecorder()
	h := newHandlerWanting(t, sqldb.Expected{"accounts": {"id"}})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	if strings.Contains(rec.Body.String(), "accounts") {
		t.Fatalf("the public answer names the table: %q", rec.Body.String())
	}
}

func get(t *testing.T, h http.Handler, path string, header ...string) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	return rec
}

func TestFrontPage(t *testing.T) {
	h := newHandler(t)

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<h1>") {
		t.Fatalf("status %d:\n%s", rec.Code, rec.Body.String())
	}

	// The stylesheet it links is the one served.
	start := strings.Index(rec.Body.String(), `href="/static/app.`)
	if start < 0 {
		t.Fatal("the front page links no stylesheet")
	}

	href := rec.Body.String()[start+len(`href="`):]
	href = href[:strings.Index(href, `"`)]

	if css := get(t, h, href); css.Code != http.StatusOK || css.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("%s: status %d, %s", href, css.Code, css.Header().Get("Content-Type"))
	}

	// A phone set to Portuguese gets a page that says it is Portuguese.
	pt := get(t, h, "/", "Accept-Language", "pt-BR,pt;q=0.9")
	if !strings.Contains(pt.Body.String(), `<html lang="pt">`) {
		t.Error("Accept-Language did not choose the language")
	}

	// And ?lang= is remembered and the address cleaned.
	if sw := get(t, h, "/?lang=es"); sw.Code != http.StatusSeeOther || sw.Header().Get("Location") != "/" {
		t.Errorf("?lang=es: status %d, Location %q", sw.Code, sw.Header().Get("Location"))
	}
}

// "/" is the front page and nothing else is.
func TestUnknownPathIsNotTheFrontPage(t *testing.T) {
	if rec := get(t, newHandler(t), "/accounts"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestNewNeedsALoggerAndADatabase(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with nothing succeeded")
	}
}
