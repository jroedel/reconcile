package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// A printed page that states no total, in a layout the site has no
// description of: the preview says why it cannot be imported, offers no
// balances to type, and refuses the import. The site's administrator then
// finds the layout on their page -- its words, and nothing of what was on
// it.
func TestANewLayoutThatProvesNothing(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { c.Bootstrap = secret })
	e := newEstate(t, h, sent)

	page := pdftexttest.Row(40, 50, "Example Bank Account Activity, Aug 1, 2026 to Aug 31, 2026")
	page = append(page, pdftexttest.Row(70, 50, "Date", 150, "Description", 330, "Name", 470, "Amount")...)
	page = append(page, pdftexttest.Row(100, 50, "Aug 20, 2026", 150, "Corner Hardware", 330, "PAT EXAMPLE", 470, "-$45.10")...)
	page = append(page, pdftexttest.Row(130, 50, "Aug 22, 2026", 150, "Parish Office Supply", 330, "PAT EXAMPLE", 470, "-$12.40")...)

	preview := uploaded(t, e.owner, e.account, "august.pdf", string(pdftexttest.Draw(page)))

	rec := e.owner.get(preview)
	wantBody(t, rec, "Corner Hardware", "This layout of statement is new to this site, and it states nothing to check", "Import</button>")

	body := rec.Body.String()
	if strings.Contains(body, `name="opening"`) || strings.Contains(body, "import it unchecked") {
		t.Errorf("the preview offers balances to type, or importing unchecked:\n%s", body)
	}

	rec = e.owner.post(preview, url.Values{"action": {"import"}, "opening": {"100.00"}, "closing": {"42.50"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("import: %d", rec.Code)
	}

	wantBody(t, rec, "nothing in it shows that every row was read")

	admin := newBrowser(t, h)
	admin.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}})

	listed := admin.get("/admin")
	wantBody(t, listed, "Statement layouts seen", "pdftexttest #.#", "date | description | name | amount",
		"Files: 1. Balanced by their own figures: 0.")

	for _, never := range []string{"Corner Hardware", "PAT EXAMPLE", "45.10", "august.pdf", "Parish checking"} {
		if strings.Contains(strings.SplitN(listed.Body.String(), "Statement layouts seen", 2)[1], never) {
			t.Errorf("the layouts seen show %q", never)
		}
	}
}
