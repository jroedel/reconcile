package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Statements split by holder through the site (docs/clearing.md, 3):
// two people's printouts, neither saying whose it is, each with the same
// garage on the same day. Everything invented.
func TestStatementsSplitByHolder(t *testing.T) {
	t.Parallel()

	e, _, _, signUpAs := sorted(t)

	wantBody(t, e.owner.get(e.account+"/transactions"), "Statements arrive one file per holder")
	wantRedirect(t, e.owner.post(e.account+"/holders", url.Values{"on": {"1"}}), e.account+"/transactions?done=split-on")
	wantBody(t, e.owner.get(e.account), "said its statements arrive one file per holder")

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
	wantBody(t, rec, "2 of 2 holders", "Ana", "Ben", "-$15.50", "-$16.00")

	body := rec.Body.String()
	if n := strings.Count(body, ">CITY GARAGE<"); n != 2 {
		t.Errorf("the garage is listed %d times, want twice", n)
	}

	// Only an owner changes it; a stranger finds nothing.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"bookkeeper@example.org"}, "role": {"bookkeeper"}}), e.org+"?done=granted")
	bookkeeper := signUpAs("bookkeeper@example.org")

	if body := bookkeeper.get(e.account + "/transactions").Body.String(); strings.Contains(body, "Statements arrive one file per holder") {
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

// A file that looks like one holder's month, on an account that does not
// keep its holders apart (issue #81): the preview says so and offers the
// option, and comes back to the file once it is on. The account's
// settings say whether it is on, and lead to it. Everything invented.
func TestAFileThatLooksLikeOneHolders(t *testing.T) {
	t.Parallel()

	e, _, _, signUpAs := sorted(t)

	settings := e.owner.get(e.account)
	wantBody(t, settings, "Statements arrive one file per holder: off.", e.account+"/transactions?holders=1#holders")
	wantBody(t, e.owner.get(e.account+"/transactions?holders=1"), `id="holders" open`)

	held := func(action string) url.Values {
		f := columns(action)
		f.Set("holder_column", "Card Member")

		return f
	}

	ana := uploaded(t, e.owner, e.account, "ana.csv", "Date,Description,Amount,Balance,Card Member\n2026-09-04,CITY GARAGE,-12.00,988.00,Ana\n")
	if rec := e.owner.post(ana, held("import")); rec.Code != http.StatusSeeOther {
		t.Fatalf("Ana's file: %d\n%s", rec.Code, rec.Body.String())
	}

	ben := uploaded(t, e.owner, e.account, "ben.csv", "Date,Description,Amount,Balance,Card Member\n2026-09-04,CITY GARAGE,-12.00,500.00,Ben\n2026-09-06,BAKERY,-4.00,496.00,Ben\n")
	wantBody(t, e.owner.post(ben, held("preview")), "One file per holder?", "Every charge in this file is Ben&#39;s", "Keep this account&#39;s holders apart",
		"Ben&#39;s in this file, where the account has “CITY GARAGE”, Ana&#39;s")

	// A bookkeeper sees why, and is told to ask an owner.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"bookkeeper@example.org"}, "role": {"bookkeeper"}}), e.org+"?done=granted")
	bookkeeper := signUpAs("bookkeeper@example.org")

	theirs := uploaded(t, bookkeeper, e.account, "ben.csv", "Date,Description,Amount,Balance,Card Member\n2026-09-04,CITY GARAGE,-12.00,500.00,Ben\n")
	if body := bookkeeper.post(theirs, held("preview")).Body.String(); !strings.Contains(body, "Ask one to") || strings.Contains(body, "Keep this account&#39;s holders apart") {
		t.Errorf("the bookkeeper's preview:\n%s", body)
	}

	if body := bookkeeper.get(e.account).Body.String(); strings.Contains(body, "Statements arrive one file per holder:") {
		t.Error("a bookkeeper's settings say the option")
	}

	// Somewhere else to go back to is not taken.
	wantRedirect(t, e.owner.post(e.account+"/holders", url.Values{"on": {""}, "return": {"https://elsewhere.invalid/"}}), e.account+"/transactions?done=split-off")

	wantRedirect(t, e.owner.post(e.account+"/holders", url.Values{"on": {"1"}, "return": {ben}}), ben+"?done=split-on")

	again := e.owner.get(ben + "?done=split-on")
	wantBody(t, again, "now kept apart by holder")

	if strings.Contains(again.Body.String(), "One file per holder?") {
		t.Error("the preview still asks for the option once it is on")
	}

	wantBody(t, e.owner.get(e.account), "Statements arrive one file per holder: on.")
}
