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

// World Youth Day's report: its parts with the balance after each, in one
// run at thirty days of quiet and two at ten; pass-through apart from
// income and expenses; the balance brought into a later period; its parts
// behind the foot's figures; the spreadsheet; and saved with its gap.
func TestAProjectsReport(t *testing.T) {
	t.Parallel()

	e, txs, _, _ := sorted(t)

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Offerings"}, "kind": {"income"}}), e.org+"/categories?done=added")
	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Lent and repaid"}, "kind": {"passthrough"}}), e.org+"/categories?done=added")

	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	for name, category := range map[string]string{
		"CORNER GROCERY": choices["Groceries"], "COFFEE CART": choices["Lent and repaid"],
		"PARISH OFFERTORY": choices["Offerings"], "ELECTRIC CO": choices["Utilities"],
	} {
		wantRedirect(t, e.owner.post(txs[name], url.Values{
			"action": {"save"}, "amount-0": {""}, "category-0": {category}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
		}), e.account+"/transactions?month=2026-07&done=sorted")
	}

	report := e.project + "/reports/project"
	wantBody(t, e.owner.get(e.project), `href="`+report+`"`)
	wantBody(t, e.owner.get(e.project+"/reports"), "A project, with its running balance", `href="`+report+`"`)

	// An ordinary project: its parts and their balance, and no runs, and
	// no gap to choose.
	ordinary := e.owner.get(report)
	wantBody(t, ordinary, "-$37.49", "$212.51", "Income $250.00 · Expenses -$153.99 · Net $96.01", "The balance at the end is $92.51.",
		"A break-even project, such as a retreat, is also shown in runs")

	if body := ordinary.Body.String(); strings.Contains(body, "<h3>2026") || strings.Contains(body, `name="gap" type="number"`) || strings.Contains(body, "On its own it came to") {
		t.Errorf("an ordinary project's report is read in runs:\n%s", body)
	}

	wantRedirect(t, e.owner.post(e.project+"/edit", url.Values{"name": {"World Youth Day"}, "starts": {"2027-08-01"}, "break-even": {"1"}}), e.project+"?done=saved")

	wantBody(t, e.owner.get(report), "World Youth Day", "From its first transaction to its last", "2026-07-03 to 2026-07-28",
		"CORNER GROCERY", "-$33.99", "-$37.49", "$212.51",
		"Income $250.00 · Expenses -$153.99 · Net $96.01",
		"Pass-through: $0.00 in, $3.50 out · <strong>$3.50 still to come back</strong>",
		"On its own it came to $92.51; the balance after it is $92.51.", "The balance at the end is $92.51.")

	// Ten days of quiet: the 10th and the 28th are two runs.
	page := e.owner.get(report + "?period=all&gap=10").Body.String()
	if !strings.Contains(page, "2026-07-03 to 2026-07-10") || !strings.Contains(page, "<h3>2026-07-28</h3>") {
		t.Errorf("with ten days of quiet:\n%s", page)
	}

	wantBody(t, e.owner.get(report+"?from=2026-08&to=2026-09"), "Brought forward from before these months: $92.51", "The balance at the end is $92.51.")

	income := report + "/parts?" + url.Values{"period": {"all"}, "by": {"kind"}, "kind": {"income"}, "currency": {"USD"}}.Encode()
	if !strings.Contains(e.owner.get(report).Body.String(), "by=kind&amp;currency=USD&amp;kind=income") {
		t.Error("the foot's income does not open its parts")
	}

	if body := e.owner.get(income).Body.String(); !strings.Contains(body, "PARISH OFFERTORY") || strings.Contains(body, "CORNER GROCERY") {
		t.Errorf("the project's income:\n%s", body)
	}

	rec := e.owner.get(report + "/download")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "2026-07-03 – 2026-07-28,2026-07-28,Parish checking,ELECTRIC CO,Utilities,-120.00,92.51") ||
		!strings.Contains(rec.Body.String(), "Balance at the end,,,,,,92.51") {
		t.Errorf("the spreadsheet: %d\n%s", rec.Code, rec.Body.String())
	}

	wantBody(t, e.owner.get(report+"?from=2026-13&to=July"), "so the whole project is shown")

	// The monthly style is the books', not a project's.
	if rec := e.owner.get(e.project + "/reports/income-expenses-by-month"); rec.Code != http.StatusNotFound {
		t.Errorf("a project by month = %d, want 404", rec.Code)
	}

	saved := saveReport(t, e.owner, e.project, url.Values{"style": {"project"}, "title": {"Youth Day"}, "gap": {"10"}, "default": {"all"}})
	wantBody(t, e.owner.get(saved), "Youth Day", "A project, with its running balance", "2026-07-03 to 2026-07-10", "<h3>2026-07-28</h3>")
	wantBody(t, e.owner.get(saved+"/edit"), `name="gap" type="number" min="1" max="365" value="10"`)

	rec = e.owner.post(saved+"/edit", url.Values{"title": {"Youth Day"}, "gap": {"a fortnight"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "The quiet gap is a number of days, from 1 to 365.") {
		t.Errorf("a gap that is not a number: %d\n%s", rec.Code, rec.Body.String())
	}

	wantRedirect(t, e.owner.post(saved+"/edit", url.Values{"title": {"Youth Day"}, "gap": {"30"}, "accounts": {"hidden"}}), saved+"?done=changed")

	page = e.owner.get(saved).Body.String()
	if !strings.Contains(page, "2026-07-03 to 2026-07-28") || strings.Contains(page, "<th scope=\"col\">Account</th>") {
		t.Errorf("changed to thirty days and no accounts:\n%s", page)
	}

	wantRedirect(t, e.owner.post(saved+"/note", url.Values{"period": {"all"}, "note": {"The electric bill was the youth hall's."}}), saved+"?done=noted&period=all#note")
	wantBody(t, e.owner.get(saved), "The electric bill was the youth hall&#39;s.")
}
