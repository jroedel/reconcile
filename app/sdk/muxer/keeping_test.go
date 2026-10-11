package muxer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// kept is a treasurer's July imported by hand, a category list, a receipt
// waiting in the project's inbox, and a key that keeps their books.
type kept struct {
	booked
	categories map[string]string
	receipt    string
}

func newKept(t *testing.T) kept {
	t.Helper()

	b := newBooked(t)

	for _, name := range []string{"Groceries", "Utilities"} {
		wantRedirect(t, b.e.owner.post(b.e.org+"/categories", url.Values{"name": {name}, "kind": {"expense"}}), b.e.org+"/categories?done=added")
	}

	k := kept{booked: b, categories: map[string]string{}}
	k.key = keyFor(t, b.e.owner, "Claude", "books-keep")

	for _, c := range list(t, b.s.get(t, "/api/v1/accounts/"+b.account+"/to-sort?month=2026-07", k.key), "categories") {
		k.categories[field(c, "name").(string)] = field(c, "id").(string)
	}

	rec := b.e.owner.receipts(b.e.project+"/receipts", nil, [2]string{"IMG_0001.jpg", photoOf("one")})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a receipt: %d", rec.Code)
	}

	waiting := list(t, b.s.get(t, "/api/v1/receipts/waiting", k.key), "receipts")
	if len(waiting) != 1 {
		t.Fatalf("receipts waiting: %v", waiting)
	}

	k.receipt = field(waiting[0], "id").(string)

	return k
}

// change asks the API to change something and reads the answer, failing the
// test unless it is the status wanted.
func (s translatingSite) change(t *testing.T, method, path, key string, body any, want int) map[string]any {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	rec := s.api(method, path, key, string(raw))
	if rec.Code != want {
		t.Fatalf("%s %s: %d, want %d\n%s", method, path, rec.Code, want, rec.Body)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}

	return out
}

// txByDescription is an account month's transactions through the API, by
// description; the last of two with the same.
func (k kept) txByDescription(t *testing.T, month string) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	for _, tx := range list(t, k.s.get(t, "/api/v1/accounts/"+k.account+"/transactions?month="+month, k.key), "transactions") {
		out[field(tx, "description").(string)] = tx.(map[string]any)
	}

	return out
}

