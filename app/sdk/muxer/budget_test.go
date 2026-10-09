package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// An owner sets a project's budget; anyone with the project reads it
// beside what happened; the history says what was set.
func TestAProjectsBudget(t *testing.T) {
	e, txs, _, signUpAs := sorted(t)

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Offerings"}, "kind": {"income"}}), e.org+"/categories?done=added")

	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	for name, category := range map[string]string{"CORNER GROCERY": choices["Groceries"], "PARISH OFFERTORY": choices["Offerings"]} {
		wantRedirect(t, e.owner.post(txs[name], url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {category}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	budget := e.project + "/budget"
	wantBody(t, e.owner.get(e.project), `href="`+budget+`"`)
	wantBody(t, e.owner.get(budget), "No budget yet", "Set the budget", `name="expense-`+choices["Groceries"]+`"`, `name="income-`+choices["Offerings"]+`"`)

	form := url.Values{
		"currency":                        {"USD"},
		"income-" + choices["Offerings"]:  {"300"},
		"expense-" + choices["Groceries"]: {"abc"},
		"expense-" + choices["Utilities"]: {""},
		"expense-total":                   {""},
	}

	rec := e.owner.post(budget, form)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an amount that is not one: %d", rec.Code)
	}

	wantBody(t, rec, "One amount could not be read", `value="abc"`, "This needs an amount")

	form.Set("expense-"+choices["Groceries"], "50,00")
	wantRedirect(t, e.owner.post(budget, form), budget+"?done=set")

	wantBody(t, e.owner.get(budget), "The budget expects $250.00 left over.", "So far $216.01 is in hand.",
		"$250.00 of $300.00 received", "$50.00 still to come in", "$33.99 of $50.00 spent", "$16.01 left", `value="50.00"`)
	wantBody(t, e.owner.get(e.project), "budgeted $300.00 for Offerings", "budgeted $50.00 for Groceries")

	// Changed, and taken out.
	form.Set("expense-"+choices["Groceries"], "")
	form.Set("expense-total", "40")
	wantRedirect(t, e.owner.post(budget, form), budget+"?done=set")
	wantBody(t, e.owner.get(e.project), "took Groceries out of the budget", "budgeted $40.00 of expenses in all")
	wantBody(t, e.owner.get(budget), "$33.99 of $40.00 spent", "Not in the budget")

	// A viewer of the project reads it and is not offered the form; a
	// stranger finds nothing.
	wantRedirect(t, e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"viewer"}}), e.project+"?done=granted")
	pilgrim := signUpAs("pilgrim@example.org")

	if body := pilgrim.get(budget).Body.String(); !strings.Contains(body, "$33.99 of $40.00 spent") || strings.Contains(body, "Save the budget") {
		t.Errorf("the pilgrim's view:\n%s", body)
	}

	if rec := pilgrim.post(budget, form); rec.Code != http.StatusForbidden {
		t.Errorf("the pilgrim set the budget: %d", rec.Code)
	}

	stranger := signUpAs("stranger@example.org")
	for _, rec := range []int{stranger.get(budget).Code, stranger.post(budget, form).Code} {
		if rec != http.StatusNotFound {
			t.Errorf("a stranger: %d", rec)
		}
	}
}
