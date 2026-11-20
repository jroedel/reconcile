package muxer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A key for printing checks is made on the keys screen and listed as one;
// it reads nothing of the books, and Claude's tools are none of its.
func TestAChecksKeyReadsNoBooks(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Office printer", "checks")

	wantBody(t, e.owner.get("/account/keys"), "Office printer", "prints checks")

	for _, path := range []string{"/api/v1/me", "/api/v1/overview", "/api/v1/receipts/waiting"} {
		if rec := s.api(http.MethodGet, path, key, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a checks key: %d", path, rec.Code)
		}
	}

	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
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
		t.Errorf("a checks key's tools: %d", len(tools.Tools))
	}
}

// sendCheck posts a printed check's image as the check-printing program
// does: the image as the body, what was printed in the query.
func (s translatingSite) sendCheck(key, account string, q url.Values, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/"+account+"/checks?"+q.Encode(), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/pdf")

	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)

	return rec
}

func decoded(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v\n%s", err, rec.Body)
	}

	return out
}

// The program's whole round: the accounts it may print for, with the
// numbers used; a number asked about before printing; payees completed;
// a check sent, sent again, and refused when its number says something
// else or the bank paid it for another amount; a check that has cleared
// attached at once; the checks it sent listed, and one voided.
func TestPrintingChecksThroughTheAPI(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Office printer", "checks")
	acct := strings.TrimPrefix(e.account, "/accounts/")

	preview := uploaded(t, e.owner, e.account, "july.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n"+
		"2026-07-02,CHECK,-120.00,880.00,0001176\n"+
		"2026-07-09,HILLTOP PLUMBING,-30.00,850.00,1177\n")
	form := columns("import")
	form.Set("check", "Check Number")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d", rec.Code)
	}

	// The index lists the routes, as no tool.
	idx := s.api(http.MethodGet, "/api/v1", "", "")
	wantBody(t, idx, `"path": "/api/v1/accounts/{account}/checks"`, `"scope": "checks"`)

	// The accounts: the checking account, not the card.
	accounts := list(t, s.get(t, "/api/v1/checks/accounts", key), "accounts")
	if len(accounts) != 1 || field(accounts[0], "id") != acct || field(accounts[0], "last4") != "1234" ||
		field(accounts[0], "organization") != "St. Joseph Parish" || field(accounts[0], "highest") != "1177" {
		t.Fatalf("the accounts: %v", accounts)
	}

	// Before printing: 1178 is free, 1177 the bank paid.
	if got := s.get(t, "/api/v1/accounts/"+acct+"/checks/1178", key); field(got, "state") != "free" {
		t.Errorf("1178: %v", got)
	}

	cleared := s.get(t, "/api/v1/accounts/"+acct+"/checks/1177", key)
	if field(cleared, "state") != "cleared" || field(list(t, cleared, "cleared")[0], "amount") != "30.00" {
		t.Errorf("1177: %v", cleared)
	}

	printed := url.Values{"number": {"1178"}, "amount": {"12.34"}, "payee": {"Hilltop Plumbing"}, "written_on": {"2026-08-05"}, "memo": {"Supplies"}, "name": {"check-1178.pdf"}}
	pdf := "%PDF-1.4\n% an invented check\n"

	rec := s.sendCheck(key, acct, printed, pdf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sending: %d\n%s", rec.Code, rec.Body)
	}

	sent := decoded(t, rec)
	check := field(sent, "check")

	if field(sent, "outcome") != "outstanding" || field(check, "number") != "1178" || field(check, "amount") != "12.34" ||
		field(check, "payee") != "Hilltop Plumbing" || field(check, "written_on") != "2026-08-05" || field(check, "state") != "outstanding" {
		t.Errorf("sent: %v", sent)
	}

	// Again, as a program that lost the answer: the same check.
	rec = s.sendCheck(key, acct, printed, pdf+"% drawn again\n")
	if again := decoded(t, rec); rec.Code != http.StatusOK || field(again, "outcome") != "already_there" || field(field(again, "check"), "id") != field(check, "id") {
		t.Errorf("sent again: %d %v", rec.Code, again)
	}

	// The number, saying something else: refused, with the check.
	other := url.Values{}
	for k, v := range printed {
		other[k] = v
	}

	other.Set("amount", "99.00")

	rec = s.sendCheck(key, acct, other, pdf)
	if refused := decoded(t, rec); rec.Code != http.StatusConflict || field(refused, "code") != "number_used" || field(field(refused, "check"), "id") != field(check, "id") {
		t.Errorf("number used: %d %v", rec.Code, refused)
	}

	// The bank paid 1177 for 30.00.
	other.Set("number", "1177")
	other.Set("amount", "31.00")

	rec = s.sendCheck(key, acct, other, pdf)
	if refused := decoded(t, rec); rec.Code != http.StatusConflict || field(refused, "code") != "amount_differs" || field(refused, "bank") != "30.00" {
		t.Errorf("amount differs: %d %v", rec.Code, refused)
	}

	// For the bank's amount: attached at once.
	other.Set("amount", "30.00")

	rec = s.sendCheck(key, acct, other, pdf+"% 1177\n")
	if attached := decoded(t, rec); rec.Code != http.StatusCreated || field(attached, "outcome") != "attached" || len(field(field(attached, "check"), "attached_to").([]any)) != 1 {
		t.Errorf("attached: %d %v", rec.Code, attached)
	}

	if got := s.get(t, "/api/v1/accounts/"+acct+"/checks/1177", key); field(got, "state") != "attached" {
		t.Errorf("1177 now: %v", got)
	}

	// Its payee is on the organization's list now, to complete from.
	if got := list(t, s.get(t, "/api/v1/accounts/"+acct+"/payees?q=hill", key), "payees"); len(got) != 1 || field(got[0], "name") != "Hilltop Plumbing" {
		t.Errorf("payees: %v", got)
	}

	if got := list(t, s.get(t, "/api/v1/accounts/"+acct+"/payees?q=zzz", key), "payees"); len(got) != 0 {
		t.Errorf("payees for zzz: %v", got)
	}

	// The checks sent: one outstanding, two in all.
	if got := list(t, s.get(t, "/api/v1/accounts/"+acct+"/checks", key), "checks"); len(got) != 1 {
		t.Errorf("outstanding: %v", got)
	}

	if got := list(t, s.get(t, "/api/v1/accounts/"+acct+"/checks?state=all", key), "checks"); len(got) != 2 {
		t.Errorf("all: %v", got)
	}

	// Voided: kept, not outstanding, and its number free to print again.
	rec = s.api(http.MethodPost, "/api/v1/accounts/"+acct+"/checks/1178/void", key, "")
	if voided := decoded(t, rec); rec.Code != http.StatusOK || field(field(voided, "check"), "state") != "void" {
		t.Errorf("voided: %d %v", rec.Code, voided)
	}

	if got := s.get(t, "/api/v1/accounts/"+acct+"/checks/1178", key); field(got, "state") != "void" {
		t.Errorf("1178 voided: %v", got)
	}

	if rec := s.api(http.MethodPost, "/api/v1/accounts/"+acct+"/checks/1177/void", key, ""); rec.Code != http.StatusConflict || field(decoded(t, rec), "code") != "cleared" {
		t.Errorf("voiding a cleared check: %d %s", rec.Code, rec.Body)
	}

	if rec := s.api(http.MethodPost, "/api/v1/accounts/"+acct+"/checks/4444/void", key, ""); rec.Code != http.StatusNotFound || field(decoded(t, rec), "code") != "no_check" {
		t.Errorf("voiding no check: %d %s", rec.Code, rec.Body)
	}

	// The history says it came through the key.
	wantBody(t, e.owner.get(e.account), "sent check 1178 for 12.34 to Hilltop Plumbing, as printed", "voided check 1178", "Office printer")

	// What will not do, before the image is kept.
	for name, q := range map[string]url.Values{
		"no name":   {"number": {"1179"}, "amount": {"1.00"}, "payee": {"A"}, "written_on": {"2026-08-05"}},
		"no amount": {"number": {"1179"}, "payee": {"A"}, "written_on": {"2026-08-05"}, "name": {"x.pdf"}},
		"no day":    {"number": {"1179"}, "amount": {"1.00"}, "payee": {"A"}, "name": {"x.pdf"}},
		"no payee":  {"number": {"1179"}, "amount": {"1.00"}, "written_on": {"2026-08-05"}, "name": {"x.pdf"}},
	} {
		if rec := s.sendCheck(key, acct, q, pdf); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	card := strings.TrimPrefix(e.ownAccount, "/accounts/")
	if rec := s.sendCheck(key, card, printed, pdf); rec.Code != http.StatusUnprocessableEntity || field(decoded(t, rec), "code") != "not_checking" {
		t.Errorf("a check on a card: %d %s", rec.Code, rec.Body)
	}
}

