package muxer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// send posts a statement file to the inbox as the Gmail script does: the
// file itself as the body, a key, and no browser.
func send(h http.Handler, key, name, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/inbox?"+url.Values{"name": {name}, "source": {"gmail:abc"}}.Encode(), strings.NewReader(body))
	r.Header.Set("Content-Type", "text/csv")

	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	return rec
}

func (s translatingSite) send(key, name, body string) *httptest.ResponseRecorder {
	return send(s.h, key, name, body)
}

// inboxed sends a statement to somebody's inbox with an upload key of
// theirs, and returns the address that puts it aside, for the stranger walk.
func inboxed(t *testing.T, h http.Handler, b *browser) string {
	t.Helper()

	if rec := send(h, keyFor(t, b, "Gmail script", "upload"), "july.csv", july); rec.Code != http.StatusCreated {
		t.Fatalf("sending to the inbox: %d %s", rec.Code, rec.Body)
	}

	d := inboxDismiss.FindStringSubmatch(b.get("/imports").Body.String())
	if d == nil {
		t.Fatal("no put-aside button on the imports page")
	}

	return d[1]
}

var (
	inboxList    = regexp.MustCompile(`href="(/imports\?f=[^"]+)"`)
	inboxDismiss = regexp.MustCompile(`action="(/imports/inbox/[^/"]+/dismiss)"`)
)

// A treasurer's Gmail script sends a statement with a key that may only
// upload: it arrives once however often it is sent, waits on the imports
// page as a list to check, and can be put aside -- by the treasurer, and by
// nobody else. The key reads nothing, and no other key or cookie sends.
func TestAStatementSentToTheInbox(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Gmail script", "upload")

	wantBody(t, e.owner.get("/account/keys"), "Gmail script", "sends statements to your inbox")

	// The index says which scope it needs, and it is no tool: Claude
	// does not send files.
	wantBody(t, s.api(http.MethodGet, "/api/v1", "", ""), `"path": "/api/v1/inbox"`, `"scope": "upload"`)

	// Nobody, a cookie, and a key for something else may not send.
	if rec := s.send("", "july.csv", july); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", rec.Code)
	}

	if rec := s.send(makeKey(t, s.admin, "laptop"), "july.csv", july); rec.Code != http.StatusForbidden {
		t.Errorf("a translating key: %d %s", rec.Code, rec.Body)
	}

	// The upload key sends, once.
	rec := s.send(key, "july.csv", july)
	wantBody(t, rec, `"outcome": "received"`)

	if rec.Code != http.StatusCreated {
		t.Errorf("sending: %d", rec.Code)
	}

	wantBody(t, s.send(key, "july again.csv", july), `"outcome": "already_waiting"`)

	for _, bad := range []struct{ name, body string }{{"", july}, {"empty.csv", ""}} {
		if rec := s.send(key, bad.name, bad.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", bad.name, rec.Code)
		}
	}

	// It reads nothing.
	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", key, ""); rec.Code != http.StatusForbidden {
		t.Errorf("the upload key reading: %d", rec.Code)
	}

	// It waits on the imports page, as a list like any upload's.
	page := e.owner.get("/imports")
	wantBody(t, page, "From your email", "july.csv", "Check them and import")

	m := inboxList.FindStringSubmatch(page.Body.String())
	if m == nil {
		t.Fatalf("no list to check:\n%s", page.Body)
	}

	wantBody(t, e.owner.get(m[1]), "july.csv", "Choose an account")

	// Only the treasurer may put it aside.
	d := inboxDismiss.FindStringSubmatch(page.Body.String())
	if d == nil {
		t.Fatalf("no put-aside button:\n%s", page.Body)
	}

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	if rec := stranger.post(d[1], nil); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger putting it aside: %d", rec.Code)
	}

	if strings.Contains(stranger.get("/imports").Body.String(), "july.csv") {
		t.Error("a stranger sees the treasurer's inbox")
	}

	wantRedirect(t, e.owner.post(d[1], nil), "/imports?done=dismissed")

	if body := e.owner.get("/imports").Body.String(); strings.Contains(body, "From your email") {
		t.Error("put aside, it is still waiting")
	}
}

// On /mcp a connection lists the tools its key may call: a key that may
// only upload lists none, since sending a file is no tool, and is told
// nothing it could not do.
func TestAnUploadKeyListsNoTools(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	key := keyFor(t, s.admin, "Gmail script", "upload")

	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)

	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(tools.Tools) != 0 {
		t.Errorf("an upload key's tools: %d", len(tools.Tools))
	}
}
