package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var payeeLink = regexp.MustCompile(`href="(/payees/[0-9a-f]{32})"`)

// addedPayee adds a payee to a list on its page, and is the new payee's
// page.
func addedPayee(t *testing.T, b *browser, list, name string) string {
	t.Helper()

	rec := b.post(list, url.Values{"name": {name}})

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=added") {
		t.Fatalf("adding the payee %q: %d to %q\n%s", name, rec.Code, loc, rec.Body.String())
	}

	return strings.TrimSuffix(loc, "?done=added")
}

// A payment's page says whom it was paid to, which makes a payee of the
// organization's list; two that look alike are suggested as one, joined,
// and split apart again, each with a line of history.
func TestPayeesOnTheSite(t *testing.T) {
	t.Parallel()

	e, txs, _, _ := sorted(t)

	electric, grocery := txs["ELECTRIC CO"], txs["CORNER GROCERY"]
	if electric == "" || grocery == "" {
		t.Fatalf("the transactions: %v", txs)
	}

	wantBody(t, e.owner.get(electric), "Paid to", `action="`+electric+`/payee"`)

	wantRedirect(t, e.owner.post(electric+"/payee", url.Values{"payee": {"  Hilltop   Power "}}), electric+"?done=payee#paid-to")
	wantRedirect(t, e.owner.post(grocery+"/payee", url.Values{"payee": {"HILLTOP POWER, LLC"}}), grocery+"?done=payee#paid-to")

	page := e.owner.get(electric).Body.String()
	m := payeeLink.FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "Hilltop Power") {
		t.Fatalf("the transaction's payee:\n%s", page)
	}

	power := m[1]

	// The organization's list, with the suggestion.
	list := e.owner.get(e.org + "/payees")
	wantBody(t, list, "Hilltop Power", "HILLTOP POWER, LLC", "These may be one payee", "Join them")

	var llc string
	for _, l := range payeeLink.FindAllStringSubmatch(list.Body.String(), -1) {
		if l[1] != power {
			llc = l[1]
		}
	}

	if llc == "" {
		t.Fatal("no second payee on the list")
	}

	// An account of the parish has its organization's list; the owner's
	// own card has one of its own.
	wantRedirect(t, e.owner.get(e.account+"/payees"), e.org+"/payees")
	wantBody(t, e.owner.get(e.ownAccount+"/payees"), "No payees yet")

	// Joined, on the list: one payee, two payments, two spellings.
	wantRedirect(t, e.owner.post(power+"/join", url.Values{"from": {strings.TrimPrefix(llc, "/payees/")}, "back": {"list"}}), e.org+"/payees?done=joined")
	wantBody(t, e.owner.get(e.org+"/payees"), "2 payments", "2 spellings")

	if body := e.owner.get(e.org + "/payees").Body.String(); strings.Contains(body, "These may be one payee") {
		t.Error("joined payees are still suggested")
	}

	wantRedirect(t, e.owner.get(llc), power)
	wantBody(t, e.owner.get(power), "Spellings", "HILLTOP POWER, LLC", "Written as HILLTOP POWER, LLC", "CORNER GROCERY")

	// Split off again, into the payee it was.
	wantRedirect(t, e.owner.post(power+"/split", url.Values{"spelling": {"HILLTOP POWER, LLC"}, "into": {""}}), llc+"?done=split")
	wantBody(t, e.owner.get(llc), "CORNER GROCERY")

	// Refused: the payee's own name, and a join into itself.
	wantRedirect(t, e.owner.post(power+"/split", url.Values{"spelling": {"Hilltop Power"}}), power+"?problem=split-name")
	wantRedirect(t, e.owner.post(power+"/join", url.Values{"from": {strings.TrimPrefix(power, "/payees/")}}), power+"?problem=self")

	// Not the same: the suggestion goes.
	wantRedirect(t, e.owner.post(llc+"/not-same", url.Values{"other": {strings.TrimPrefix(power, "/payees/")}, "back": {"list"}}), e.org+"/payees?done=not-same")

	if body := e.owner.get(e.org + "/payees").Body.String(); strings.Contains(body, "These may be one payee") {
		t.Error("payees said not to be the same are still suggested")
	}

	wantBody(t, e.owner.get(e.org), "added the payee Hilltop Power", "joined the payee HILLTOP POWER, LLC into Hilltop Power", "split HILLTOP POWER, LLC off the payee Hilltop Power")

	// Renamed and archived from its page; added by hand on the list.
	wantRedirect(t, e.owner.post(power+"/rename", url.Values{"name": {"Hilltop Power and Light"}}), power+"?done=renamed")
	wantRedirect(t, e.owner.post(power+"/archive", url.Values{"archived": {"1"}}), power+"?done=archived")
	wantBody(t, e.owner.get(power), "Archived")

	added := addedPayee(t, e.owner, e.org+"/payees", "Summit Roofing")
	wantBody(t, e.owner.get(added), "Summit Roofing", "No payments yet")

	if rec := e.owner.post(e.org+"/payees", url.Values{"name": {"summit roofing"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a name the list has: %d", rec.Code)
	}
}
