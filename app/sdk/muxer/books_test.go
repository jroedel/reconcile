package muxer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// get asks the API as a program does and reads the JSON answer, failing
// the test unless it is a 200.
func (s translatingSite) get(t *testing.T, path, key string) map[string]any {
	t.Helper()

	rec := s.api(http.MethodGet, path, key, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}

	return out
}

// list is a list in an answer.
func list(t *testing.T, v map[string]any, key string) []any {
	t.Helper()

	l, ok := v[key].([]any)
	if !ok {
		t.Fatalf("no list %q in %v", key, v)
	}

	return l
}

// field is a value in an object in an answer.
func field(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}

		v = m[p]
	}

	return v
}

// booked is a treasurer's July imported, and a key that reads their books.
type booked struct {
	s                         translatingSite
	e                         estate
	key                       string
	account, statement, first string
}

func newBooked(t *testing.T) booked {
	t.Helper()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	b := booked{s: s, e: e, key: keyFor(t, e.owner, "Claude", "books-read")}

	b.account = strings.TrimPrefix(e.account, "/accounts/")
	b.statement = strings.TrimPrefix(imported(t, e.owner, e.account, "checking-july.csv", july), "/statements/")

	txs := list(t, s.get(t, "/api/v1/accounts/"+b.account+"/transactions?month=2026-07", b.key), "transactions")
	b.first = field(txs[0], "id").(string)

	return b
}