// Claude, with a key that keeps the treasurer's books, does the month's
// work: imports what the Gmail script sent, sorts, splits, writes a rule
// and applies it, attaches a receipt and gathers an explanation. Each
// change says on the site that a program made it, until the treasurer
// saves it there, and the history names the key.
func TestKeepingTheBooksThroughTheAPI(t *testing.T) {
	t.Parallel()

	k := newKept(t)
	s, key, owner := k.s, k.key, k.e.owner
	tx := "/api/v1/transactions/"

	me := s.get(t, "/api/v1/me", key)
	if scopes := field(me, "scopes").([]any); len(scopes) != 1 || scopes[0] != "books:write" {
		t.Errorf("whoami: %v", me)
	}

	wantBody(t, owner.get("/account/keys"), "keeps your books")

	// The inbox: August arrives by email, and is imported with nobody
	// looking, because the layout is remembered and it balances.
	if rec := s.send(keyFor(t, owner, "Gmail script", "upload"), "august.csv", august); rec.Code != http.StatusCreated {
		t.Fatalf("sending August: %d %s", rec.Code, rec.Body)
	}

	got := s.change(t, http.MethodPost, "/api/v1/inbox/import", key, map[string]any{}, http.StatusOK)
	if field(got, "imported") != 1.0 {
		t.Fatalf("importing from the inbox: %v", got)
	}

	if part := field(list(t, got, "files")[0], "parts").([]any)[0]; field(part, "outcome") != "imported" || field(part, "account_name") != "Parish checking" {
		t.Errorf("what became of August: %v", part)
	}

	if files := list(t, s.get(t, "/api/v1/inbox", key), "files"); len(files) != 0 {
		t.Errorf("imported, still in the inbox: %v", files)
	}

	// Sorting: two of July's, each whole. Asking again changes nothing,
	// and says why.
	july := k.txByDescription(t, "2026-07")
	grocery, electric := july["CORNER GROCERY"]["id"].(string), july["ELECTRIC CO"]["id"].(string)

	choices := map[string]any{"choices": []map[string]string{
		{"transaction": grocery, "category": k.categories["Groceries"]},
		{"transaction": electric, "category": k.categories["Utilities"], "project": strings.TrimPrefix(k.e.project, "/projects/")},
	}}

	if got := s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/sort", key, choices, http.StatusOK); field(got, "sorted") != 2.0 {
		t.Errorf("sorting: %v", got)
	}

	if got := s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/sort", key, choices, http.StatusOK); field(got, "sorted") != 0.0 || len(list(t, got, "left")) != 0 {
		t.Errorf("sorting again: %v", got)
	}

	other := map[string]any{"choices": []map[string]string{{"transaction": grocery, "category": k.categories["Utilities"]}}}
	if got := s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/sort", key, other, http.StatusOK); field(got, "sorted") != 0.0 || field(list(t, got, "left")[0], "why") != "sorted already" {
		t.Errorf("sorting a sorted one otherwise: %v", got)
	}

	if p := field(s.get(t, tx+grocery, key), "transaction", "parts").([]any)[0]; field(p, "category_name") != "Groceries" || field(p, "through") == nil {
		t.Errorf("the grocery's part: %v", p)
	}

	month := owner.get(k.e.account + "/transactions?month=2026-07")
	wantBody(t, month, "2 changed by a program, to check", "by Claude")
	wantBody(t, owner.get(k.e.account+"/transactions?month=2026-07&through=1"), "CORNER GROCERY", "ELECTRIC CO", "Show all of the month")
	wantBody(t, owner.get("/transactions/"+grocery), "sorted by a program", "Saving it, changed or not, says a person has checked it.")

	// The treasurer saves the grocery as it is, which says they checked.
	wantRedirect(t, owner.post("/transactions/"+grocery, url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {k.categories["Groceries"]}, "project-0": {""}, "memo-0": {""},
	}), k.e.account+"/transactions?month=2026-07&done=sorted")

	wantBody(t, owner.get(k.e.account+"/transactions?month=2026-07"), "1 changed by a program, to check")

	if p := field(s.get(t, tx+grocery, key), "transaction", "parts").([]any)[0]; field(p, "through") != nil {
		t.Errorf("saved on the web, the grocery still says a program sorted it: %v", p)
	}

	// Splitting: parts that do not add up are refused, saying which field.
	bad := map[string]any{"parts": []map[string]string{{"amount": "-100.00", "category": k.categories["Utilities"]}}}
	if got := s.change(t, http.MethodPut, tx+electric+"/splits", key, bad, http.StatusUnprocessableEntity); field(got, "error", "field") != "parts" {
		t.Errorf("parts that do not add up: %v", got)
	}

	two := map[string]any{"parts": []map[string]string{
		{"amount": "-100.00", "category": k.categories["Utilities"]},
		{"amount": "-20.00", "category": k.categories["Groceries"], "memo": "light bulbs"},
	}}

	if parts := field(s.change(t, http.MethodPut, tx+electric+"/splits", key, two, http.StatusOK), "transaction", "parts").([]any); len(parts) != 2 || field(parts[1], "memo") != "light bulbs" {
		t.Errorf("split: %v", parts)
	}

	// A description of its own: shown in the bank's place, the bank's
	// kept, and marked as the key's until a person saves the transaction
	// on the web, which says they have looked.
	described := s.change(t, http.MethodPut, tx+grocery+"/description", key, map[string]string{"description": "Retreat  groceries "}, http.StatusOK)
	if field(described, "transaction", "own_description") != "Retreat groceries" || field(described, "transaction", "description") != "CORNER GROCERY" ||
		field(described, "transaction", "own_description_through") != "Claude" {
		t.Errorf("describing: %v", described)
	}

	s.change(t, http.MethodPut, tx+grocery+"/description", key, map[string]string{"description": strings.Repeat("x", 201)}, http.StatusUnprocessableEntity)

	wantBody(t, owner.get(k.e.account+"/transactions?month=2026-07"), "Retreat groceries", "described by Claude", "2 changed by a program, to check")
	wantBody(t, owner.get("/transactions/"+grocery), "Retreat groceries", "The bank calls it CORNER GROCERY.", "described by a program")
	wantBody(t, owner.get(k.e.account+"/transactions?month=2026-07&through=1"), "Retreat groceries")

	wantRedirect(t, owner.post("/transactions/"+grocery, url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {k.categories["Groceries"]}, "project-0": {""}, "memo-0": {""},
		"description": {"Retreat groceries"},
	}), k.e.account+"/transactions?month=2026-07&done=sorted")

	if got := field(s.get(t, tx+grocery, key), "transaction"); field(got, "own_description") != "Retreat groceries" || field(got, "own_description_through") != nil {
		t.Errorf("saved on the web, the description: %v", got)
	}

	wantBody(t, owner.get(k.e.account+"/transactions?month=2026-07"), "1 changed by a program, to check")
	wantBody(t, owner.get(k.e.account), "as “Retreat groceries”")

	// A rule, tried first, saved, and applied: the three coffee carts of
	// July and August.
	rule := map[string]any{"match": "coffee cart", "direction": "out", "category": k.categories["Groceries"]}

	saved := s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/rules", key, rule, http.StatusOK)
	ruleID, _ := field(saved, "rule", "id").(string)

	if ruleID == "" || field(saved, "rule", "through") == nil {
		t.Fatalf("saving a rule: %v", saved)
	}

	wantBody(t, owner.get(k.e.account+"/rules"), "by Claude", "A program wrote this through the API key")

	if got := s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/rules/apply", key, map[string]any{}, http.StatusOK); field(got, "sorted") != 3.0 {
		t.Errorf("applying the rules: %v", got)
	}

	wantBody(t, owner.get(k.e.account+"/transactions?month=2026-08"), "COFFEE CART", "by rule")

	// Changed and removed, as a rule written wrongly would be.
	rule["match"] = "COFFEE CART"
	rule["direction"] = "any"
	if got := s.change(t, http.MethodPut, "/api/v1/rules/"+ruleID, key, rule, http.StatusOK); field(got, "rule", "direction") != "any" {
		t.Errorf("changing the rule: %v", got)
	}

	if got := s.change(t, http.MethodDelete, "/api/v1/rules/"+ruleID, key, nil, http.StatusOK); field(got, "removed", "match") != "COFFEE CART" {
		t.Errorf("removing the rule: %v", got)
	}

	if rules := list(t, s.get(t, "/api/v1/accounts/"+k.account+"/rules", key), "rules"); len(rules) != 0 {
		t.Errorf("removed, still there: %v", rules)
	}

	if rec := s.api(http.MethodDelete, "/api/v1/rules/"+ruleID, key, ""); rec.Code != http.StatusNotFound {
		t.Errorf("removing it again: %d", rec.Code)
	}

	// A receipt onto the grocery, and off again: back to waiting.
	on := map[string]string{"transaction": grocery}
	if got := s.change(t, http.MethodPost, "/api/v1/receipts/"+k.receipt+"/attach", key, on, http.StatusOK); field(got, "transaction", "receipts") != 1.0 {
		t.Errorf("attaching: %v", got)
	}

	if waiting := list(t, s.get(t, "/api/v1/receipts/waiting", key), "receipts"); len(waiting) != 0 {
		t.Errorf("attached, still waiting: %v", waiting)
	}

	s.change(t, http.MethodPost, "/api/v1/receipts/"+k.receipt+"/detach", key, on, http.StatusOK)

	if waiting := list(t, s.get(t, "/api/v1/receipts/waiting", key), "receipts"); len(waiting) != 1 {
		t.Errorf("taken off, not waiting: %v", waiting)
	}

	// An explanation: the offertory gathered into what explains the
	// opening deposit, and the app's count of it. A line not offered is
	// refused.
	deposit, offertory := july["OPENING DEPOSIT"]["id"].(string), july["PARISH OFFERTORY"]["id"].(string)
	gather := map[string]any{"add": []string{offertory}, "account": []string{k.account}, "from": "2026-07", "to": "2026-07"}

	x := s.change(t, http.MethodPost, tx+deposit+"/explanation/lines", key, gather, http.StatusOK)
	if len(list(t, x, "lines")) != 1 || field(x, "sum") != "250.00" || field(x, "difference") != "750.00" {
		t.Errorf("gathering: %v", x)
	}

	gather["add"] = []string{deposit}
	s.change(t, http.MethodPost, tx+offertory+"/explanation/lines", key, gather, http.StatusConflict)

	// The history names the key, and list_changes is what was done.
	changes := list(t, s.get(t, "/api/v1/changes", key), "changes")

	actions := map[string]bool{}
	for _, c := range changes {
		actions[field(c, "action").(string)] = true

		if field(c, "via") != "Claude" {
			t.Errorf("a change not through the key: %v", c)
		}
	}

	for _, want := range []string{"rule.made", "rule.changed", "rule.removed", "transaction.described"} {
		if !actions[want] {
			t.Errorf("no %s among the changes: %v", want, actions)
		}
	}

	wantBody(t, owner.get(k.e.account), "through Claude")
}

