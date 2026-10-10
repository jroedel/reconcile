package muxer

import (
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// A CSV with a column of check numbers: the preview finds the column and
// shows each check's number where its description does not say it, the
// import keeps it, and the month and the transaction show it.
func TestCheckNumbersFromACSV(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	const file = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
		"2026-07-02,CHECK,-120.00,880.00,0001176\n" +
		"2026-07-03,Check 1177,-30.00,850.00,1177\n"

	preview := uploaded(t, e.owner, e.account, "july.csv", file)
	wantBody(t, e.owner.get(preview), `<option value="Check Number" selected>`, "Check number 1176")

	form := columns("import")
	form.Set("check", "Check Number")

	rec := e.owner.post(preview, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d\n%s", rec.Code, rec.Body.String())
	}

	month := e.owner.get(e.account + "/transactions?month=2026-07").Body.String()

	// Once beside the bare "CHECK", and not beside "Check 1177", which
	// says it already.
	if strings.Count(month, "Check number 1176") != 1 || strings.Contains(month, "Check number 1177") {
		t.Errorf("the month shows the check numbers wrongly:\n%s", month)
	}

	link := regexp.MustCompile(`/transactions/[0-9a-f]+`).FindAllString(month, -1)

	var seen bool

	for _, l := range link {
		if body := e.owner.get(l).Body.String(); strings.Contains(body, "Check number 1177") {
			seen = true
		}
	}

	if !seen {
		t.Errorf("no transaction page shows check 1177's number")
	}

}

// A check's front and back, named after it, uploaded to the account: one
// receipt attached to the check, whom it was paid to beside the check in
// the month, and both sides in the accountant's checks/ folder. A number
// the account has no check for is refused with nothing added, and a
// stranger and a viewer may not upload at all.
func TestCheckImages(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	const file = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
		"2026-07-02,CHECK,-120.00,880.00,0001176\n"

	preview := uploaded(t, e.owner, e.account, "july.csv", file)
	form := columns("import")
	form.Set("check", "Check Number")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d", rec.Code)
	}

	wantBody(t, e.owner.get(e.account+"/receipts"), "Check images", `action="`+e.account+`/checks"`)

	rec := e.owner.receipts(e.account+"/checks", url.Values{"payee": {"Hilltop Plumbing"}},
		[2]string{"1176-front.jpg", photoOf("front")}, [2]string{"1176-back.jpg", photoOf("back")})

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc, "done=checks") {
		t.Fatalf("upload: %d to %q", rec.Code, loc)
	}

	inbox := e.owner.get(loc)
	wantBody(t, inbox, "1 check images added", "Check 1176 · paid to Hilltop Plumbing", "2 pages")

	if strings.Contains(inbox.Body.String(), "receipts added") {
		t.Error("the check images were also announced as receipts that want a date")
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), "Paid to Hilltop Plumbing", "Check number 1176")
	wantBody(t, e.owner.get(e.account), "wrote that CHECK on 2026-07-02 was paid to Hilltop Plumbing")

	// No check 1190 in the account.
	rec = e.owner.receipts(e.account+"/checks", nil, [2]string{"1190.jpg", photoOf("1190")})
	wantBody(t, e.owner.get(rec.Header().Get("Location")), "No transaction in this account is check 1190, so nothing was added")

	// A phone's name, and no number typed.
	rec = e.owner.receipts(e.account+"/checks", nil, [2]string{"IMG_4521.jpg", photoOf("phone")})
	wantBody(t, e.owner.get(rec.Header().Get("Location")), "Say the check&#39;s number")

	zipped := e.owner.get(e.account + "/export?from=2026-07&to=2026-07")
	files := unzipped(t, zipped.Body.Bytes())

	for _, want := range []string{"checks/1176_2026-07-02_120.00_hilltop-plumbing_1.jpg", "checks/1176_2026-07-02_120.00_hilltop-plumbing_2.jpg"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the package lacks %s: %v", want, slices.Sorted(maps.Keys(files)))
		}
	}

	if sheet := files["transactions.csv"]; !strings.Contains(sheet, "1176,Hilltop Plumbing") {
		t.Errorf("the spreadsheet:\n%s", sheet)
	}

	stranger := signUp(t, h, sent, "stranger@example.org")
	if rec := stranger.receipts(e.account+"/checks", nil, [2]string{"1176.jpg", photoOf("x")}); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's upload: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(e.account+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.account+"?done=granted")
	viewer := signUp(t, h, sent, "viewer@example.org")

	if rec := viewer.receipts(e.account+"/checks", nil, [2]string{"1176.jpg", photoOf("x")}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer's upload: %d", rec.Code)
	}

	if strings.Contains(viewer.get(e.account+"/receipts").Body.String(), `action="`+e.account+`/checks"`) {
		t.Error("a viewer is offered the check images form")
	}
}
