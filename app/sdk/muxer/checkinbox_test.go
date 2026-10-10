package muxer

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// fileForm is the form that files a check from a person's own checks.
var fileForm = regexp.MustCompile(`action="(/checks/[0-9a-f]+)/file"`)

// checked puts one check into a browser's own checks and returns its
// address, /checks/{id}.
func checked(t *testing.T, b *browser) string {
	t.Helper()

	if rec := b.receipts("/checks", nil, [2]string{"Screenshot_20260712-101500.png", photoOf("a check of " + b.t.Name())}); rec.Code != http.StatusSeeOther {
		t.Fatalf("adding to the checks: %d %s", rec.Code, rec.Body)
	}

	m := fileForm.FindStringSubmatch(b.get("/checks").Body.String())
	if m == nil {
		t.Fatal("no check to file on the checks page")
	}

	return m[1]
}

// importChecks imports invented checks into the estate's checking
// account, which ends 1234: 1176 for 120.00 and 1177 for 30.00.
func importChecks(t *testing.T, e estate) {
	t.Helper()

	preview := uploaded(t, e.owner, e.account, "july.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n"+
		"2026-07-02,CHECK,-120.00,880.00,1176\n"+
		"2026-07-09,CHECK,-30.00,850.00,1177\n")
	form := columns("import")
	form.Set("check", "Check Number")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d", rec.Code)
	}
}

// A treasurer's stack of checks from several accounts goes to their own
// checks, from the page's form or shared a file at a time, and is seen by
// nobody else. Each is filed by choosing its account, which attaches it to
// the transaction with its number; the account's history says it came
// from the treasurer's own checks.
func TestChecksFiledFromYourOwnChecks(t *testing.T) {
	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	importChecks(t, e)

	account := strings.TrimPrefix(e.account, "/accounts/")

	wantBody(t, e.owner.get("/"), `href="/checks"`, "Add checks from any account")
	wantBody(t, e.owner.get("/receipts/share"), `value="mine"`, "Your checks, from any account", "seen by nobody else")

	rec := e.owner.receipts("/checks", nil, [2]string{"Screenshot_1.png", photoOf("one")}, [2]string{"Screenshot_2.png", photoOf("two")})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/checks?") {
		t.Fatalf("the form: %d to %q", rec.Code, loc)
	}

	wantBody(t, e.owner.get(rec.Header().Get("Location")), "2 check images added", "Waiting to be filed", "Parish checking ···1234")
	wantBody(t, e.owner.get("/"), "2 of your checks waiting to be filed")

	// Shared one at a time: kept, then there already.
	one := func(file [2]string) map[string]any {
		t.Helper()

		rec := e.owner.receipts("/receipts/share/one", url.Values{"to": {"mine"}}, file)

		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("shared: %d %s", rec.Code, rec.Body)
		}

		return out
	}

	if out := one([2]string{"Screenshot_3.png", photoOf("three")}); out["outcome"] != "kept" {
		t.Errorf("shared: %v", out)
	}

	if out := one([2]string{"Screenshot_3 (1).png", photoOf("three")}); out["outcome"] != "already" {
		t.Errorf("shared again: %v", out)
	}

	page := e.owner.get("/checks").Body.String()
	forms := fileForm.FindAllStringSubmatch(page, -1)
	if len(forms) != 3 {
		t.Fatalf("%d checks to file, want 3", len(forms))
	}

	// Filed with its number: attached to check 1176.
	rec = e.owner.post(forms[0][1]+"/file", url.Values{"account": {account}, "number": {"1176"}})
	wantRedirect(t, rec, "/checks?done=filed-attached")
	wantBody(t, e.owner.get("/checks?done=filed-attached"), "Filed, and attached", "Check 1176", "Parish checking · attached")
	wantBody(t, e.owner.get(e.account+"/receipts"), "Check 1176")
	wantBody(t, e.owner.get(e.account), "filed the image of check 1176 here from their own checks")

	if rec := e.owner.post(forms[0][1]+"/file", url.Values{"account": {account}, "number": {"1176"}}); rec.Code != http.StatusConflict {
		t.Errorf("filed twice: %d", rec.Code)
	}

	if rec := e.owner.post(forms[1][1]+"/file", url.Values{"account": {account}, "number": {"12a"}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "A check number is digits") {
		t.Errorf("a number that is no number: %d", rec.Code)
	}

	if rec := e.owner.post(forms[1][1]+"/file", url.Values{"number": {"1177"}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose the account") {
		t.Errorf("no account: %d", rec.Code)
	}

	// Removed and brought back.
	wantRedirect(t, e.owner.post(forms[2][1]+"/remove", url.Values{"removed": {"1"}}), "/checks?done=check-removed")
	wantBody(t, e.owner.get("/checks"), "Removed checks", "Bring back")
	wantRedirect(t, e.owner.post(forms[2][1]+"/remove", url.Values{"removed": {"0"}}), "/checks?done=check-restored")

	if rec := e.owner.get(forms[1][1] + "/image"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("the image: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// Nobody else sees or files them, the site administrator included,
	// and a person given only a project is offered no checks of their
	// own, having nowhere to file one.
	stranger := signUp(t, s.h, s.sent, "stranger@example.org")

	if strings.Contains(stranger.get("/checks").Body.String(), forms[1][1]) {
		t.Error("a stranger sees the treasurer's checks")
	}

	e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"contributor"}})
	pilgrim := signUp(t, s.h, s.sent, "pilgrim@example.org")

	if strings.Contains(pilgrim.get("/receipts/share").Body.String(), `value="mine"`) {
		t.Error("somebody with only a project is offered checks of their own")
	}
}