// Every write the API has, by method and path. A new one is added here on
// purpose, in a pull request that says so; none reconciles, reopens, or
// removes a statement or a receipt (docs/books-api.md, "Not in the API, on
// purpose").
func TestTheAPIsWritesAreTheList(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)

	var idx struct {
		Endpoints []struct{ Method, Path, Scope string }
	}

	if err := json.Unmarshal(s.api(http.MethodGet, "/api/v1", "", "").Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}

	var writes []string

	for _, e := range idx.Endpoints {
		if e.Method != http.MethodGet {
			writes = append(writes, e.Method+" "+e.Path+" "+e.Scope)
		}
	}

	want := []string{
		"DELETE /api/v1/rules/{rule} books:write",
		"POST /api/v1/accounts/{account}/rules books:write",
		"POST /api/v1/accounts/{account}/rules/apply books:write",
		"POST /api/v1/accounts/{account}/sort books:write",
		"POST /api/v1/inbox upload",
		"POST /api/v1/inbox/import books:write",
		"POST /api/v1/projects books:write",
		"POST /api/v1/receipts/{receipt}/attach books:write",
		"POST /api/v1/receipts/{receipt}/check books:write",
		"POST /api/v1/receipts/{receipt}/detach books:write",
		"POST /api/v1/transactions/{transaction}/explanation/lines books:write",
		"PUT /api/v1/projects/{project} books:write",
		"PUT /api/v1/rules/{rule} books:write",
		"PUT /api/v1/transactions/{transaction}/description books:write",
		"PUT /api/v1/transactions/{transaction}/splits books:write",
		"PUT /api/v1/translations translate",
	}

	slices.Sort(writes)

	if !slices.Equal(writes, want) {
		t.Errorf("the API's writes:\n%s\nwant:\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}

	for _, w := range writes {
		for _, never := range []string{"reconcil", "reopen", "DELETE /api/v1/statements", "DELETE /api/v1/receipts", "accept"} {
			if strings.Contains(w, never) {
				t.Errorf("%s is a write the API must not have", w)
			}
		}
	}
}

// Every keeping endpoint, asked by a stranger's key that keeps their own
// books, is a 404 for the treasurer's things and changes nothing; a key
// that only reads, or is for something else, is a 403.
func TestNobodyKeepsTheBooksTheyWereNotGiven(t *testing.T) {
	t.Parallel()

	k := newKept(t)
	s := k.s

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	theirs := keyFor(t, stranger, "Claude", "books-keep")
	reads := keyFor(t, k.e.owner, "laptop", "books-read")

	july := k.txByDescription(t, "2026-07")
	grocery := july["CORNER GROCERY"]["id"].(string)

	rule := s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/rules", k.key,
		map[string]any{"match": "electric co", "category": k.categories["Utilities"]}, http.StatusOK)

	ids := strings.NewReplacer(
		"{account}", k.account, "{transaction}", grocery, "{receipt}", k.receipt,
		"{rule}", field(rule, "rule", "id").(string), "{project}", strings.TrimPrefix(k.e.project, "/projects/"),
	)

	bodies := map[string]any{
		"sort_transactions":  map[string]any{"choices": []map[string]string{{"transaction": grocery, "category": k.categories["Groceries"]}}},
		"set_splits":         map[string]any{"parts": []map[string]string{{"amount": "-33.99", "category": k.categories["Groceries"]}}},
		"set_description":    map[string]string{"description": "Retreat groceries"},
		"save_rule":          map[string]any{"match": "corner grocery", "category": k.categories["Groceries"]},
		"change_rule":        map[string]any{"match": "electric co", "category": k.categories["Groceries"]},
		"attach_receipt":     map[string]string{"transaction": grocery},
		"detach_receipt":     map[string]string{"transaction": grocery},
		"read_check":         map[string]string{"number": "1176", "amount": "120.00"},
		"gather_explanation": map[string]any{"add": []string{}},
		"apply_rules":        map[string]any{},
		"import_from_inbox":  map[string]any{},
		"create_project":     map[string]string{"organization": strings.TrimPrefix(k.e.org, "/orgs/"), "name": "Mine now"},
		"change_project":     map[string]string{"name": "Mine now"},
	}

	var idx struct {
		Endpoints []struct{ Method, Path, Scope, Tool string }
	}

	if err := json.Unmarshal(s.api(http.MethodGet, "/api/v1", "", "").Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}

	translate := makeKey(t, s.admin, "laptop")
	before := s.api(http.MethodGet, "/api/v1/accounts/"+k.account+"/transactions?month=2026-07", k.key, "").Body.String()
	walked := 0

	for _, e := range idx.Endpoints {
		if e.Scope != "books:write" {
			continue
		}

		walked++
		path := ids.Replace(e.Path)

		raw := ""
		if e.Method != http.MethodDelete {
			b, _ := json.Marshal(bodies[e.Tool])
			raw = string(b)
		}

		for name, key := range map[string]string{"a reading key": reads, "a translating key": translate} {
			if rec := s.api(e.Method, path, key, raw); rec.Code != http.StatusForbidden {
				t.Errorf("%s: %s %s = %d, want 403", name, e.Method, e.Path, rec.Code)
			}
		}

		rec := s.api(e.Method, path, theirs, raw)

		// A project made in the treasurer's organization is as much theirs
		// as anything with an id in its path.
		switch {
		case strings.Contains(e.Path, "{") || e.Tool == "create_project":
			if rec.Code != http.StatusNotFound {
				t.Errorf("a stranger: %s %s = %d, want 404\n%s", e.Method, e.Path, rec.Code, rec.Body)
			}
		case rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Parish"):
			t.Errorf("a stranger: %s %s = %d %s", e.Method, e.Path, rec.Code, rec.Body)
		}
	}

	if walked != 14 {
		t.Errorf("%d keeping endpoints were walked, want 14", walked)
	}

	if after := s.api(http.MethodGet, "/api/v1/accounts/"+k.account+"/transactions?month=2026-07", k.key, "").Body.String(); after != before {
		t.Errorf("a stranger changed the treasurer's month:\n%s\nthen\n%s", before, after)
	}

	if rules := list(t, s.get(t, "/api/v1/accounts/"+k.account+"/rules", k.key), "rules"); len(rules) != 1 || field(rules[0], "category_name") != "Utilities" {
		t.Errorf("a stranger changed the treasurer's rules: %v", rules)
	}
}

