package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// "For the accountant" (docs/accountant.md, 3): the organization's card
// leads to it; its first tab is the reader's own; each row says why it is
// listed and what it was given; a tab keeps the other choices; each list
// empty says what to do; and a transaction's page says which list it is
// in. Everything invented.
func TestTheAccountantsPage(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)
	accountant := e.org + "/accountant"

	// Before anything is important, the bookkeeper's tab is the empty to
	// do, which says so and points to the other.
	wantBody(t, e.owner.get(e.org), "For the accountant", `href="`+accountant+`?context=needed"`, `href="`+accountant+`?context=added"`)
	wantBody(t, e.owner.get(accountant), `aria-current="page">More context needed (0)`, "Context added (0)", "Nothing needs more context.", "See what is ready for the accountant")
	wantBody(t, e.owner.get(accountant+"?context=added"), `aria-current="page">Context added (0)`, "Nothing is ready for the accountant yet.")

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Diocesan collection"}, "kind": {"passthrough"}}), e.org+"/categories?done=added")

	coffee, grocery := txs["COFFEE CART"], txs["CORNER GROCERY"]
	choices := options(e.owner.get(grocery).Body.String())

	// Coffee passed on, and nothing more said of it: to do. The groceries
	// passed on for the project: ready.
	wantRedirect(t, e.owner.post(coffee, url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {choices["Diocesan collection"]}, "project-0": {""}, "memo-0": {""}}),
		e.account+"/transactions?month=2026-07&done=sorted")
	wantRedirect(t, e.owner.post(grocery, url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {choices["Diocesan collection"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""}}),
		e.account+"/transactions?month=2026-07&done=sorted")

	// The rows come before the form, whose further choices stay closed:
	// the tab is the page's, not a choice made in the form.
	needed := e.owner.get(accountant).Body.String()
	if strings.Contains(needed, "<details open>") || strings.Index(needed, ">COFFEE CART<") > strings.Index(needed, "Find among them") {
		t.Errorf("the form is open, or before the rows:\n%s", needed)
	}

	for _, want := range []string{`aria-current="page">More context needed (1)`, "Context added (1)", ">COFFEE CART<", `<span>pass-through</span>`, `class="tabs lists needed"`} {
		if !strings.Contains(needed, want) {
			t.Errorf("more context needed: no %q in\n%s", want, needed)
		}
	}

	if strings.Contains(needed, ">CORNER GROCERY<") {
		t.Error("the groceries, given a project, are still to do")
	}

	added := e.owner.get(accountant + "?context=added").Body.String()
	for _, want := range []string{">CORNER GROCERY<", "Added: <span>a project</span>", `class="tabs lists added"`} {
		if !strings.Contains(added, want) {
			t.Errorf("context added: no %q in\n%s", want, added)
		}
	}

	// A tab keeps the month; a month keeps the tab. The months offered
	// are the latest six, counted from today.
	now := time.Now()
	last := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	wantBody(t, e.owner.get(accountant+"?context=needed&month=2026-07"), `href="?month=2026-07&amp;context=added"`, `href="?context=needed&amp;month=`+last+`"`)

	// A filter nothing fits says so, rather than that nothing is to do.
	wantBody(t, e.owner.get(accountant+"?context=needed&q=nowhere"), "Nothing in this list fits these choices.")

	// A transaction's page says where it stands, in words.
	wantBody(t, e.owner.get(coffee), "More context needed.", "Part of it is money held for somebody else", "Add a description of its own, a receipt or whom it was paid to.")
	wantBody(t, e.owner.get(grocery), "Ready for the accountant.", "<span>a project</span>")

	if page := e.owner.get(txs["ELECTRIC CO"]).Body.String(); strings.Contains(page, "accountant") {
		t.Errorf("an ordinary bill speaks of the accountant:\n%s", page)
	}

	// The accountant's own first tab is what is ready, and it reads what
	// the search for the same list finds.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"accountant@example.org"}, "role": {"accountant"}}), e.org+"?done=granted")
	them := signUpAs("accountant@example.org")

	wantBody(t, them.get(accountant), `aria-current="page">Context added (1)`, ">CORNER GROCERY<")
	wantBody(t, them.get(e.org+"/transactions?context=needed"), "1 transactions fit", ">COFFEE CART<", `<option value="needed" selected>More context needed</option>`)

	// Somebody given nothing here finds no page, as for anything else.
	if rec := signUpAs("stranger@example.org").get(accountant); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: %d", rec.Code)
	}
}
