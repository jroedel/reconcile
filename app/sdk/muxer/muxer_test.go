package muxer

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()

	h, err := New(Config{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}

	return h
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

func TestNewNeedsALogger(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with no logger succeeded")
	}
}
