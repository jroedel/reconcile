package muxer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Questions through a key (docs/accountant.md, 6): the treasurer's Claude
// asks one about a transaction, which reads back with its link and is
// marked with the key; the accountant's Claude reads it and replies, which
// answers it, and may not mark it; the lists of one thing and of an
// organization find it; a viewer's key reads and writes nothing; a
// stranger's finds nothing; and each is a tool on /mcp for the keys that
// may call it. Everything invented.
func TestQuestionsThroughTheAPI(t *testing.T) {
	t.Parallel()

	b := newBooked(t)
	s, read := b.s, b.key
	keep := keyFor(t, b.e.owner, "Claude", "books-keep")
	orgID := strings.TrimPrefix(b.e.org, "/orgs/")

	keys := map[string]string{}

	for _, p := range []struct{ email, role string }{{"accountant@example.org", "accountant"}, {"viewer@example.org", "viewer"}} {
		wantRedirect(t, b.e.owner.post(b.e.org+"/people", url.Values{"email": {p.email}, "role": {p.role}}), b.e.org+"?done=granted")
		keys[p.role] = keyFor(t, signUp(t, s.h, s.sent, p.email), "Claude", "books-keep")
	}

	keys["stranger"] = keyFor(t, signUp(t, s.h, s.sent, "stranger@example.org"), "Claude", "books-keep")

	ask := map[string]string{"about": "transaction", "id": b.first, "text": "What was the opening deposit?\nThe statement does not say."}

	// A key that only reads asks nothing.
	if rec := s.api(http.MethodPost, "/api/v1/questions", read, `{"about": "transaction", "id": "`+b.first+`", "text": "?"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a reading key asking: %d", rec.Code)
	}

	asked := s.change(t, http.MethodPost, "/api/v1/questions", keep, ask, http.StatusOK)
	q := asked["question"]
	id := field(q, "id").(string)

	for path, want := range map[string]any{
		"state": "open", "text": ask["text"], "asked_by": "owner@example.org", "asked_by_you": true, "through": "Claude",
		"url": base + "/questions/" + id,
	} {
		if got := field(q, path); got != want {
			t.Errorf("the question's %s: %v, want %v", path, got, want)
		}
	}

	if field(q, "about", "kind") != "transaction" || field(q, "about", "url") != base+"/transactions/"+b.first || field(q, "about", "what") == "" {
		t.Errorf("what it is about: %v", field(q, "about"))
	}

	wantBody(t, b.e.owner.get("/questions/"+id), "What was the opening deposit?", "through Claude")

	// One thing's list, through the key that reads.
	on := s.get(t, "/api/v1/questions?about=transaction&id="+b.first, read)
	if l := list(t, on, "questions"); len(l) != 1 || field(l[0], "id") != id || on["can_ask"] != true {
		t.Errorf("the transaction's questions: %v", on)
	}

	// The accountant's Claude reads it, may reply and not mark it, and its
	// reply answers it.
	thread := s.get(t, "/api/v1/questions/"+id, keys["accountant"])
	if thread["can_reply"] != true || thread["can_mark_answered"] != false || field(thread, "question", "asked_by_you") != nil {
		t.Errorf("the thread, to the accountant: %v", thread)
	}

	if rec := s.api(http.MethodPost, "/api/v1/questions/"+id+"/answered", keys["accountant"], ""); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "Only the person who asked") {
		t.Errorf("the accountant marking it: %d %s", rec.Code, rec.Body)
	}

	replied := s.change(t, http.MethodPost, "/api/v1/questions/"+id+"/replies", keys["accountant"], map[string]string{"text": "Last year's balance, carried over."}, http.StatusOK)
	reply := field(replied, "question", "replies").([]any)

	if field(replied, "question", "state") != "answered" || len(reply) != 1 || field(reply[0], "by_you") != true || field(reply[0], "through") != "Claude" ||
		field(replied, "question", "answered_at") == nil {
		t.Errorf("replied: %v", replied)
	}

	// An organization's list, by state and by who asked.
	for query, want := range map[string]int{"": 1, "&state=answered": 1, "&state=open": 0, "&mine=true": 1} {
		if l := list(t, s.get(t, "/api/v1/questions?organization="+orgID+query, read), "questions"); len(l) != want {
			t.Errorf("the organization's%s: %d, want %d", query, len(l), want)
		}
	}

	if l := list(t, s.get(t, "/api/v1/questions?organization="+orgID+"&mine=true", keys["accountant"]), "questions"); len(l) != 0 {
		t.Errorf("the accountant's own: %v", l)
	}

	// The asker marks it, through their key.
	if got := s.change(t, http.MethodPost, "/api/v1/questions/"+id+"/answered", keep, nil, http.StatusOK); field(got, "question", "state") != "answered" {
		t.Errorf("marked: %v", got)
	}

	// A viewer's key reads, and asks and replies nothing.
	if l := list(t, s.get(t, "/api/v1/questions?about=transaction&id="+b.first, keys["viewer"]), "questions"); len(l) != 1 {
		t.Errorf("the viewer reads: %v", l)
	}

	for _, w := range []struct{ path, body string }{
		{"/api/v1/questions", `{"about": "transaction", "id": "` + b.first + `", "text": "Mine?"}`},
		{"/api/v1/questions/" + id + "/replies", `{"text": "Mine?"}`},
	} {
		if rec := s.api(http.MethodPost, w.path, keys["viewer"], w.body); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "a viewer reads only") {
			t.Errorf("the viewer, %s: %d %s", w.path, rec.Code, rec.Body)
		}
	}

	// A stranger's key finds nothing, whichever way it asks.
	for _, path := range []string{"/api/v1/questions/" + id, "/api/v1/questions?about=transaction&id=" + b.first, "/api/v1/questions?organization=" + orgID} {
		if rec := s.api(http.MethodGet, path, keys["stranger"], ""); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger, %s: %d", path, rec.Code)
		}
	}

	for _, w := range []struct{ path, body string }{
		{"/api/v1/questions", `{"about": "transaction", "id": "` + b.first + `", "text": "Whose?"}`},
		{"/api/v1/questions/" + id + "/replies", `{"text": "Whose?"}`},
		{"/api/v1/questions/" + id + "/answered", ``},
	} {
		if rec := s.api(http.MethodPost, w.path, keys["stranger"], w.body); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger, %s: %d", w.path, rec.Code)
		}
	}

	// What will not do is said.
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"about": "account", "id": "` + b.account + `", "text": "?"}`, http.StatusBadRequest},
		{`{"about": "transaction", "id": "nonsense", "text": "?"}`, http.StatusBadRequest},
		{`{"about": "transaction", "id": "` + b.first + `", "text": "  "}`, http.StatusUnprocessableEntity},
	} {
		if rec := s.api(http.MethodPost, "/api/v1/questions", keep, c.body); rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.body, rec.Code, c.want)
		}
	}

	if rec := s.api(http.MethodGet, "/api/v1/questions", read, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a list of nothing: %d", rec.Code)
	}

	// Each change is in its person's history, through the key: the
	// question asked, and the reply that answered it.
	for key, want := range map[string]string{keep: "question.asked", keys["accountant"]: "question.answered"} {
		var actions []string
		for _, c := range list(t, s.get(t, "/api/v1/changes", key), "changes") {
			actions = append(actions, field(c, "action").(string))
		}

		if !slices.Contains(actions, want) {
			t.Errorf("no %s among the changes: %v", want, actions)
		}
	}

	// The tools: the reading ones for a key that reads, all of them for
	// one that keeps.
	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	tools := func(key string) []string {
		t.Helper()

		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{key}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()

		res, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}

		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}

		return names
	}

	reading, keeping := tools(read), tools(keep)

	for _, name := range []string{"list_questions", "get_question", "get_weekly_email_settings"} {
		if !slices.Contains(reading, name) {
			t.Errorf("a reading key has no %s", name)
		}
	}

	for _, name := range []string{"ask_question", "reply_to_question", "mark_question_answered"} {
		if slices.Contains(reading, name) || !slices.Contains(keeping, name) {
			t.Errorf("%s: reading %v, keeping %v", name, slices.Contains(reading, name), slices.Contains(keeping, name))
		}
	}
}

// The key's own person's weekly email settings, read only: the defaults,
// then what they saved on their profile.
func TestTheWeeklyEmailSettingsThroughTheAPI(t *testing.T) {
	t.Parallel()

	b := newBooked(t)

	got := b.s.get(t, "/api/v1/me/weekly-email", b.key)
	for path, want := range map[string]any{"enabled": true, "weekday": "Monday", "hour": 8.0, "time_zone": "UTC", "url": base + "/account#weekly"} {
		if got[path] != want {
			t.Errorf("the defaults' %s: %v, want %v", path, got[path], want)
		}
	}

	if next, _ := got["next"].(string); !strings.HasSuffix(next, "T08:00:00Z") {
		t.Errorf("the next one: %v", got["next"])
	}

	wantRedirect(t, b.e.owner.post("/account/weekly", url.Values{"weekday": {"5"}, "hour": {"17"}, "tz": {"America/Sao_Paulo"}}), "/account?done=weekly#weekly")

	got = b.s.get(t, "/api/v1/me/weekly-email", b.key)
	if got["enabled"] != false || got["weekday"] != "Friday" || got["hour"] != 17.0 || got["time_zone"] != "America/Sao_Paulo" || got["next"] != nil {
		t.Errorf("saved: %v", got)
	}
}
