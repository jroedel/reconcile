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

// An organization's year: set for 2026, moved to run July to June, copied
// into the year after; read by a viewer, set only by an owner.
func TestAnOrganizationsBudgetYear(t *testing.T) {
	e, txs, _, signUpAs := sorted(t)

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Offerings"}, "kind": {"income"}}), e.org+"/categories?done=added")

	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	for name, category := range map[string]string{
		"CORNER GROCERY": choices["Groceries"], "PARISH OFFERTORY": choices["Offerings"], "ELECTRIC CO": choices["Utilities"],
	} {
		wantRedirect(t, e.owner.post(txs[name], url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {category}, "project-0": {""}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	budget := e.org + "/budget"
	wantBody(t, e.owner.get(e.org), `href="`+budget+`"`)
	wantBody(t, e.owner.get(budget+"?year=2026"), "Budget for 2026", "From 2026-01-01 to 2026-12-31", "No budget for this year", "Set the budget")

	wantRedirect(t, e.owner.post(budget+"?year=2026", url.Values{
		"currency": {"USD"}, "income-" + choices["Offerings"]: {"3000"}, "expense-" + choices["Utilities"]: {"1,200.00"},
	}), budget+"?year=2026&done=set")

	wantBody(t, e.owner.get(budget+"?year=2026"), "$250.00 of $3,000.00 received", "$120.00 of $1,200.00 spent", "Not in the budget", "Groceries")

	// Moved to July: 2026 is now July 2026 to June 2027, and still holds
	// July's money and the budget set for it.
	wantRedirect(t, e.owner.post(budget+"/year-start", url.Values{"month": {"7"}}), budget+"?done=year-start")
	wantBody(t, e.owner.get(budget+"?year=2026"), "Budget for 2026–27", "From 2026-07-01 to 2027-06-30", "$120.00 of $1,200.00 spent")
	wantRedirect(t, e.owner.post(budget+"/year-start", url.Values{"month": {"13"}}), budget+"?done=no-month")

	// The year after starts as a copy.
	wantBody(t, e.owner.get(budget+"?year=2027"), "Copy 2026–27&#39;s budget")
	wantRedirect(t, e.owner.post(budget+"/copy?year=2027", nil), budget+"?year=2027&done=copied")
	wantBody(t, e.owner.get(budget+"?year=2027"), "$0.00 of $3,000.00 received")
	wantRedirect(t, e.owner.post(budget+"/copy?year=2027", nil), budget+"?year=2027&done=nothing-to-copy")

	wantBody(t, e.owner.get(e.org), "made the budget year start in July", "copied the budget for 2026–27 into 2027–28", "budgeted $3,000.00 for Offerings (2026)")

	if rec := e.owner.get(budget + "?year=abc"); rec.Code != http.StatusNotFound {
		t.Errorf("a year that is not one: %d", rec.Code)
	}

	// A viewer reads it; only an owner sets it or moves the year.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	if body := viewer.get(budget + "?year=2026").Body.String(); !strings.Contains(body, "$120.00 of $1,200.00 spent") || strings.Contains(body, "Save the budget") || strings.Contains(body, "Budget year starts in") {
		t.Errorf("the viewer's view:\n%s", body)
	}

	for path, form := range map[string]url.Values{
		budget + "?year=2026":      {"currency": {"USD"}, "expense-total": {"1"}},
		budget + "/copy?year=2028": nil,
		budget + "/year-start":     {"month": {"1"}},
	} {
		if rec := viewer.post(path, form); rec.Code != http.StatusForbidden {
			t.Errorf("a viewer: POST %s = %d", path, rec.Code)
		}
	}
}
