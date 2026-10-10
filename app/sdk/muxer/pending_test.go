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
