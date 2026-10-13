package muxer

import (
	"net/url"
	"strings"
	"testing"
)

// A charge opened from somewhere other than its month -- a report's
// figure, a project's money -- offers the way back there, and saving goes
// back there, carrying it through the payee's form on the way. Everything
// invented.
func TestSavingGoesBackToWhereTheChargeWasOpened(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)
	grocery := txs["CORNER GROCERY"]
	report := e.org + "/reports/income-expenses-by-month"
	from := grocery + "?back=" + url.QueryEscape(report)

	page := e.owner.get(from).Body.String()
	choices := options(page)
	if !strings.Contains(page, `<a href="`+report+`"><span aria-hidden="true">←</span> Back to the report</a>`) {
		t.Fatalf("no way back to the report:\n%s", page)
	}

	// The payee's form comes back to the charge, still knowing the way.
	rec := e.owner.post(grocery+"/payee?back="+url.QueryEscape(report), url.Values{"payee": {"Corner Grocery"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "back="+url.QueryEscape(report)) || !strings.Contains(loc, "done=payee") {
		t.Errorf("setting the payee went to %q", loc)
	}

	sort := url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {choices["Groceries"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""}}
	wantRedirect(t, e.owner.post(from, sort), report)

	// A way back that is not a page of this site is no way back: saving
	// goes to the month, as it always did.
	elsewhere := grocery + "?back=" + url.QueryEscape("//elsewhere.invalid/reports")
	if strings.Contains(e.owner.get(elsewhere).Body.String(), `class="return`) {
		t.Error("a way back off the site was offered")
	}

	wantRedirect(t, e.owner.post(elsewhere, sort), e.account+"/transactions?month=2026-07&done=sorted")

	// The project's money opens each part's charge, carrying the way back,
	// and the charge names its project.
	book := e.owner.get(e.project + "/book").Body.String()
	if !strings.Contains(book, `href="`+grocery+`?back=`) {
		t.Errorf("the project's money does not open the grocery:\n%s", book)
	}

	wantBody(t, e.owner.get(grocery), `<a href="`+e.project+`/book">See the money of World Youth Day</a>`)

	// Somebody with a role on the project alone sees its parts but not the
	// account they are in: no link that would only answer "not found".
	wantRedirect(t, e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"viewer"}}), e.project+"?done=granted")
	pilgrim := signUpAs("pilgrim@example.org")

	book = pilgrim.get(e.project + "/book").Body.String()
	if !strings.Contains(book, "CORNER GROCERY") || strings.Contains(book, `href="`+grocery) {
		t.Errorf("the pilgrim's view of the project's money:\n%s", book)
	}
}
