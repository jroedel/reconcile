package muxer

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// unzipped is a download's files by name.
func unzipped(t *testing.T, body []byte) map[string]string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}

	out := map[string]string{}

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}

		data, err := io.ReadAll(rc)
		rc.Close()

		if err != nil {
			t.Fatal(err)
		}

		out[f.Name] = string(data)
	}

	return out
}

func sheet(t *testing.T, files map[string]string) [][]string {
	t.Helper()

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(files["transactions.csv"], "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("the spreadsheet: %v", err)
	}

	return rows
}

// The month's package, as the accountant gets it: a row per part, the
// receipt under a name that says what it is, the statement it all came
// from, and which rows are in a reconciled period. Then the project's,
// which has only the project's part.
func TestTheAccountantsPackage(t *testing.T) {
	e, txs, _, signUpAs := sorted(t)
	grocery := txs["CORNER GROCERY"]

	choices := options(e.owner.get(grocery).Body.String())
	wantRedirect(t, e.owner.post(grocery, url.Values{
		"action":   {"save"},
		"amount-0": {"30.00"}, "category-0": {choices["Groceries"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
		"amount-1": {"3.99"}, "category-1": {choices["Utilities"]}, "project-1": {""}, "memo-1": {"=1+1"},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	e.owner.receipts(grocery+"/receipts", nil, [2]string{"IMG_0001.jpg", photoOf("grocery")})

	statement := statementLink.FindStringSubmatch(e.owner.get(e.account + "/transactions").Body.String())[1]
	wantRedirect(t, e.owner.post(statement+"/reconcile", url.Values{"from": {"2026-07-01"}, "to": {"2026-07-31"}}), statement+"?done=reconciled")

	wantBody(t, e.owner.get(e.account+"/months"), "For the accountant", `action="`+e.account+`/export"`)
	wantBody(t, e.owner.get(statement), e.account+"/export?from=2026-07-01&amp;to=2026-07-31")

	rec := e.owner.get(e.account + "/export?from=2026-07&to=2026-07")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename=parish-checking_2026-07-01_2026-07-31.zip` {
		t.Fatalf("the download: %d %q %q\n%s", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"), rec.Body.String())
	}

	files := unzipped(t, rec.Body.Bytes())

	const photo = "receipts/2026-07-03_33.99_corner-grocery_1.jpg"

	if files[photo] != photoOf("grocery") {
		t.Errorf("the receipt is not in the zip as %s: %v", photo, slices.Collect(maps.Keys(files)))
	}

	if files["statements/2026-07-01_2026-07-31_checking-july.csv"] != july {
		t.Errorf("the statement is not in the zip: %v", slices.Collect(maps.Keys(files)))
	}

	rows := sheet(t, files)
	if len(rows) != 8 || rows[0][0] != "Date" || rows[0][11] != "Reconciled on" {
		t.Fatalf("%d rows, header %q", len(rows), rows[0])
	}

	var parts [][]string

	for _, r := range rows[1:] {
		if r[11] == "" {
			t.Errorf("a July row is not marked reconciled: %q", r)
		}

		if r[2] == "CORNER GROCERY" {
			parts = append(parts, r)
		}
	}

	if len(parts) != 2 {
		t.Fatalf("the grocery has %d rows", len(parts))
	}

	if p := parts[0]; p[3] != "-30.00" || p[5] != "-33.99" || p[6] != "Groceries" || p[7] != "World Youth Day" || p[9] != photo || p[10] != "checking-july.csv" {
		t.Errorf("the grocery's first part: %q", p)
	}

	if p := parts[1]; p[3] != "-3.99" || p[8] != "'=1+1" || p[9] != photo {
		t.Errorf("the grocery's second part: %q", p)
	}

	// The project's package: its part, its receipt, and nothing of the
	// account it came from but the part.
	rec = e.owner.get(e.project + "/export")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "world-youth-day.zip") {
		t.Fatalf("the project's download: %d %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}

	files = unzipped(t, rec.Body.Bytes())
	if rows := sheet(t, files); len(rows) != 2 || rows[1][3] != "-30.00" || rows[1][9] != photo || rows[1][10] != "" {
		t.Errorf("the project's spreadsheet: %q", rows)
	}

	for name := range files {
		if strings.HasPrefix(name, "statements/") {
			t.Errorf("the project's package holds %s", name)
		}
	}

	wantBody(t, e.owner.get(e.project+"/book"), e.project+"/export")

	// A period that ends before it starts is said, not zipped.
	if rec := e.owner.get(e.account + "/export?from=2026-08&to=2026-07"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a backwards period: %d", rec.Code)
	}

	// The accountant downloads; a contributor and a viewer do not.
	for role, want := range map[string]int{"accountant": http.StatusOK, "contributor": http.StatusForbidden, "viewer": http.StatusForbidden} {
		addr := role + "@example.org"
		wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {addr}, "role": {role}}), e.org+"?done=granted")
		b := signUpAs(addr)

		for _, path := range []string{e.account + "/export?from=2026-07&to=2026-07", e.project + "/export"} {
			if rec := b.get(path); rec.Code != want {
				t.Errorf("%s: GET %s = %d, want %d", role, path, rec.Code, want)
			}
		}

		if body := b.get(e.account + "/months").Body.String(); strings.Contains(body, "For the accountant") != (want == http.StatusOK) {
			t.Errorf("%s is offered the download: %v", role, !(want == http.StatusOK))
		}
	}
}
