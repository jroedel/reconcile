package muxer

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

	h, err := New(Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: db, Expected: want})
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

func TestNewNeedsALoggerAndADatabase(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with nothing succeeded")
	}
}
