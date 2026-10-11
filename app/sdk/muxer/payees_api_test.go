package muxer

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Claude, with a key that keeps the treasurer's books, tidies their payees
// as docs/payees.md (5) says: lists them and the suggestions, joins two on
// the treasurer's yes, splits one back off, and says whom a payment went
// to. Each change says on the site that a program made it, until the
// treasurer saves it there.
func TestClaudeTidiesPayeesThroughTheAPI(t *testing.T) {
	t.Parallel()

	k := newKept(t)
	s := k.s
	org := strings.TrimPrefix(k.e.org, "/orgs/")

	july := k.txByDescription(t, "2026-07")
	grocery, electric, coffee := july["CORNER GROCERY"]["id"].(string), july["ELECTRIC CO"]["id"].(string), july["COFFEE CART"]["id"].(string)

	// The treasurer wrote two payments' payees on the site, two ways.
	wantRedirect(t, k.e.owner.post("/transactions/"+grocery+"/payee", url.Values{"payee": {"Corner Grocery"}}), "/transactions/"+grocery+"?done=payee#paid-to")
	wantRedirect(t, k.e.owner.post("/transactions/"+coffee+"/payee", url.Values{"payee": {"CORNER GROCERY, INC."}}), "/transactions/"+coffee+"?done=payee#paid-to")

	// The list, asked by the organization or by one of its accounts.
	listed := s.get(t, "/api/v1/payees?organization="+org, k.key)
	if field(listed, "organization") != org || !strings.HasSuffix(field(listed, "url").(string), k.e.org+"/payees") {
		t.Errorf("the list: %v", listed)
	}

	byName := map[string]string{}
	for _, p := range list(t, listed, "payees") {
		byName[field(p, "name").(string)] = field(p, "id").(string)

		if field(p, "payments") != 1.0 || !strings.Contains(field(p, "url").(string), "/payees/") {
			t.Errorf("a payee: %v", p)
		}
	}

	shop, inc := byName["Corner Grocery"], byName["CORNER GROCERY, INC."]
	if shop == "" || inc == "" || len(byName) != 2 {
		t.Fatalf("the payees: %v", byName)
	}

	if again := list(t, s.get(t, "/api/v1/payees?account="+k.account, k.key), "payees"); len(again) != 2 {
		t.Errorf("the list by the account: %v", again)
	}

	for _, path := range []string{"/api/v1/payees", "/api/v1/payees?organization=" + org + "&account=" + k.account} {
		if rec := s.api(http.MethodGet, path, k.key, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: %d", path, rec.Code)
		}
	}

	// The suggestion, with each one's payments to say why.
	sugg := list(t, s.get(t, "/api/v1/payees/suggestions?organization="+org, k.key), "suggestions")
	if len(sugg) != 1 {
		t.Fatalf("the suggestions: %v", sugg)
	}

	from := field(sugg[0], "from").(map[string]any)
	if latest := from["payments_latest"].([]any); len(latest) != 1 || field(latest[0], "written_as") != "CORNER GROCERY, INC." || field(latest[0], "account_name") != "Parish checking" {
		t.Errorf("the suggestion's payments: %v", from)
	}

	into, other := field(sugg[0], "into", "id").(string), from["id"].(string)

	// Joined, and marked with the key on the site and in the history.
	joined := s.change(t, http.MethodPost, "/api/v1/payees/"+into+"/join", k.key, map[string]any{"from": []string{other}}, http.StatusOK)
	if p := field(joined, "payee").(map[string]any); p["through"] != "Claude" || len(p["spellings"].([]any)) != 2 || p["payments"] != 2.0 ||
		!slices.Equal(p["spellings_through"].([]any), []any{onlySpelling(from)}) {
		t.Errorf("joined: %v", joined)
	}

	wantBody(t, k.e.owner.get("/payees/"+into), "A program made, joined or split this payee through the API key", "by Claude")
	wantBody(t, k.e.owner.get(k.e.org+"/payees"), "by Claude")

	if rec := s.api(http.MethodPost, "/api/v1/payees/"+into+"/join", k.key, `{"from": ["`+into+`"]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a join into itself: %d %s", rec.Code, rec.Body)
	}

	if rec := s.api(http.MethodPost, "/api/v1/payees/"+into+"/join", k.key, `{"from": []}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a join of nobody: %d %s", rec.Code, rec.Body)
	}

	// Split back off: the payee it was comes back.
	split := s.change(t, http.MethodPost, "/api/v1/payees/"+into+"/split", k.key, map[string]any{"spelling": "corner grocery, inc."}, http.StatusOK)
	if field(split, "payee", "id") != other || field(split, "payee", "payments") != 1.0 || field(split, "from", "payments") != 1.0 {
		t.Errorf("split: %v", split)
	}

	for spelling, want := range map[string]int{"Not a spelling": http.StatusUnprocessableEntity, field(split, "from", "name").(string): http.StatusUnprocessableEntity} {
		if rec := s.api(http.MethodPost, "/api/v1/payees/"+into+"/split", k.key, `{"spelling": "`+spelling+`"}`); rec.Code != want {
			t.Errorf("splitting off %q: %d %s", spelling, rec.Code, rec.Body)
		}
	}

	// Whom a payment went to: a new name, then one of the list.
	set := s.change(t, http.MethodPut, "/api/v1/transactions/"+electric+"/payee", k.key, map[string]string{"name": "City Power"}, http.StatusOK)
	if field(set, "transaction", "payee") != "City Power" || field(set, "transaction", "payee_through") != "Claude" || field(set, "payee", "name") != "City Power" ||
		field(set, "transaction", "payee_id") != field(set, "payee", "id") {
		t.Errorf("set_payee by name: %v", set)
	}

	wantBody(t, k.e.owner.get("/transactions/"+electric), "A program wrote whom it was paid to through the API key")
	wantBody(t, k.e.owner.get(k.e.account+"/transactions?month=2026-07"), "changed by a program, to check")

	set = s.change(t, http.MethodPut, "/api/v1/transactions/"+electric+"/payee", k.key, map[string]string{"payee": shop}, http.StatusOK)
	if field(set, "transaction", "payee") != "Corner Grocery" || field(set, "payee", "id") != shop || field(set, "payee", "payments") != 2.0 {
		t.Errorf("set_payee by id: %v", set)
	}

	if rec := s.api(http.MethodPut, "/api/v1/transactions/"+electric+"/payee", k.key, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("set_payee with neither: %d %s", rec.Code, rec.Body)
	}

	// The treasurer saves each on the site, unchanged, and the marks go.
	wantRedirect(t, k.e.owner.post("/transactions/"+electric+"/payee", url.Values{"payee": {"Corner Grocery"}}), "/transactions/"+electric+"?done=payee#paid-to")

	if got := s.get(t, "/api/v1/transactions/"+electric, k.key); field(got, "transaction", "payee_through") != nil {
		t.Errorf("saved on the site, still marked: %v", got)
	}

	name := field(split, "from", "name").(string)
	wantRedirect(t, k.e.owner.post("/payees/"+into+"/rename", url.Values{"name": {name}, "was": {name}}), "/payees/"+into+"?done=saved")

	for _, p := range list(t, s.get(t, "/api/v1/payees?organization="+org, k.key), "payees") {
		if field(p, "id") == into && (p.(map[string]any)["through"] != nil || p.(map[string]any)["spellings_through"] != nil) {
			t.Errorf("saved on the site, still marked: %v", p)
		}
	}

	// The history names the key.
	var actions []string
	for _, c := range list(t, s.get(t, "/api/v1/changes", k.key), "changes") {
		actions = append(actions, field(c, "action").(string))
	}

	for _, want := range []string{"payee.joined", "payee.split", "payee.added", "transaction.paid-to"} {
		if !slices.Contains(actions, want) {
			t.Errorf("no %s among the changes through the key: %v", want, actions)
		}
	}
}

// onlySpelling is the spelling of a payee a suggestion gave that has one.
func onlySpelling(p map[string]any) any { return p["spellings"].([]any)[0] }
