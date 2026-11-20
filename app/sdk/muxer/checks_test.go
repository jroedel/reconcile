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
	t.Parallel()

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
// the account has no check for, and a screenshot that says no number,
// wait in the inbox; the screenshot is numbered on its page, which
// attaches it. A stranger and a viewer may not upload or number at all.
func TestCheckImages(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	const file = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
		"2026-07-02,CHECK,-120.00,880.00,0001176\n" +
		"2026-07-09,Check 1177,-30.00,850.00,1177\n"

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

	// No check 1190 in the account, and a screenshot: both wait.
	rec = e.owner.receipts(e.account+"/checks", nil,
		[2]string{"1190.jpg", photoOf("1190")}, [2]string{"Screenshot_20260712-101500.png", photoOf("phone")})
	inbox = e.owner.get(rec.Header().Get("Location"))
	wantBody(t, inbox, "2 check images are waiting below", "Check 1190", "waiting for the transaction that paid it", "A check", "its number not yet said")

	if strings.Contains(inbox.Body.String(), "Screenshot_") {
		t.Error("a waiting check is named by its file")
	}

	// The screenshot, numbered on its page.
	var shot string

	for _, rc := range receiptsOn(inbox.Body.String()) {
		if strings.Contains(e.owner.get(rc).Body.String(), "whose number has not been said") {
			shot = rc
		}
	}

	if shot == "" {
		t.Fatal("no page for the screenshot")
	}

	wantBody(t, e.owner.get(shot), `action="`+shot+`/number"`)

	if rec := e.owner.post(shot+"/number", url.Values{"number": {"soon"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a number with no digits: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(shot+"/number", url.Values{"number": {"1177"}}), shot+"?done=attached")
	wantBody(t, e.owner.get(shot), "Check 1177", "attached to the transaction that paid it by its number")

	// Check 1190 clears: the import of its statement attaches the image
	// that was waiting for it.
	august := uploaded(t, e.owner, e.account, "august.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-08-03,CHECK,-50.00,800.00,1190\n")
	form = columns("import")
	form.Set("check", "Check Number")

	if rec := e.owner.post(august, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("importing August: %d", rec.Code)
	}

	if body := e.owner.get(e.account + "/receipts").Body.String(); strings.Contains(body, "waiting for the transaction that paid it") {
		t.Error("check 1190 still waits after its statement was imported")
	}

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

	if rec := viewer.post(shot+"/number", url.Values{"number": {"1177"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer numbering a check: %d", rec.Code)
	}

	if rec := stranger.post(shot+"/number", url.Values{"number": {"1177"}}); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger numbering a check: %d", rec.Code)
	}
}

// A screenshot numbered on its page with the number of a check already
// waiting becomes more pages of that check: the person is taken to the
// check, which says so, and the account's history has the line.
func TestACheckNumberedTwiceIsOneCheck(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	rec := e.owner.receipts(e.account+"/checks", nil,
		[2]string{"1190.jpg", photoOf("1190")}, [2]string{"Screenshot_20260712-101500.png", photoOf("phone")})
	inbox := e.owner.get(rec.Header().Get("Location"))

	var check, shot string

	for _, rc := range receiptsOn(inbox.Body.String()) {
		if strings.Contains(e.owner.get(rc).Body.String(), "whose number has not been said") {
			shot = rc
		} else {
			check = rc
		}
	}

	if shot == "" || check == "" {
		t.Fatalf("the check %q and the screenshot %q", check, shot)
	}

	wantRedirect(t, e.owner.post(shot+"/number", url.Values{"number": {"1190"}}), check+"?done=joined")
	wantBody(t, e.owner.get(check+"?done=joined"), "This check was already waiting here, so the image was added to it as more pages.", "Check 1190")
	wantBody(t, e.owner.get(e.account+"/receipts"), "2 pages")
	wantBody(t, e.owner.get(e.account), "added an image to check 1190, which was already waiting, as more of its pages")
}

// A waiting check is voided on its page, and listed apart; the mark comes
// off the same way. A viewer may not, and a stranger finds nothing.
func TestVoidingACheckOnItsPage(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	rec := e.owner.receipts(e.account+"/checks", nil, [2]string{"1190.jpg", photoOf("1190")})
	inbox := e.owner.get(rec.Header().Get("Location"))

	checks := receiptsOn(inbox.Body.String())
	if len(checks) != 1 {
		t.Fatalf("the checks: %v", checks)
	}

	check := checks[0]

	wantBody(t, e.owner.get(check), `action="`+check+`/void"`, "Void this check")
	wantRedirect(t, e.owner.post(check+"/void", url.Values{"voided": {"1"}}), check+"?done=voided")
	wantBody(t, e.owner.get(check+"?done=voided"), "Voided. It is kept here with its number", "The image of a check that was voided", "It is not void")
	wantBody(t, e.owner.get(e.account+"/receipts"), "Void checks", "Check 1190", ">void<")
	wantBody(t, e.owner.get(e.account), "voided check 1190")

	wantRedirect(t, e.owner.post(check+"/void", url.Values{"voided": {"0"}}), check+"?done=unvoided")
	wantBody(t, e.owner.get(e.account+"/receipts"), "waiting for the transaction that paid it")

	wantRedirect(t, e.owner.post(e.account+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.account+"?done=granted")
	viewer := signUp(t, h, sent, "viewer@example.org")

	if rec := viewer.post(check+"/void", url.Values{"voided": {"1"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer voiding: %d", rec.Code)
	}

	stranger := signUp(t, h, sent, "stranger@example.org")
	if rec := stranger.post(check+"/void", url.Values{"voided": {"1"}}); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger voiding: %d", rec.Code)
	}
}

// A statement's checks outstanding are those that were out when it ended,
// and stay so: check 1190, written in July and paid in August, is on
// July's statement before August is imported and after, saying when it
// cleared, while the account's months, which are about now, no longer
// list it.
func TestAStatementKeepsItsChecksOutstanding(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	importChecks := func(name, file string) string {
		t.Helper()

		form := columns("import")
		form.Set("check", "Check Number")

		rec := e.owner.post(uploaded(t, e.owner, e.account, name, file), form)
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=imported") {
			t.Fatalf("importing %s: %d to %q\n%s", name, rec.Code, loc, rec.Body)
		}

		return strings.TrimSuffix(loc, "?done=imported")
	}

	july := importChecks("july.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n"+
		"2026-07-09,CHECK,-30.00,970.00,1177\n")

	rec := e.owner.receipts(e.account+"/checks", nil, [2]string{"1190.jpg", photoOf("1190")})
	checks := receiptsOn(e.owner.get(rec.Header().Get("Location")).Body.String())
	if len(checks) != 1 {
		t.Fatalf("check 1190: %v", checks)
	}

	if rec := e.owner.post(checks[0]+"/details", url.Values{
		"amount": {"45.50"}, "merchant": {"Diocesan Office"}, "memo": {""}, "written_on": {"2026-07-05"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("its amount and day: %d\n%s", rec.Code, rec.Body)
	}

	wantBody(t, e.owner.get(july), "Checks written by 2026-07-09 that the bank had not paid by that day", "Check 1190", "Checks outstanding: 1, for $45.50 in all.")

	importChecks("august.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-08-04,CHECK,-45.50,924.50,1190\n")

	page := e.owner.get(july)
	wantBody(t, page, "Check 1190", "cleared 2026-08-04", "Checks outstanding: 1, for $45.50 in all.")

	if strings.Contains(e.owner.get(e.account+"/months").Body.String(), "Checks outstanding") {
		t.Error("the account's months still list check 1190 as outstanding")
	}
}
