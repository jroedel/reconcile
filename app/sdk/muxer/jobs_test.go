package muxer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/foundation/jobs"
)

// held is a site whose jobs wait in line behind one that holds the runner
// until release is closed, and whose requests wait on a job only briefly:
// work that is longer than a request may last, without a test that is.
func held(t *testing.T, release <-chan struct{}) func(*Config) {
	t.Helper()

	runner := jobs.New(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := runner.Start("nobody", "hold", func(ctx context.Context, _ func(jobs.Progress)) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	return func(c *Config) {
		c.Jobs = runner
		c.JobWait = 20 * time.Millisecond
	}
}

// eventually asks until done says so, or gives up after a few seconds.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()

	for range 200 {
		if done() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s never came", what)
}

// Reading an inbox, from the page or through the API, is a job (issue
// #107): while it waits, the page says how far it has got and refreshes
// itself, and the API answers 202 with the job to ask after; once it is
// done, each shows what it always did.
func TestReadingThatOutlastsARequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	s := newTranslatingSite(t, held(t, release))
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Claude", "books-keep")

	if rec := s.send(keyFor(t, e.owner, "Gmail script", "upload"), "july.csv", july); rec.Code != http.StatusCreated {
		t.Fatalf("sending to the inbox: %d %s", rec.Code, rec.Body)
	}

	m := inboxList.FindStringSubmatch(e.owner.get("/imports").Body.String())
	if m == nil {
		t.Fatal("no list of the inbox")
	}

	// The page, while the reading waits.
	rec := e.owner.get(m[1])
	wantBody(t, rec, "Reading the files.", "This page refreshes by itself")

	again, ok := strings.CutPrefix(rec.Header().Get("Refresh"), "2; url=")
	if !ok || !strings.Contains(again, "job=") {
		t.Fatalf("the page does not ask again: %q", rec.Header().Get("Refresh"))
	}

	// The API, the same.
	rec = s.api(http.MethodGet, "/api/v1/inbox", key, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("list_inbox while reading: %d %s", rec.Code, rec.Body)
	}

	var working struct {
		Working bool
		Job     string
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &working); err != nil || !working.Working || working.Job == "" {
		t.Fatalf("list_inbox while reading: %s", rec.Body)
	}

	// Another tool's job, and one that is not there, are not this one's.
	for _, job := range []string{working.Job, "no-such-job"} {
		if rec := s.api(http.MethodPost, "/api/v1/inbox/import", key, `{"job": "`+job+`"}`); rec.Code != http.StatusNotFound {
			t.Errorf("import_from_inbox with job %s: %d %s", job, rec.Code, rec.Body)
		}
	}

	close(release)

	eventually(t, "the page's list", func() bool {
		return strings.Contains(e.owner.get(again).Body.String(), "Choose an account")
	})

	eventually(t, "list_inbox's answer", func() bool {
		rec := s.api(http.MethodGet, "/api/v1/inbox?job="+working.Job, key, "")

		return rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "july.csv")
	})

	// Somebody else's job is not there for them: the page reads its own
	// list again, which for them has nothing on it.
	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	if body := stranger.get(again).Body.String(); strings.Contains(body, "july.csv") {
		t.Error("a stranger sees the treasurer's reading")
	}

	if rec := s.api(http.MethodGet, "/api/v1/inbox?job="+working.Job, keyFor(t, stranger, "Claude", "books-read"), ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's key asking after the treasurer's job: %d", rec.Code)
	}
}
