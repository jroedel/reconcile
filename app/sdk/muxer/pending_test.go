package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Pending charges through the site (docs/clearing.md, 4): a card page with
// a hold on it, the hold posting at another amount, and one that never
// posts. Everything invented.
func TestPendingCharges(t *testing.T) {
	t.Parallel()

	e, _, _, signUpAs := sorted(t)

	preview := uploaded(t, e.owner, e.account, "september.csv",
		"Date,Description,Amount,Status\n2026-09-29,CITY PARKING,-7.25,Pending\n2026-09-02,HOTEL DEPOSIT,-50.00,Pending\n2026-09-16,CORNER BAKERY,-12.00,Posted\n")
	wantBody(t, e.owner.get(preview), ">pending<")

	form := columns("import")
	form.Del("balance")
	form.Set("status", "Status")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("importing September: %d\n%s", rec.Code, rec.Body.String())
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-09"), "CITY PARKING", ">pending<")

	// October posts the parking with the tip on it, and not the hotel.
	preview = uploaded(t, e.owner, e.account, "october.csv",
		"Date,Description,Amount,Status\n2026-10-01,CITY PARKING 0412,-12.00,Posted\n2026-10-20,HARDWARE BARN,-4.00,Posted\n")
	wantBody(t, e.owner.get(preview), "1 of its rows are charges that were pending here and have now posted")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("importing October: %d\n%s", rec.Code, rec.Body.String())
	}

	page := e.owner.get(e.account + "/transactions").Body.String()
	if !strings.Contains(page, "Still pending") || !strings.Contains(page, "HOTEL DEPOSIT") {
		t.Fatalf("no hotel still pending:\n%s", page)
	}

	release := regexp.MustCompile(`/transactions/[0-9a-f]+/release`).FindString(page)
	if release == "" {
		t.Fatal("no way to remove the hotel's hold")
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-10"), "CITY PARKING 0412", "-$12.00")

	// A viewer is refused, a stranger finds nothing, the owner removes it.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")

	viewer := signUpAs("viewer@example.org")
	if body := viewer.get(e.account + "/transactions").Body.String(); strings.Contains(body, "/release") {
		t.Error("a viewer is offered the removal")
	}

	if rec := viewer.post(release, nil); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer: %d", rec.Code)
	}

	if rec := signUpAs("stranger@example.org").post(release, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(release, nil), e.account+"/transactions?done=released")

	if rec := e.owner.post(release, nil); rec.Code != http.StatusNotFound {
		t.Errorf("removing it twice: %d", rec.Code)
	}

	if page := e.owner.get(e.account + "/transactions").Body.String(); strings.Contains(page, "HOTEL DEPOSIT") {
		t.Error("the hotel's hold is still listed")
	}

	wantBody(t, e.owner.get(e.account), "removed a pending charge")
}

// Two holds at one shop and one posted charge (issue #104): the site asks
// which hold it was, on the account's page and the hold's; the answer is
// a bookkeeper's, and can be undone. Claude asks and answers the same
// through the API, with the person's yes. Everything invented.
func TestPairingAPendingCharge(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	account := strings.TrimPrefix(e.account, "/accounts/")

	form := columns("import")
	form.Del("balance")
	form.Set("status", "Status")

	for _, file := range []struct{ name, content string }{
		{"page.csv", "Date,Description,Amount,Status\n2026-08-30,CAFE EXAMPLE PAL A…,-40.00,Pending\n2026-08-31,CAFE EXAMPLE PAL A…,-40.00,Pending\n"},
		{"bank.csv", "Date,Description,Amount,Status\n2026-08-31,CAFE EXAMPLE CITY ST,-50.00,Posted\n"},
	} {
		if rec := e.owner.post(uploaded(t, e.owner, e.account, file.name, file.content), form); rec.Code != http.StatusSeeOther {
			t.Fatalf("importing %s: %d\n%s", file.name, rec.Code, rec.Body.String())
		}
	}

	page := e.owner.get(e.account + "/transactions").Body.String()
	if !strings.Contains(page, "Pending charges that may have posted") {
		t.Fatalf("no pairs offered:\n%s", page)
	}

	pairs := regexp.MustCompile(`action="(/transactions/[0-9a-f]+/posted-as)" class="inline"><input type="hidden" name="posted" value="([0-9a-f]+)"`).FindAllStringSubmatch(page, -1)
	if len(pairs) != 2 || pairs[0][2] != pairs[1][2] {
		t.Fatalf("pairs: %v", pairs)
	}

	pair, posted := pairs[0][1], pairs[0][2]
	hold := strings.TrimSuffix(pair, "/posted-as")

	// A viewer is not offered it and is refused; a stranger finds nothing.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")

	viewer := signUp(t, s.h, s.sent, "viewer@example.org")
	if body := viewer.get(e.account + "/transactions").Body.String(); strings.Contains(body, "/posted-as") {
		t.Error("a viewer is offered the pairing")
	}

	if rec := viewer.post(pair, url.Values{"posted": {posted}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer: %d", rec.Code)
	}

	if rec := signUp(t, s.h, s.sent, "stranger@example.org").post(pair, url.Values{"posted": {posted}}); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(pair, url.Values{"posted": {posted}, "from": {"account"}}), e.account+"/transactions?done=paired")

	if page := e.owner.get(e.account + "/transactions").Body.String(); strings.Contains(page, "Pending charges that may have posted") {
		t.Error("the pairs are still offered")
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-08"), "pending, since posted", "counted as its posted charge", "-$50.00")
	wantBody(t, e.owner.get(hold), "It posted as the charge below", "CAFE EXAMPLE CITY ST", hold+"/unclear")
	wantBody(t, e.owner.get("/transactions/"+posted), "It was pending at -$40.00 on 2026-08-30 first")

	// Undone, and asked again.
	wantRedirect(t, e.owner.post(hold+"/unclear", nil), hold+"?done=unpaired")
	wantBody(t, e.owner.get(e.account+"/transactions"), "Pending charges that may have posted")
	wantBody(t, e.owner.get(e.account), "said a pending charge had not posted as the charge it was paired with")

	// Claude, through the API: the pairs, and the person's answer.
	key := keyFor(t, e.owner, "Claude", "books-keep")

	listed := s.get(t, "/api/v1/accounts/"+account+"/pending", key)
	if got := list(t, listed, "pairs"); len(got) != 2 || field(got[0], "posted", "id") != posted {
		t.Fatalf("list_pending: %v", listed)
	}

	holdID := strings.TrimPrefix(hold, "/transactions/")

	paired := s.change(t, http.MethodPut, "/api/v1/transactions/"+holdID+"/posted-as", key, map[string]string{"posted": posted}, http.StatusOK)
	if field(paired, "transaction", "posted_as") != posted {
		t.Errorf("pair_pending: %v", paired)
	}

	if got := s.get(t, "/api/v1/transactions/"+posted, key); field(got, "pending_form", "id") != holdID {
		t.Errorf("the posted charge's pending form: %v", got)
	}

	// The other hold cannot have the same charge.
	other := field(list(t, listed, "pairs")[1], "pending", "id").(string)
	if other == holdID {
		other = field(list(t, listed, "pairs")[0], "pending", "id").(string)
	}

	if got := s.change(t, http.MethodPut, "/api/v1/transactions/"+other+"/posted-as", key, map[string]string{"posted": posted}, http.StatusConflict); field(got, "error", "field") != "posted" {
		t.Errorf("the same charge twice: %v", got)
	}
}