// Claude, with a key that reads the treasurer's books, reads them as the
// treasurer does, from the overview down to one transaction, and finds,
// tries and explains without changing anything.
func TestReadingTheBooksThroughTheAPI(t *testing.T) {
	b := newBooked(t)
	s, key := b.s, b.key

	me := s.get(t, "/api/v1/me", key)
	if scopes := field(me, "scopes").([]any); len(scopes) != 1 || scopes[0] != "books:read" {
		t.Errorf("whoami: %v", me)
	}

	ov := s.get(t, "/api/v1/overview", key)
	orgs := list(t, ov, "organizations")

	if len(orgs) != 1 || field(orgs[0], "name") != "St. Joseph Parish" {
		t.Fatalf("the overview: %v", ov)
	}

	acct := field(orgs[0], "accounts").([]any)[0]
	if field(acct, "name") != "Parish checking" || field(acct, "unsorted") != 6.0 || field(acct, "last_statement_ends") != "2026-07-28" {
		t.Errorf("the account on the overview: %v", acct)
	}

	months := list(t, s.get(t, "/api/v1/accounts/"+b.account+"/months", key), "months")
	if field(months[len(months)-1], "month") != "2026-07" || field(months[len(months)-1], "state") != "partial" {
		t.Errorf("the months: %v", months)
	}

	sts := list(t, s.get(t, "/api/v1/accounts/"+b.account+"/statements", key), "statements")
	if len(sts) != 1 || field(sts[0], "checked") != "balances" || field(sts[0], "closing") != "1089.01" {
		t.Errorf("the statements: %v", sts)
	}

	st := s.get(t, "/api/v1/statements/"+b.statement, key)
	if field(st, "in") != "1250.00" || len(list(t, st, "unsorted")) != 6 {
		t.Errorf("the statement: in %v, unsorted %d", field(st, "in"), len(list(t, st, "unsorted")))
	}

	tx := s.get(t, "/api/v1/transactions/"+b.first, key)
	if field(tx, "transaction", "description") != "OPENING DEPOSIT" || field(tx, "transaction", "account_name") != "Parish checking" {
		t.Errorf("the transaction: %v", tx)
	}

	if got := list(t, s.get(t, "/api/v1/transactions?text=coffee", key), "transactions"); len(got) != 2 {
		t.Errorf("finding the coffee: %d", len(got))
	}

	if got := list(t, s.get(t, "/api/v1/transactions?amount=-120&direction=out", key), "transactions"); len(got) != 1 || field(got[0], "description") != "ELECTRIC CO" {
		t.Errorf("finding the bill: %v", got)
	}

	if got := list(t, s.get(t, "/api/v1/accounts/"+b.account+"/to-sort?month=2026-07", key), "rows"); len(got) != 6 {
		t.Errorf("the month to sort: %d rows", len(got))
	}

	trial := s.get(t, "/api/v1/accounts/"+b.account+"/rules/try?match=coffee+cart&direction=out", key)
	if field(trial, "would", "count") != 2.0 || field(trial, "saved") != false {
		t.Errorf("trying a rule: %v", trial)
	}

	if got := list(t, s.get(t, "/api/v1/accounts/"+b.account+"/rules", key), "rules"); len(got) != 0 {
		t.Errorf("trying a rule saved it: %v", got)
	}

	if h := s.get(t, "/api/v1/accounts/"+b.account+"/holders?month=2026-07", key); field(h, "by_holder") != false {
		t.Errorf("the holders: %v", h)
	}

	x := s.get(t, "/api/v1/transactions/"+b.first+"/explanation", key)
	if field(x, "state") != "not gathered yet" || field(x, "difference") != "1000.00" {
		t.Errorf("the explanation: %v", x)
	}

	cands := s.get(t, "/api/v1/transactions/"+b.first+"/explanation/candidates?account="+b.account+"&from=2026-07&to=2026-07", key)
	if got := list(t, cands, "lines"); len(got) != 5 {
		t.Errorf("the candidates: %d", len(got))
	}

	s.get(t, "/api/v1/receipts/waiting", key)
	s.get(t, "/api/v1"+b.e.project, key)

	if got := list(t, s.get(t, "/api/v1/changes", key), "changes"); len(got) != 0 {
		t.Errorf("reading changed something: %v", got)
	}

	// A refusal says what to fix.
	for path, code := range map[string]int{
		"/api/v1/accounts/" + b.account + "/transactions":                      http.StatusBadRequest,
		"/api/v1/accounts/" + b.account + "/rules/try?match=co":                http.StatusUnprocessableEntity,
		"/api/v1/transactions?amount=lots":                                     http.StatusBadRequest,
		"/api/v1/accounts/not-an-id/months":                                    http.StatusNotFound,
		"/api/v1/accounts/0123456789abcdef0123456789abcdef/months":             http.StatusNotFound,
		"/api/v1/accounts/" + b.account + "/transactions?month=2026-07&show=x": http.StatusBadRequest,
	} {
		if rec := s.api(http.MethodGet, path, key, ""); rec.Code != code || !strings.Contains(rec.Body.String(), `"problem"`) {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

// Every reading endpoint, asked by a stranger's key for the treasurer's
// things, is a 404, the same as for something that does not exist; those
// that list find none of the treasurer's. And a key for something else
// reads nothing.
func TestNobodyReadsTheBooksTheyWereNotGiven(t *testing.T) {
	b := newBooked(t)
	s := b.s

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	theirs := keyFor(t, stranger, "Claude", "books-read")

	ids := strings.NewReplacer(
		"{account}", b.account, "{statement}", b.statement, "{transaction}", b.first,
		"{project}", strings.TrimPrefix(b.e.project, "/projects/"),
	)

	var idx struct {
		Endpoints []struct {
			Method, Path, Scope string
		}
	}

	if err := json.Unmarshal(s.api(http.MethodGet, "/api/v1", "", "").Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}

	upload := keyFor(t, b.e.owner, "Gmail script", "upload")
	translate := makeKey(t, s.admin, "laptop")
	reads := 0

	for _, e := range idx.Endpoints {
		if e.Scope != "books:read" {
			continue
		}

		reads++
		path := ids.Replace(e.Path) + "?month=2026-07&match=coffee"

		for name, key := range map[string]string{"an upload key": upload, "a translating key": translate} {
			if rec := s.api(e.Method, path, key, ""); rec.Code != http.StatusForbidden {
				t.Errorf("%s: %s %s = %d, want 403", name, e.Method, e.Path, rec.Code)
			}
		}

		rec := s.api(e.Method, path, theirs, "")

		if strings.Contains(e.Path, "{") {
			if rec.Code != http.StatusNotFound {
				t.Errorf("a stranger: %s %s = %d, want 404", e.Method, e.Path, rec.Code)
			}

			continue
		}

		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Parish") || strings.Contains(rec.Body.String(), "COFFEE") {
			t.Errorf("a stranger: %s %s = %d %s", e.Method, e.Path, rec.Code, rec.Body)
		}
	}

	if reads < 18 {
		t.Errorf("only %d reading endpoints were walked", reads)
	}
}

// On /mcp, a key that reads the books lists the reading tools and no
// others, and a tool's answer is the API's.
func TestClaudeReadsTheBooksThroughMCP(t *testing.T) {
	b := newBooked(t)

	srv := httptest.NewServer(b.s.h)
	t.Cleanup(srv.Close)

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)

	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{b.key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	if got := cs.InitializeResult().Instructions; !strings.Contains(got, "Start with get_overview") {
		t.Errorf("the instructions:\n%s", got)
	}

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true

		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
	}

	for _, want := range []string{"get_overview", "find_transactions", "try_rule", "get_explanation", "list_changes"} {
		if !names[want] {
			t.Errorf("no %s among %v", want, names)
		}
	}

	if names["put_translations"] {
		t.Error("a key that reads the books lists the translations")
	}

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "find_transactions", Arguments: map[string]any{"text": "coffee", "account": b.account}})
	if err != nil || res.IsError || !strings.Contains(toolText(res), "COFFEE CART") {
		t.Errorf("find_transactions: %v %s", err, toolText(res))
	}

	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_statement", Arguments: map[string]any{"statement": b.statement}})
	if err != nil || res.IsError || !strings.Contains(toolText(res), `"checked": "balances"`) {
		t.Errorf("get_statement: %v %s", err, toolText(res))
	}
}
