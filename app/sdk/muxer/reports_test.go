package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The organization's income and expenses by month: the offertory as income
// and the groceries as an expense, the rest not sorted yet; every figure
// opens what it adds up; the same report as a spreadsheet; a viewer reads
// it, and a period that is not one says so.
func TestIncomeAndExpensesByMonth(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Offerings"}, "kind": {"income"}}), e.org+"/categories?done=added")

	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	for name, category := range map[string]string{"CORNER GROCERY": choices["Groceries"], "PARISH OFFERTORY": choices["Offerings"]} {
		wantRedirect(t, e.owner.post(txs[name], url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {category}, "project-0": {""}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	report := e.org + "/reports/income-expenses-by-month"
	wantBody(t, e.owner.get(e.org), `href="`+report+`"`)
	wantBody(t, e.owner.get(e.org+"/reports"), "Income and expenses by month", `href="`+report+`"`)

	july := report + "?from=2026-07&to=2026-07"
	wantBody(t, e.owner.get(july), "Jul 2026", "Offerings", "$250.00", "Groceries", "-$33.99", "Net", "$216.01",
		"Not sorted yet", "$873.00", "Pass-through still owed at the end: $0.00", "Transfers between your accounts come back to zero.")

	parts := report + "/parts?" + url.Values{
		"from": {"2026-07"}, "to": {"2026-07"}, "by": {"kind,month"}, "kind": {"expense"}, "month": {"2026-07"}, "currency": {"USD"},
	}.Encode()
	if body := e.owner.get(parts).Body.String(); !strings.Contains(body, "CORNER GROCERY") || strings.Contains(body, "PARISH OFFERTORY") {
		t.Errorf("July's expenses:\n%s", body)
	}

	if !strings.Contains(e.owner.get(july).Body.String(), "by=kind%2Cmonth") {
		t.Error("the report's figures do not open their parts")
	}

	rec := e.owner.get(report + "/download?from=2026-07&to=2026-07")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Body.String(), "Groceries,-33.99,-33.99") || !strings.Contains(rec.Body.String(), "Not sorted yet,873.00,873.00") {
		t.Errorf("the spreadsheet: %d %q\n%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	wantBody(t, e.owner.get(report+"?from=2026-13&to=July"), "That period could not be read")

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	wantBody(t, signUpAs("viewer@example.org").get(july), "$216.01")

	// An account in an organization is reported in its organization's
	// reports, and a style nobody wrote is nothing.
	for _, path := range []string{e.account + "/reports", e.org + "/reports/weekly"} {
		if rec := e.owner.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}

	// An account on its own is its own books.
	wantBody(t, e.owner.get(e.ownAccount+"/reports/income-expenses-by-month"), "Income and expenses by month", "No money in these months.")
}
