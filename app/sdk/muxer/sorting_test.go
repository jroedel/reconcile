package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

var (
	transactionLink = regexp.MustCompile(`href="(/transactions/[0-9a-f]+)">([^<]*)<`)
	categoryOption  = regexp.MustCompile(`<option value="([0-9a-f]{32})"[^>]*>([^<]+)</option>`)
	renameForm      = regexp.MustCompile(`action="(/categories/[0-9a-f]+)/rename"`)
)

// transactionsOf is the transaction pages linked from an account's month,
// by description.
func transactionsOf(t *testing.T, b *browser, account string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, m := range transactionLink.FindAllStringSubmatch(b.get(account+"/transactions").Body.String(), -1) {
		out[m[2]] = m[1]
	}

	return out
}

// options is a select's choices on a page, by name.
func options(body string) map[string]string {
	out := map[string]string{}
	for _, m := range categoryOption.FindAllStringSubmatch(body, -1) {
		out[m[2]] = m[1]
	}

	return out
}

// sorted is the estate with July imported and a category list.
func sorted(t *testing.T) (estate, map[string]string, http.Handler, func(string) *browser) {
	t.Helper()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	for _, name := range []string{"Groceries", "Utilities"} {
		wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {name}, "kind": {"expense"}}), e.org+"/categories?done=added")
	}

	imported(t, e.owner, e.account, "checking-july.csv", july)

	return e, transactionsOf(t, e.owner, e.account), h, func(addr string) *browser { return signUp(t, h, sent, addr) }
}