// Nobody prints a check where they were not given: no key, a key that
// only reads the books, and a stranger's key are refused, and the
// stranger is told the account is not there.
func TestNobodyPrintsChecksWhereTheyWereNotGiven(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	acct := strings.TrimPrefix(e.account, "/accounts/")

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	strangers := keyFor(t, stranger, "Their printer", "checks")
	reader := keyFor(t, e.owner, "Claude", "books-read")
	keeper := keyFor(t, e.owner, "Claude Code", "books-keep")

	printed := url.Values{"number": {"1178"}, "amount": {"12.34"}, "payee": {"Hilltop Plumbing"}, "written_on": {"2026-08-05"}, "name": {"check-1178.pdf"}}
	pdf := "%PDF-1.4\n% an invented check\n"

	if rec := s.sendCheck("", acct, printed, pdf); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", rec.Code)
	}

	if rec := s.sendCheck(reader, acct, printed, pdf); rec.Code != http.StatusForbidden {
		t.Errorf("a reading key: %d", rec.Code)
	}

	for _, path := range []string{"/checks/1178", "/checks", "/payees"} {
		if rec := s.api(http.MethodGet, "/api/v1/accounts/"+acct+path, strangers, ""); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger's %s: %d", path, rec.Code)
		}
	}

	if rec := s.sendCheck(strangers, acct, printed, pdf); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's check: %d", rec.Code)
	}

	if rec := s.api(http.MethodPost, "/api/v1/accounts/"+acct+"/checks/1178/void", strangers, ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's void: %d", rec.Code)
	}

	if got := list(t, s.get(t, "/api/v1/checks/accounts", strangers), "accounts"); len(got) != 0 {
		t.Errorf("a stranger's accounts: %v", got)
	}

	// Keeping the books includes printing checks.
	if rec := s.sendCheck(keeper, acct, printed, pdf); rec.Code != http.StatusCreated {
		t.Errorf("a keeping key: %d %s", rec.Code, rec.Body)
	}
}
