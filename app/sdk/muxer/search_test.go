package muxer

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// backLink is a row's link to its charge with the way back to the search.
var backLink = regexp.MustCompile(`href="(/transactions/[0-9a-f]+\?back=[^"]+)">CORNER GROCERY<`)

// The account's month list with the form on it (docs/explorer.md): with
// only a month, the month as it always was; with words, a kind or a bad
// amount, what fits across the months, with the choices given back; and a
// charge opened from the results comes back to them after saving.
// Everything invented.
func TestSearchingAnAccount(t *testing.T) {
	t.Parallel()

	e, txs, _, _ := sorted(t)
	grocery := txs["CORNER GROCERY"]
	choices := options(e.owner.get(grocery).Body.String())

	sort := url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {choices["Groceries"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""}}
	wantRedirect(t, e.owner.post(grocery, sort), e.account+"/transactions?month=2026-07&done=sorted")

	// The month, as it was, and its links to what is to sort still work.
	month := e.owner.get(e.account + "/transactions").Body.String()
	if !strings.Contains(month, "5 not sorted yet") || strings.Contains(month, "transactions fit") || !strings.Contains(month, "Find transactions") {
		t.Errorf("the month, nothing chosen:\n%s", month)
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07&category=unsorted"), "COFFEE CART", "Show all of the month", "5 transactions fit")

	// Words, given back, and the months keep them.
	coffee := e.owner.get(e.account + "/transactions?q=coffee").Body.String()
	for _, want := range []string{"2 transactions fit", `value="coffee"`, "?month=2026-07&amp;q=coffee", "-$7.00"} {
		if !strings.Contains(coffee, want) {
			t.Errorf("searching for coffee: no %q in\n%s", want, coffee)
		}
	}

	if strings.Contains(coffee, ">CORNER GROCERY<") {
		t.Error("the grocery fits a search for coffee")
	}

	// A whole kind, chosen and given back; the further choices open.
	expenses := e.owner.get(e.account + "/transactions?category=kind:expense").Body.String()
	for _, want := range []string{"1 transactions fit", `<option value="kind:expense" selected>All expenses</option>`, "<details open>", ">CORNER GROCERY<"} {
		if !strings.Contains(expenses, want) {
			t.Errorf("all expenses: no %q in\n%s", want, expenses)
		}
	}

	// An amount that cannot be read is said, and left out.
	wantBody(t, e.owner.get(e.account+"/transactions?min=twelve&q=coffee"), "could not be read", "2 transactions fit")

	// From the results to the charge and back.
	m := backLink.FindStringSubmatch(expenses)
	if m == nil {
		t.Fatalf("no way back from the grocery's row:\n%s", expenses)
	}

	opened := html.UnescapeString(m[1])
	wantBody(t, e.owner.get(opened), "Back to the transactions you found")
	wantRedirect(t, e.owner.post(opened, sort), e.account+"/transactions?category=kind%3Aexpense")
}