// Claude reads the treasurer's own checks and files each by the account
// number on its face: listed with the waiting receipts, looked at with
// get_receipt_image, and read with read_check and account_number. One
// whose number ends like no account is refused, and nothing moves; and
// nobody else's key sees or reads them.
func TestClaudeFilesYourOwnChecks(t *testing.T) {
	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	importChecks(t, e)
	key := keyFor(t, e.owner, "Claude", "books-keep")

	rec := e.owner.receipts("/checks", nil, [2]string{"Screenshot_1.jpg", drawnPhoto(t, 900, 400)}, [2]string{"Screenshot_2.jpg", drawnPhoto(t, 901, 400)})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the screenshots: %d", rec.Code)
	}

	var mine []string

	for _, rc := range list(t, s.get(t, "/api/v1/receipts/waiting", key), "receipts") {
		if field(rc, "your_checks") == true && field(rc, "check_image") == true && field(rc, "pages") == 1.0 {
			mine = append(mine, field(rc, "id").(string))
		}
	}

	if len(mine) != 2 {
		t.Fatalf("%d of the treasurer's own checks listed, want 2", len(mine))
	}

	if page := s.api(http.MethodGet, "/api/v1/receipts/"+mine[0]+"/image", key, ""); page.Code != http.StatusOK || page.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("the image: %d %q", page.Code, page.Header().Get("Content-Type"))
	}

	read := func(id, body string) (int, string) {
		t.Helper()

		rec := s.api(http.MethodPost, "/api/v1/receipts/"+id+"/check", key, body)

		return rec.Code, rec.Body.String()
	}

	if code, body := read(mine[0], `{"number": "1176", "amount": "120.00"}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "account_number") {
		t.Errorf("no account number: %d %s", code, body)
	}

	if code, body := read(mine[0], `{"number": "1176", "amount": "120.00", "account_number": "9999-0000"}`); code != http.StatusConflict || !strings.Contains(body, "ends 0000") || strings.Contains(body, "9999-0000") {
		t.Errorf("an account nobody has: %d %s", code, body)
	}

	if code, body := read(mine[0], `{"number": "1176", "amount": "210.00", "account_number": "9999-1234"}`); code != http.StatusConflict || !strings.Contains(body, "The bank paid check 1176 for 120.00") {
		t.Errorf("a misread amount: %d %s", code, body)
	}

	code, body := read(mine[0], `{"number": "1176", "amount": "120.00", "payee": "Hilltop Plumbing", "account_number": "9999 7777 1234"}`)
	if code != http.StatusOK || !strings.Contains(body, `"attached": true`) || strings.Contains(body, "7777 1234") {
		t.Fatalf("read and filed: %d %s", code, body)
	}

	wantBody(t, e.owner.get(e.account), "filed the image of a check here from their own checks", "read the image of check 1176, for 120.00", "through Claude")
	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), "Paid to Hilltop Plumbing")

	// Filed, it is a receipt under an id of its own, and its id in the
	// checks says so.
	if code, body := read(mine[0], `{"number": "1176", "amount": "120.00", "account_number": "9999 1234"}`); code != http.StatusConflict || !strings.Contains(body, "filed") {
		t.Errorf("read again once filed: %d %s", code, body)
	}

	stranger := keyFor(t, signUp(t, s.h, s.sent, "stranger@example.org"), "Claude", "books-keep")

	if rec := s.api(http.MethodGet, "/api/v1/receipts/"+mine[1]+"/image", stranger, ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's key sees the image: %d", rec.Code)
	}

	if rec := s.api(http.MethodPost, "/api/v1/receipts/"+mine[1]+"/check", stranger, `{"number": "1177", "amount": "30.00", "account_number": "9999 1234"}`); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's key reads it: %d", rec.Code)
	}

	for _, rc := range list(t, s.get(t, "/api/v1/receipts/waiting", stranger), "receipts") {
		if field(rc, "your_checks") == true {
			t.Error("a stranger's key lists the treasurer's checks")
		}
	}
}
