package muxer

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A bookkeeper marks a payee to be reported, with its papers and a note;
// the year's payments by payee put it first, count only what is an
// expense or not sorted yet, and download as a spreadsheet; the
// accountant's package says the reported payee; and Claude reads the
// marks and the year, but cannot set them.
func TestReportedPayeesAndTheYear(t *testing.T) {
	t.Parallel()

	k := newKept(t)
	s := k.s
	org := strings.TrimPrefix(k.e.org, "/orgs/")

	july := k.txByDescription(t, "2026-07")
	grocery, electric := july["CORNER GROCERY"]["id"].(string), july["ELECTRIC CO"]["id"].(string)

	wantRedirect(t, k.e.owner.post("/transactions/"+grocery+"/payee", url.Values{"payee": {"Corner Grocery"}}), "/transactions/"+grocery+"?done=payee#paid-to")
	wantRedirect(t, k.e.owner.post("/transactions/"+electric+"/payee", url.Values{"payee": {"City Power"}}), "/transactions/"+electric+"?done=payee#paid-to")

	// The power bill is a utility, an expense; the grocery not sorted yet.
	s.change(t, http.MethodPost, "/api/v1/accounts/"+k.account+"/sort", k.key,
		map[string]any{"choices": []map[string]string{{"transaction": electric, "category": k.categories["Utilities"]}}}, http.StatusOK)

	page := k.e.owner.get("/transactions/" + electric).Body.String()
	m := payeeLink.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no payee on the payment:\n%s", page)
	}

	power := m[1]

	wantBody(t, k.e.owner.get(power), "For the accountant", "Report this payee", "Such as a W-9 in the United States.")
	wantRedirect(t, k.e.owner.post(power+"/marks", url.Values{"reported": {"1"}, "note": {"Form received in March."}}), power+"?done=marked")
	wantBody(t, k.e.owner.get(k.e.org+"/payees"), "reported", "Payments by payee")
	wantBody(t, k.e.owner.get(k.e.org), "marked the payee City Power to be reported to the accountant", "wrote a note on the payee City Power")

	if rec := k.e.owner.post(power+"/marks", url.Values{"note": {strings.Repeat("x", 501)}}); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "problem=note") {
		t.Errorf("a long note: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// The year: the reported payee first, then the grocery, unsorted.
	year := k.e.owner.get(k.e.org + "/payees/2026")
	wantBody(t, year, "Payments by payee, 2026", "Reported to the accountant", "City Power", "Everybody else", "Corner Grocery", "not sorted yet", "no tax details on file")

	if body := year.Body.String(); strings.Index(body, "City Power") > strings.Index(body, "Corner Grocery") {
		t.Error("the reported payee is not first")
	}

	wantRedirect(t, k.e.owner.get(k.e.account+"/payees/2026"), k.e.org+"/payees/2026")
	wantBody(t, k.e.owner.get(k.e.org+"/payees/2025"), "Nothing paid this year.", "Nobody else was paid this year.")

	if rec := k.e.owner.get(k.e.org + "/payees/12"); rec.Code != http.StatusNotFound {
		t.Errorf("a year from long ago: %d", rec.Code)
	}

	dl := k.e.owner.get(k.e.org + "/payees/2026/download")
	if dl.Code != http.StatusOK || !strings.HasPrefix(dl.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("the download: %d %s", dl.Code, dl.Header())
	}

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(dl.Body.String(), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 3 || len(rows[0]) != 13 || rows[0][0] != "Payee" {
		t.Fatalf("the spreadsheet: %q, %v", rows, err)
	}

	if r := rows[1]; r[0] != "City Power" || r[1] != "yes" || r[2] != "" || r[4] != "day posted" || r[9] != "-120.00" || r[10] != "0.00" {
		t.Errorf("the reported payee's row: %q", r)
	}

	if r := rows[2]; r[0] != "Corner Grocery" || r[9] != "0.00" || r[10] != "-33.99" {
		t.Errorf("the grocery's row: %q", r)
	}

	// The accountant's package names the reported payee in its last column.
	pkg := k.e.owner.get(k.e.account + "/export?from=2026-07&to=2026-07")
	if !strings.Contains(unzipped(t, pkg.Body.Bytes())["transactions.csv"], ",City Power\r\n") {
		t.Error("the package has no reported payee")
	}

	// Claude reads the marks and the year.
	listed := list(t, s.get(t, "/api/v1/payees?organization="+org+"&year=2026", k.key), "payees")
	for _, p := range listed {
		if field(p, "name") == "City Power" && (field(p, "reported") != true || field(p, "note") != "Form received in March." || field(p, "year", "payments") != 1.0) {
			t.Errorf("City Power listed: %v", p)
		}
	}

	paid := s.get(t, "/api/v1/payees/"+strings.TrimPrefix(power, "/payees/")+"/payments?year=2026", k.key)
	if ps := list(t, paid, "payments"); len(ps) != 1 || field(ps[0], "date_is") != "posted" || field(ps[0], "paid") != "-120.00" || field(paid, "sums").([]any)[0].(map[string]any)["paid"] != "-120.00" {
		t.Errorf("get_payee_payments: %v", paid)
	}

	if rec := s.api(http.MethodGet, "/api/v1/payees/"+strings.TrimPrefix(power, "/payees/")+"/payments", k.key, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("payments with no year: %d", rec.Code)
	}
}
