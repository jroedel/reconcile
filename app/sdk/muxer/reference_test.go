package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// An owner marks an organization's account for reference and takes the
// mark off; the page and the organization's list say so, and the history
// keeps both. A viewer may not, and an account on its own cannot be one.
func TestAnAccountForReference(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	wantRedirect(t, e.owner.post(e.account+"/reference", url.Values{"reference": {"1"}}), e.account+"?done=reference-on")

	page := e.owner.get(e.account).Body.String()
	for _, want := range []string{"This account is for reference.", "Count it in the organization&#39;s books", "marked it for reference"} {
		if !strings.Contains(page, want) {
			t.Errorf("the account's page lacks %q", want)
		}
	}

	wantBody(t, e.owner.get(e.org), "for reference")
	wantBody(t, e.owner.get(e.account+"/transactions"), "nothing in it needs sorting")

	wantRedirect(t, e.owner.post(e.account+"/reference", url.Values{"reference": {"0"}}), e.account+"?done=reference-off")

	if page := e.owner.get(e.account).Body.String(); strings.Contains(page, "This account is for reference.") || !strings.Contains(page, "took the mark for reference off") {
		t.Errorf("after taking it off:\n%s", page)
	}

	if rec := e.owner.post(e.ownAccount+"/reference", url.Values{"reference": {"1"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "An account on its own is its own books.") {
		t.Errorf("an account on its own: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")

	viewer := signUp(t, h, sent, "viewer@example.org")
	if rec := viewer.post(e.account+"/reference", url.Values{"reference": {"1"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer marked it: %d", rec.Code)
	}
}
