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

// saveReport saves the organization's income and expenses as a report and
// returns its path.
func saveReport(t *testing.T, b *browser, org string, form url.Values) string {
	t.Helper()

	rec := b.post(org+"/reports", form)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/reports/") || !strings.HasSuffix(loc, "?done=saved") {
		t.Fatalf("saving a report: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	return strings.TrimSuffix(loc, "?done=saved")
}

// The treasurer saves the report as "P&L" with a description, writes
// July's note, sees it on July and nowhere else, and is told when sorting
// changes July's figures under it; renames it, and the history says so;
// a viewer reads it and writes nothing; removed, it is gone.
func TestASavedReport(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)

	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())
	wantRedirect(t, e.owner.post(txs["CORNER GROCERY"], url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Groceries"]}, "project-0": {""}, "memo-0": {""},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	july := "from=2026-07&to=2026-07"
	style := e.org + "/reports/income-expenses-by-month"
	wantBody(t, e.owner.get(style+"?"+july), "Save this report")

	if rec := e.owner.post(e.org+"/reports", url.Values{"style": {"income-expenses-by-month"}, "title": {" "}, "query": {july}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Give the report a title") {
		t.Errorf("no title: %d", rec.Code)
	}

	report := saveReport(t, e.owner, e.org, url.Values{
		"style": {"income-expenses-by-month"}, "title": {"P&L"}, "description": {"Our true income and expenses."},
		"default": {"last-month"}, "passthrough": {"total"}, "query": {july},
	})

	wantBody(t, e.owner.get(e.org+"/reports"), "P&amp;L", `href="`+report+`"`)
	wantBody(t, e.owner.get(report+"?"+july), "<h1>P&amp;L</h1>", "Income and expenses by month", "Our true income and expenses.",
		"-$33.99", "A note for these months")

	wantRedirect(t, e.owner.post(report+"/note", url.Values{"from": {"2026-07"}, "to": {"2026-07"}, "note": {"The roof repair is in August."}}),
		report+"?done=noted&from=2026-07&to=2026-07#note")

	wantBody(t, e.owner.get(report+"?"+july), "The roof repair is in August.", "Written by")

	if body := e.owner.get(report + "?from=2026-07&to=2026-08").Body.String(); strings.Contains(body, "The roof repair") {
		t.Error("July's note is on July and August's report")
	}

	// The electric bill is sorted after the note was written.
	wantRedirect(t, e.owner.post(txs["ELECTRIC CO"], url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Utilities"]}, "project-0": {""}, "memo-0": {""},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	wantBody(t, e.owner.get(report+"?"+july), "The figures have changed since this note was written")

	wantBody(t, e.owner.get(report+"/edit"), `value="P&amp;L"`, "Our true income and expenses.")
	wantRedirect(t, e.owner.post(report+"/edit", url.Values{"title": {"Profit and loss"}, "default": {"last-month"}, "passthrough": {"each"}}),
		report+"?done=changed")
	wantBody(t, e.owner.get(e.org), "renamed the report P&amp;L to Profit and loss", "saved the report P&amp;L",
		"wrote a note on the report P&amp;L for 2026-07 to 2026-07: The roof repair is in August.")

	if rec := e.owner.get(report + "/download?" + july); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Utilities,-120.00") {
		t.Errorf("its spreadsheet: %d\n%s", rec.Code, rec.Body.String())
	}

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	if body := viewer.get(report + "?" + july).Body.String(); !strings.Contains(body, "Profit and loss") || strings.Contains(body, "A note for these months") {
		t.Errorf("the viewer's report:\n%s", body)
	}

	for _, w := range []struct {
		path string
		form url.Values
	}{
		{report + "/note", url.Values{"from": {"2026-07"}, "to": {"2026-07"}, "note": {"Mine"}}},
		{report + "/edit", url.Values{"title": {"Mine"}}},
		{report + "/remove", nil},
		{e.org + "/reports", url.Values{"style": {"income-expenses-by-month"}, "title": {"Mine"}}},
	} {
		if rec := viewer.post(w.path, w.form); rec.Code != http.StatusForbidden {
			t.Errorf("the viewer: POST %s = %d, want 403", w.path, rec.Code)
		}
	}

	wantRedirect(t, e.owner.post(report+"/remove", nil), e.org+"/reports?done=removed")

	if rec := e.owner.get(report); rec.Code != http.StatusNotFound {
		t.Errorf("a removed report: %d", rec.Code)
	}
}
