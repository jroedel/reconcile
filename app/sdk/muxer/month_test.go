package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// augustToSort follows july, with payees july's sorting can teach about.
const augustToSort = "Date,Description,Amount,Balance\n" +
	"2026-08-02,SQ *COFFEE CART 0802,-3.50,1085.51\n" +
	"2026-08-05,CORNER GROCERY,-40.00,1045.51\n" +
	"2026-08-09,ELECTRIC CO,-118.00,927.51\n"

var sortRowField = regexp.MustCompile(`name="tx-(\d+)" value="([0-9a-f]{32})"`)

// linksIn is a month's transaction pages by description.
func linksIn(b *browser, path string) map[string][]string {
	out := map[string][]string{}
	for _, m := range transactionLink.FindAllStringSubmatch(b.get(path).Body.String(), -1) {
		out[m[2]] = append(out[m[2]], m[1])
	}

	return out
}

// July sorted by hand suggests August's coffee; one Save sorts the rows
// with a choice, makes the rule asked for, and leaves the rest.
func TestSortingAMonthOnOnePage(t *testing.T) {
	t.Parallel()

	e, _, _, signUpAs := sorted(t)

	july := linksIn(e.owner, e.account+"/transactions?month=2026-07")
	coffees := july["COFFEE CART"]
	choices := options(e.owner.get(coffees[0]).Body.String())

	save := func(path, category string) {
		t.Helper()
		wantRedirect(t, e.owner.post(path, url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {category}, "project-0": {""}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	// One coffee sorted is not evidence; two are.
	save(coffees[0], choices["Groceries"])

	if strings.Contains(e.owner.get(coffees[1]).Body.String(), "not saved yet") {
		t.Error("a suggestion from one example")
	}

	save(coffees[1], choices["Groceries"])
	imported(t, e.owner, e.account, "checking-august.csv", augustToSort)

	aug := linksIn(e.owner, e.account+"/transactions?month=2026-08")
	wantBody(t, e.owner.get(aug["SQ *COFFEE CART 0802"][0]), "not saved yet",
		"Suggested, because 2 of 2 earlier COFFEE CART transactions were Groceries.")
	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-08"), `href="`+e.account+`/sort?month=2026-08"`)

	// The month on one page: the coffee preselected and marked.
	sortPage := e.account + "/sort?month=2026-08"
	page := e.owner.get(sortPage).Body.String()

	rows := map[string]string{}
	for _, m := range sortRowField.FindAllStringSubmatch(page, -1) {
		rows[m[2]] = m[1]
	}

	if len(rows) != 3 || !strings.Contains(page, "suggested</span>") || !strings.Contains(page, `<option value="`+choices["Groceries"]+`" selected>`) {
		t.Fatalf("the month to sort:\n%s", page)
	}

	form := url.Values{}
	for _, m := range sortRowField.FindAllStringSubmatch(page, -1) {
		form.Set("tx-"+m[1], m[2])
	}

	i := func(name string) string {
		t.Helper()

		for _, m := range transactionLink.FindAllStringSubmatch(page, -1) {
			if m[2] == name {
				return rows[strings.TrimPrefix(m[1], "/transactions/")]
			}
		}

		t.Fatalf("no row for %s", name)

		return ""
	}

	coffee, grocery, electric := i("SQ *COFFEE CART 0802"), i("CORNER GROCERY"), i("ELECTRIC CO")

	// A category that is not on the list refuses that row alone.
	form.Set("category-"+coffee, choices["Groceries"])
	form.Set("category-"+grocery, "0123456789abcdef0123456789abcdef")
	form.Set("category-"+electric, choices["Utilities"])
	form.Set("always-"+electric, "1")
	form.Set("match-"+electric, "Electric")

	rec := e.owner.post(sortPage, form)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a row that will not do: %d", rec.Code)
	}

	wantBody(t, rec, "Saved 2. The rest are below.", "Not saved: that category is not on this account")

	if strings.Contains(rec.Body.String(), "ELECTRIC CO") {
		t.Error("a saved row is still on the page")
	}

	// The rule the box asked for, and nothing more to save but the grocery.
	wantBody(t, e.owner.get(e.account+"/rules"), "“Electric”", "Utilities")

	page = e.owner.get(sortPage).Body.String()
	form = url.Values{}
	for _, m := range sortRowField.FindAllStringSubmatch(page, -1) {
		form.Set("tx-"+m[1], m[2])
	}

	if len(form) != 1 {
		t.Fatalf("%d rows left", len(form))
	}

	form.Set("category-0", "")
	wantRedirect(t, e.owner.post(sortPage, form), sortPage+"&n=0&done=sorted")

	form.Set("category-0", choices["Groceries"])
	wantRedirect(t, e.owner.post(sortPage, form), sortPage+"&n=1&done=sorted")
	wantBody(t, e.owner.get(sortPage+"&n=1&done=sorted"), "Saved 1.", "Nothing in this month is waiting to be sorted.")

	// A viewer is refused; a stranger finds nothing.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")

	for b, want := range map[*browser]int{signUpAs("viewer@example.org"): http.StatusForbidden, signUpAs("stranger@example.org"): http.StatusNotFound} {
		if got := b.get(sortPage).Code; got != want {
			t.Errorf("GET %s = %d, want %d", sortPage, got, want)
		}

		if got := b.post(sortPage, form).Code; got != want {
			t.Errorf("POST %s = %d, want %d", sortPage, got, want)
		}
	}
}
