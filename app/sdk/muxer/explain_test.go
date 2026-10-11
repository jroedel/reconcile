package muxer

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Explaining an amount through the site (docs/clearing.md, 2): a Sunday's
// offertory, deposited as one sum, explained by the count sheets of its
// two Masses, entered by hand with their documents. Everything invented.
func TestExplainingAnAmount(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)
	offertory, grocery := txs["PARISH OFFERTORY"], txs["CORNER GROCERY"]
	accountID := strings.TrimPrefix(e.account, "/accounts/")

	wantBody(t, e.owner.get(offertory), "Explain this amount", offertory+"/explain")
	wantBody(t, e.owner.get(offertory+"/explain"), "Explain this amount", "Nothing yet.", "Parish checking", "Add what no statement has")

	sheet := func(desc, amount string) {
		t.Helper()

		fields := url.Values{"account": {accountID}, "date": {"2026-07-10"}, "description": {desc}, "amount": {amount}, "direction": {"in"}}
		wantRedirect(t, e.owner.entry(offertory+"/explain/entries", fields, "count-sheet.pdf", ticket), offertory+"/explain?done=entered-line")
	}

	sheet("Count sheet, early Mass", "100.00")
	wantBody(t, e.owner.get(offertory+"/explain"), "1 lines come to $100.00", "the difference is $150.00", ">open<")

	sheet("Count sheet, late Mass", "150.00")
	wantBody(t, e.owner.get(offertory+"/explain"), "2 lines come to $250.00", ">explained<")

	// The month says which is explained and which are its lines.
	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), ">explained<", "cleared 2026-07-10")

	// Gathering from the account's month offers what no explanation has,
	// the count sheets not among them.
	look := e.owner.get(offertory + "/explain?account=" + accountID + "&from=2026-07&to=2026-07").Body.String()
	if !strings.Contains(look, "CORNER GROCERY") || strings.Contains(look, `name="add" value="`+strings.TrimPrefix(offertory, "/transactions/")) ||
		strings.Count(look, "Count sheet") != 2 {
		t.Errorf("the offer:\n%s", look)
	}

	groceryID := strings.TrimPrefix(grocery, "/transactions/")
	gather := url.Values{"account": {accountID}, "from": {"2026-07"}, "to": {"2026-07"}, "add": {groceryID}}

	rec := e.owner.post(offertory+"/explain/lines", gather)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "done=gathered") {
		t.Fatalf("gathering: %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	wantBody(t, e.owner.get(offertory+"/explain"), "3 lines come to $216.01")
	wantBody(t, e.owner.get(grocery), "Part of what explains PARISH OFFERTORY, $250.00 on 2026-07-10")

	// Taken out again, and settled with a note the history keeps.
	e.owner.post(offertory+"/explain/lines", url.Values{"remove": {groceryID}})
	wantBody(t, e.owner.get(offertory+"/explain"), "2 lines come to $250.00")

	wantRedirect(t, e.owner.post(offertory+"/explain/settle", url.Values{"note": {"both count sheets signed"}}), offertory+"/explain?done=settled")
	wantBody(t, e.owner.get(e.account), "changed what explains PARISH OFFERTORY on 2026-07-10: 1 lines added, 0 taken out",
		"wrote a note on what explains PARISH OFFERTORY on 2026-07-10: “both count sheets signed”")

	// The accountant's package says what each line is cleared by, and
	// lists the explanation.
	files := unzipped(t, e.owner.get(e.account+"/export?from=2026-07&to=2026-07").Body.Bytes())

	var cleared int

	for _, r := range sheetOf(t, files["transactions.csv"]) {
		if r[13] == "2026-07-10 250.00 PARISH OFFERTORY" {
			cleared++
		}
	}

	rows := sheetOf(t, files["explanations.csv"])
	if cleared != 2 || len(rows) != 3 || rows[1][2] != "PARISH OFFERTORY" || rows[1][8] != "0.00" || rows[1][10] != "both count sheets signed" {
		t.Errorf("%d rows cleared; explanations.csv: %q", cleared, rows)
	}

	// A stranger gets nothing from any of it; a viewer reads, and changes
	// nothing.
	stranger := signUpAs("stranger@example.org")

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	if body := viewer.get(offertory + "/explain").Body.String(); !strings.Contains(body, "2 lines come to $250.00") || strings.Contains(body, "<h2>Gather</h2>") {
		t.Errorf("the viewer's page:\n%s", body)
	}

	for who, want := range map[*browser]int{stranger: http.StatusNotFound, viewer: http.StatusForbidden} {
		if who == stranger {
			if rec := who.get(offertory + "/explain"); rec.Code != want {
				t.Errorf("a stranger: GET = %d", rec.Code)
			}
		}

		for path, form := range map[string]url.Values{
			offertory + "/explain/lines":  gather,
			offertory + "/explain/settle": {"note": {"mine"}, "accept": {"1"}},
		} {
			if rec := who.post(path, form); rec.Code != want {
				t.Errorf("POST %s = %d, want %d", path, rec.Code, want)
			}
		}

		fields := url.Values{"account": {accountID}, "date": {"2026-07-10"}, "description": {"x"}, "amount": {"1.00"}, "direction": {"in"}}
		if rec := who.entry(offertory+"/explain/entries", fields, "x.pdf", ticket); rec.Code != want {
			t.Errorf("an entry = %d, want %d", rec.Code, want)
		}
	}
}

// sheetOf is a CSV of the package, without its byte-order mark.
func sheetOf(t *testing.T, data string) [][]string {
	t.Helper()

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(data, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("the spreadsheet: %v", err)
	}

	return rows
}
