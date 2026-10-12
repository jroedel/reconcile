package muxer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// sendReceipt posts a receipt's file to the API as curl on the person's
// computer does: the file itself as the body, where it goes and what is
// known about it in the query, and a key.
func (s translatingSite) sendReceipt(key string, q url.Values, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/receipts?"+q.Encode(), strings.NewReader(body))
	r.Header.Set("Content-Type", "image/jpeg")

	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)

	return rec
}

// sentReceipt is sendReceipt's answer, failing the test unless it is the
// status and outcome wanted.
func (k kept) sentReceipt(t *testing.T, q url.Values, body string, status int, outcome string) map[string]any {
	t.Helper()

	rec := k.s.sendReceipt(k.key, q, body)
	if rec.Code != status {
		t.Fatalf("sending %v: %d, want %d\n%s", q, rec.Code, status, rec.Body)
	}

	var out struct {
		Outcome string         `json:"outcome"`
		Receipt map[string]any `json:"receipt"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("sending %v: %v", q, err)
	}

	if out.Outcome != outcome {
		t.Fatalf("sending %v: %q, want %q", q, out.Outcome, outcome)
	}

	return out.Receipt
}

// attachedTo is the transactions a receipt in an answer is on.
func attachedTo(rc map[string]any) []string {
	var out []string
	for _, l := range rc["attached_to"].([]any) {
		out = append(out, l.(map[string]any)["id"].(string))
	}

	return out
}

// A treasurer's Claude on their computer finds receipts in their folders
// and sends them with curl: into the account's or the project's inbox,
// with what it read off them, or onto the charge each justifies. Sending
// one again adds nothing and answers with the receipt it made, which is
// how Claude learns that a receipt waiting in the inbox is the one to
// attach.
func TestAReceiptSentFromTheComputer(t *testing.T) {
	t.Parallel()

	k := newKept(t)
	s := k.s
	project := strings.TrimPrefix(k.e.project, "/projects/")
	grocery := k.txByDescription(t, "2026-07")["CORNER GROCERY"]["id"].(string)

	// The index has it, as a route and no tool: a file does not pass
	// through a conversation.
	var idx struct {
		Endpoints []struct{ Method, Path, Scope, Tool string }
	}

	if err := json.Unmarshal(s.api(http.MethodGet, "/api/v1", "", "").Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}

	listed := false
	for _, e := range idx.Endpoints {
		if e.Method == http.MethodPost && e.Path == "/api/v1/receipts" {
			listed = e.Scope == "books:write" && e.Tool == ""
		}
	}

	if !listed {
		t.Error("the index does not list POST /api/v1/receipts for books:write, as no tool")
	}

	// Into the account's inbox, with what is on it: the details are kept,
	// and it waits.
	rc := k.sentReceipt(t, url.Values{
		"account": {k.account}, "name": {"hardware.jpg"},
		"spent_on": {"2026-07-14"}, "amount": {"18.40"}, "merchant": {"Hilltop Hardware"}, "note": {"Hall door hinges"},
	}, photoOf("hinges"), http.StatusCreated, "received")

	if rc["amount"] != "18.40" || rc["spent_on"] != "2026-07-14" || rc["merchant"] != "Hilltop Hardware" || len(attachedTo(rc)) != 0 {
		t.Errorf("into the inbox: %v", rc)
	}

	hinges := rc["id"].(string)

	// The same bytes again, under another name: the same receipt.
	if again := k.sentReceipt(t, url.Values{"account": {k.account}, "name": {"IMG_2231.jpg"}}, photoOf("hinges"), http.StatusOK, "already_there"); again["id"] != hinges {
		t.Errorf("sent again: %v, want %s", again["id"], hinges)
	}

	// The photo a pilgrim already put in the project's inbox, found on the
	// computer too: the receipt there is the answer.
	if there := k.sentReceipt(t, url.Values{"project": {project}, "name": {"one.jpg"}}, photoOf("one"), http.StatusOK, "already_there"); there["id"] != k.receipt {
		t.Errorf("one the project holds: %v, want %s", there["id"], k.receipt)
	}

	// Onto the charge it justifies: attached at once, and once.
	on := k.sentReceipt(t, url.Values{"transaction": {grocery}, "name": {"grocery.pdf"}}, photoOf("grocery"), http.StatusCreated, "received")
	if got := attachedTo(on); len(got) != 1 || got[0] != grocery {
		t.Errorf("onto the charge: attached to %v, want %s", got, grocery)
	}

	if again := k.sentReceipt(t, url.Values{"transaction": {grocery}, "name": {"grocery.pdf"}}, photoOf("grocery"), http.StatusOK, "already_there"); again["id"] != on["id"] {
		t.Errorf("onto the charge again: %v, want %v", again["id"], on["id"])
	}

	// One waiting in the account's inbox, sent onto a charge of the
	// account: the waiting receipt, not a second one, to attach.
	if waiting := k.sentReceipt(t, url.Values{"transaction": {grocery}, "name": {"hardware.jpg"}}, photoOf("hinges"), http.StatusOK, "already_there"); waiting["id"] != hinges || len(attachedTo(waiting)) != 0 {
		t.Errorf("one waiting, sent onto a charge: %v", waiting)
	}

	tx := s.get(t, "/api/v1/transactions/"+grocery, k.key)
	if n := len(list(t, tx, "receipts")); n != 1 {
		t.Errorf("the charge has %d receipts, want 1: %v", n, tx)
	}

	// What will not do keeps nothing, and says what to fix.
	for _, bad := range []struct {
		q      url.Values
		body   string
		status int
	}{
		{url.Values{"name": {"x.jpg"}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {k.account}, "project": {project}, "name": {"x.jpg"}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {k.account}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {"not-an-id"}, "name": {"x.jpg"}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {k.account}, "name": {"x.jpg"}, "amount": {"-18.40"}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {k.account}, "name": {"x.jpg"}, "spent_on": {"14 July"}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {k.account}, "name": {"x.jpg"}, "merchant": {strings.Repeat("m", 101)}}, photoOf("x"), http.StatusBadRequest},
		{url.Values{"account": {k.account}, "name": {"notes.txt"}}, "these are not a receipt", http.StatusUnsupportedMediaType},
		{url.Values{"account": {k.account}, "name": {"empty.jpg"}}, "", http.StatusUnsupportedMediaType},
		{url.Values{"account": {k.account}, "name": {"huge.jpg"}}, photoOf(strings.Repeat("x", 21<<20)), http.StatusRequestEntityTooLarge},
	} {
		if rec := s.sendReceipt(k.key, bad.q, bad.body); rec.Code != bad.status {
			t.Errorf("%v: %d, want %d\n%s", bad.q, rec.Code, bad.status, rec.Body)
		}
	}

	if n := len(list(t, s.get(t, "/api/v1/receipts/waiting", k.key), "receipts")); n != 2 {
		t.Errorf("%d receipts waiting, want the pilgrim's and the hinges", n)
	}

	// It is in the history as sent through the key.
	sent := 0
	for _, c := range list(t, s.get(t, "/api/v1/changes", k.key), "changes") {
		if field(c, "action") == "receipt.added" && field(c, "via") == "Claude" {
			sent++
		}
	}

	if sent != 2 {
		t.Errorf("%d receipts added through the key in the history, want 2", sent)
	}
}

// Only a key that keeps the books sends a receipt, and only into what its
// person may add receipts to: a stranger's is told the treasurer's
// account, project and charge are not there, and nothing is added.
func TestNobodySendsReceiptsWhereTheyWereNotGiven(t *testing.T) {
	t.Parallel()

	k := newKept(t)
	s := k.s
	project := strings.TrimPrefix(k.e.project, "/projects/")
	grocery := k.txByDescription(t, "2026-07")["CORNER GROCERY"]["id"].(string)
	into := url.Values{"account": {k.account}, "name": {"x.jpg"}}

	if rec := s.sendReceipt("", into, photoOf("x")); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", rec.Code)
	}

	for name, key := range map[string]string{
		"a reading key":     keyFor(t, k.e.owner, "laptop", "books-read"),
		"an upload key":     keyFor(t, k.e.owner, "Gmail script", "upload"),
		"a translating key": makeKey(t, s.admin, "laptop"),
	} {
		if rec := s.sendReceipt(key, into, photoOf("x")); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}

	// A cookie is no key, even the treasurer's.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/receipts?"+into.Encode(), strings.NewReader(photoOf("x")))
	for name, value := range k.e.owner.cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the treasurer's cookie: %d", rec.Code)
	}

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	theirs := keyFor(t, stranger, "Claude", "books-keep")

	for _, where := range []url.Values{
		{"account": {k.account}}, {"project": {project}}, {"transaction": {grocery}},
	} {
		where.Set("name", "x.jpg")

		// The photo the pilgrim sent is in the project already: a
		// stranger is not told so.
		if rec := s.sendReceipt(theirs, where, photoOf("one")); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger, %v: %d, want 404\n%s", where, rec.Code, rec.Body)
		}
	}

	if n := len(list(t, s.get(t, "/api/v1/receipts/waiting", k.key), "receipts")); n != 1 {
		t.Errorf("%d receipts waiting, want only the pilgrim's", n)
	}

	if n := len(list(t, s.get(t, "/api/v1/transactions/"+grocery, k.key), "receipts")); n != 0 {
		t.Errorf("a stranger put %d receipts on the treasurer's charge", n)
	}
}