// On /mcp, a key that keeps the books lists the reading tools and the
// keeping ones, a write marked as one and a removal as destructive, and a
// tool's change is the API's, marked with the key.
func TestClaudeKeepsTheBooksThroughMCP(t *testing.T) {
	t.Parallel()

	k := newKept(t)

	srv := httptest.NewServer(k.s.h)
	t.Cleanup(srv.Close)

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)

	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{k.key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	if got := cs.InitializeResult().Instructions; !strings.Contains(got, "Bookkeeping, not locking") {
		t.Errorf("the instructions:\n%s", got)
	}

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}

	for _, want := range []string{"get_overview", "try_rule", "import_from_inbox", "sort_transactions", "set_splits", "save_rule", "apply_rules", "attach_receipt", "gather_explanation"} {
		if byName[want] == nil {
			t.Errorf("no %s among the tools", want)
		}
	}

	if a := byName["sort_transactions"].Annotations; a == nil || a.ReadOnlyHint {
		t.Error("sort_transactions is marked read-only")
	}

	if a := byName["remove_rule"].Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Error("remove_rule is not marked destructive")
	}

	grocery := k.txByDescription(t, "2026-07")["CORNER GROCERY"]["id"].(string)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "sort_transactions", Arguments: map[string]any{
		"account": k.account,
		"choices": []map[string]string{{"transaction": grocery, "category": k.categories["Groceries"]}},
	}})
	if err != nil || res.IsError || !strings.Contains(toolText(res), `"sorted": 1`) {
		t.Fatalf("sort_transactions: %v %s", err, toolText(res))
	}

	wantBody(t, k.e.owner.get(k.e.account+"/transactions?month=2026-07"), "1 changed by a program, to check")
}