func TestSortingATransaction(t *testing.T) {
	e, txs, _, _ := sorted(t)

	grocery := txs["CORNER GROCERY"]
	if grocery == "" {
		t.Fatalf("no link to the grocery in %v", txs)
	}

	wantBody(t, e.owner.get(e.account+"/transactions"), "6 not sorted yet")

	page := e.owner.get(grocery).Body.String()
	choices := options(page)

	if choices["Groceries"] == "" || choices["World Youth Day"] == "" || !strings.Contains(page, "Money out") {
		t.Fatalf("the choices: %v", choices)
	}

	// One part: no amount to type.
	wantRedirect(t, e.owner.post(grocery, url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Groceries"]}, "project-0": {""}, "memo-0": {""},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	wantBody(t, e.owner.get(e.account+"/transactions"), "Groceries", "5 not sorted yet")
	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07&unsorted=1"), "COFFEE CART", "Show all of the month")

	if strings.Contains(e.owner.get(e.account+"/transactions?month=2026-07&unsorted=1").Body.String(), "CORNER GROCERY") {
		t.Error("a sorted transaction is on the unsorted list")
	}

	// Into two parts, one of them into the project.
	two := url.Values{
		"action":   {"add"},
		"amount-0": {"33.99"}, "category-0": {choices["Groceries"]}, "project-0": {""}, "memo-0": {""},
	}
	wantBody(t, e.owner.post(grocery, two), `name="amount-1"`, "Part 2")

	two.Set("action", "save")
	two.Set("amount-0", "30,00")
	two.Set("project-0", choices["World Youth Day"])
	two.Set("amount-1", "3.98")
	two.Set("category-1", choices["Utilities"])
	two.Set("project-1", "")
	two.Set("memo-1", "light bulbs")

	rec := e.owner.post(grocery, two)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("parts that do not add up: %d", rec.Code)
	}

	wantBody(t, rec, "must add up to the whole transaction", `value="3.98"`, "light bulbs")

	two.Set("amount-1", "3.99")
	wantRedirect(t, e.owner.post(grocery, two), e.account+"/transactions?month=2026-07&done=sorted")
	wantBody(t, e.owner.get(e.account+"/transactions"), "2 parts")

	// The project's book and history.
	wantBody(t, e.owner.get(e.project), `href="`+e.project+`/book"`, "put -$30.00 of CORNER GROCERY (2026-07-03, Parish checking) into it")
	wantBody(t, e.owner.get(e.project+"/book"), "Short by $30.00", "Groceries", "Parish checking", "CORNER GROCERY")
}

func TestCategoryLists(t *testing.T) {
	e, _, _, _ := sorted(t)

	// The two the organization started with, under their kinds, and the
	// two the owner added, under expenses.
	page := e.owner.get(e.org + "/categories").Body.String()
	for _, want := range []string{"Groceries", "Utilities", "Transfers between our accounts", "Personal, repaid", "Pass-through"} {
		if !strings.Contains(page, want) {
			t.Errorf("the list lacks %s", want)
		}
	}

	rec := e.owner.post(e.org+"/categories", url.Values{"name": {"groceries"}, "kind": {"expense"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a second Groceries: %d", rec.Code)
	}

	wantBody(t, rec, "already has a category with that name", `value="groceries"`)

	forms := renameForm.FindAllStringSubmatch(page, -1)
	if len(forms) != 4 {
		t.Fatalf("%d rename forms", len(forms))
	}

	// Expenses come first, so the first two are Groceries and Utilities.

	wantRedirect(t, e.owner.post(forms[0][1]+"/rename", url.Values{"name": {"Food"}}), e.org+"/categories?done=renamed")
	wantRedirect(t, e.owner.post(forms[0][1]+"/rename", url.Values{"name": {""}}), e.org+"/categories?problem=name")
	wantRedirect(t, e.owner.post(forms[1][1]+"/archive", url.Values{"archived": {"1"}}), e.org+"/categories?done=archived")
	wantBody(t, e.owner.get(e.org+"/categories"), "Food", "Archived categories")
	wantBody(t, e.owner.get(e.org), "added the category Groceries", "renamed the category Groceries to Food")

	// An account in the organization uses its list; a personal one has
	// its own.
	wantRedirect(t, e.owner.get(e.account+"/categories"), e.org+"/categories")
	wantRedirect(t, e.owner.post(e.ownAccount+"/categories", url.Values{"name": {"Books"}, "kind": {"expense"}}), e.ownAccount+"/categories?done=added")
	wantBody(t, e.owner.get(e.ownAccount), `href="`+e.ownAccount+`/categories"`)
}

// A transaction, a project's book and a category are as private as what
// they belong to; a viewer reads and sorts nothing; a project's own viewer
// reads its book and not the account.
func TestSortingIsAsPrivateAsTheAccount(t *testing.T) {
	e, txs, _, signUpAs := sorted(t)
	grocery := txs["CORNER GROCERY"]
	category := renameForm.FindStringSubmatch(e.owner.get(e.org + "/categories").Body.String())[1]

	save := url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {""}, "project-0": {""}, "memo-0": {"mine"}}

	stranger := signUpAs("stranger@example.org")

	for _, path := range []string{grocery, e.project + "/book"} {
		if rec := stranger.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger: GET %s = %d", path, rec.Code)
		}
	}

	for path, form := range map[string]url.Values{
		grocery:               save,
		category + "/rename":  {"name": {"Mine"}},
		category + "/archive": {"archived": {"1"}},
	} {
		if rec := stranger.post(path, form); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger: POST %s = %d", path, rec.Code)
		}
	}

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	if body := viewer.get(grocery).Body.String(); !strings.Contains(body, "not sorted yet") || strings.Contains(body, `name="amount-0"`) {
		t.Error("a viewer is offered the parts form")
	}

	for path, form := range map[string]url.Values{
		grocery:               save,
		category + "/rename":  {"name": {"Mine"}},
		e.org + "/categories": {"name": {"Mine"}},
	} {
		if rec := viewer.post(path, form); rec.Code != http.StatusForbidden {
			t.Errorf("a viewer: POST %s = %d", path, rec.Code)
		}
	}

	// Somebody given only the project.
	wantRedirect(t, e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"viewer"}}), e.project+"?done=granted")
	pilgrim := signUpAs("pilgrim@example.org")

	wantBody(t, pilgrim.get(e.project+"/book"), "Nothing in this project yet")

	for _, path := range []string{grocery, e.account + "/transactions"} {
		if rec := pilgrim.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("the pilgrim: GET %s = %d", path, rec.Code)
		}
	}
}
