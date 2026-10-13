package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The organization's lists say how much each account and project has held,
// and the front page says it of those on their own (issue #106).
func TestTheListsFigures(t *testing.T) {
	t.Parallel()

	e, txs, _, _ := sorted(t)

	choices := options(e.owner.get(txs["ELECTRIC CO"]).Body.String())
	wantRedirect(t, e.owner.post(txs["ELECTRIC CO"], url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Utilities"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	page := e.owner.get(e.org)
	wantBody(t, page,
		"From 2026-07-01 to 2026-07-28: 6 transactions, money in $1,250.00, money out -$160.99.",
		"Balance on 2026-07-28: $1,089.01",
		"2026-07-28 · 1 transactions",
		`Net <span class="money">-$120.00</span>`)

	// World Youth Day is an ordinary project until it is said to be a
	// break-even one: only then is it short.
	if strings.Contains(page.Body.String(), "Short by") {
		t.Errorf("an ordinary project reads as short:\n%s", page.Body.String())
	}

	wantRedirect(t, e.owner.post(e.project+"/edit", url.Values{"name": {"World Youth Day"}, "starts": {"2027-08-01"}, "break-even": {"1"}}), e.project+"?done=saved")
	wantBody(t, e.owner.get(e.project), `name="break-even" value="1" checked`, "Break-even project", "changed its details, and made it a break-even project")
	wantBody(t, e.owner.get(e.org), "Short by $120.00")

	// A collection passed on to the diocese: pass-through only, so its
	// line says what went through and what is still to pass on, and
	// nothing says "Even" of it.
	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Diocesan collection"}, "kind": {"passthrough"}}), e.org+"/categories?done=added")

	rec := e.owner.post("/projects", url.Values{"org": {strings.TrimPrefix(e.org, "/orgs/")}, "name": {"Second collection"}})
	collection := strings.SplitN(rec.Header().Get("Location"), "?", 2)[0]
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(collection, "/projects/") {
		t.Fatalf("making the project: %d to %q", rec.Code, collection)
	}

	choices = options(e.owner.get(txs["PARISH OFFERTORY"]).Body.String())
	for _, name := range []string{"PARISH OFFERTORY", "COFFEE CART"} {
		wantRedirect(t, e.owner.post(txs[name], url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {choices["Diocesan collection"]}, "project-0": {choices["Second collection"]}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	page = e.owner.get(e.org)
	wantBody(t, page, "Pass-through: $250.00 in, $3.50 out · <strong>$246.50 still to pass on</strong>", "Short by $120.00")

	if strings.Contains(page.Body.String(), "Even") {
		t.Errorf("a project of pass-through only reads as even:\n%s", page.Body.String())
	}

	wantBody(t, e.owner.get(collection+"/book"), "Pass-through: $250.00 in, $3.50 out", "$246.50 still to pass on")

	imported(t, e.owner, e.ownAccount, "checking-july.csv", july)
	wantBody(t, e.owner.get("/"), "From 2026-07-01 to 2026-07-28: 6 transactions")
}
