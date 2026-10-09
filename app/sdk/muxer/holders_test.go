package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Statements split by cardholder through the site (docs/clearing.md, 3):
// two people's printouts, neither saying whose it is, each with the same
// garage on the same day. Everything invented.
func TestStatementsSplitByCardholder(t *testing.T) {
	e, _, _, signUpAs := sorted(t)

	wantBody(t, e.owner.get(e.account+"/transactions"), "Statements arrive one file per cardholder")
	wantRedirect(t, e.owner.post(e.account+"/holders", url.Values{"on": {"1"}}), e.account+"/transactions?done=split-on")
	wantBody(t, e.owner.get(e.account), "said its statements arrive one file per cardholder")

	month := func(name, content, holder string) {
		t.Helper()

		preview := uploaded(t, e.owner, e.account, name, content)
		wantBody(t, e.owner.get(preview), "Whose statement is this?")

		form := columns("import")
		form.Set("holder_new", holder)

		rec := e.owner.post(preview, form)
		if rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "?done=imported") {
			t.Fatalf("importing %s: %d\n%s", name, rec.Code, rec.Body.String())
		}
	}

	month("ana.csv", "Date,Description,Amount,Balance\n2026-09-04,CITY GARAGE,-12.00,988.00\n2026-09-05,COFFEE CART,-3.50,984.50\n", "Ana")
	month("ben.csv", "Date,Description,Amount,Balance\n2026-09-04,CITY GARAGE,-12.00,500.00\n2026-09-06,BAKERY,-4.00,496.00\n", "Ben")

	rec := e.owner.get(e.account + "/transactions?month=2026-09")
	wantBody(t, rec, "2 of 2 cardholders", "Ana", "Ben", "-$15.50", "-$16.00")

	body := rec.Body.String()
	if n := strings.Count(body, ">CITY GARAGE<"); n != 2 {
		t.Errorf("the garage is listed %d times, want twice", n)
	}

	// Only an owner changes it; a stranger finds nothing.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"bookkeeper@example.org"}, "role": {"bookkeeper"}}), e.org+"?done=granted")
	bookkeeper := signUpAs("bookkeeper@example.org")

	if body := bookkeeper.get(e.account + "/transactions").Body.String(); strings.Contains(body, "Statements arrive one file per cardholder") {
		t.Error("a bookkeeper is offered the option")
	}

	if rec := bookkeeper.post(e.account+"/holders", url.Values{"on": {""}}); rec.Code != http.StatusForbidden {
		t.Errorf("a bookkeeper: %d", rec.Code)
	}

	stranger := signUpAs("stranger@example.org")
	for _, account := range []string{e.account, e.ownAccount} {
		if rec := stranger.post(account+"/holders", url.Values{"on": {"1"}}); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger on %s: %d", account, rec.Code)
		}
	}
}
