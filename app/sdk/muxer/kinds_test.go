package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"
)

var kindForm = regexp.MustCompile(`action="(/categories/[0-9a-f]+)/kind"`)

// What each part was, through the pages: the choice grouped by kind, the
// month's operations beside its cash, a personal charge left to come back,
// a kind only the owner sets, and the project's net leaving pass-through
// out.
func TestKindsOfMoney(t *testing.T) {
	e, txs, _, signUpAs := sorted(t)

	grocery := txs["CORNER GROCERY"]
	page := e.owner.get(grocery).Body.String()
	choices := options(page)

	wantBody(t, e.owner.get(grocery), `<optgroup label="Expenses">`, `<optgroup label="Pass-through">`)

	// The groceries into the project as an expense, the bulbs personal.
	wantRedirect(t, e.owner.post(grocery, url.Values{
		"action":   {"save"},
		"amount-0": {"30.00"}, "category-0": {choices["Groceries"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
		"amount-1": {"3.99"}, "category-1": {choices["Personal, repaid"]}, "project-1": {choices["World Youth Day"]}, "memo-1": {"my bulbs"},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), "Income $0.00 · Expenses -$30.00 · Net -$30.00", "pass-through -$3.99")

	// The project is short by the groceries only.
	wantBody(t, e.owner.get(e.project+"/book"), "Short by $30.00", "Pass-through: -$3.99, which should come back to zero")

	// The bulbs are owed until repaid.
	wantBody(t, e.owner.get(e.org+"/categories"), "What should come back to zero", "Personal, repaid", "Still to come back: somebody has not repaid it yet.")

	// Only the owner says what a category is.
	bookkeeper := signUpAs("bookkeeper@example.org")
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"bookkeeper@example.org"}, "role": {"bookkeeper"}}), e.org+"?done=granted")

	m := kindForm.FindStringSubmatch(e.owner.get(e.org + "/categories").Body.String())
	if m == nil {
		t.Fatal("the owner has no kind form")
	}

	if regexp.MustCompile(`/kind"`).MatchString(bookkeeper.get(e.org + "/categories").Body.String()) {
		t.Error("a bookkeeper is offered the kind")
	}

	if rec := bookkeeper.post(m[1]+"/kind", url.Values{"kind": {"income"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a bookkeeper setting a kind: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(m[1]+"/kind", url.Values{"kind": {"refund"}}), e.org+"/categories?problem=kind")
	wantRedirect(t, e.owner.post(m[1]+"/kind", url.Values{"kind": {"income"}}), e.org+"/categories?done=kind")
}
