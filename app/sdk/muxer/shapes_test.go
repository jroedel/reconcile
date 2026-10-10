package muxer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/shapes"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
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

	wantBody(t, rec, "Nothing in this PDF shows that every row of it was read")

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

// The site's administrator writes a draft of a layout's description,
// keeps it while it is not yet one, and tries it, once it is, on a
// statement of theirs beside the reading the site makes now. Nobody else
// finds the drafts at all.
func TestALayoutDraft(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { c.Bootstrap = secret })
	e := newEstate(t, h, sent)

	admin := newBrowser(t, h)
	admin.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}})

	wantBody(t, admin.get("/admin/drafts/new?revise=us-consolidated-checking-a"), `&#34;version&#34;: 2`, "Keep the draft")

	rec := admin.post("/admin/drafts", url.Values{"text": {`{"id": "Not Yet"}`}})
	draft := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(draft, "/admin/drafts/") {
		t.Fatalf("a draft: %d to %q", rec.Code, draft)
	}

	unfinished := admin.get(draft)
	wantBody(t, unfinished, "This is not yet a layout description", "lower-case words joined by hyphens")

	if strings.Contains(unfinished.Body.String(), "Try the draft") {
		t.Error("a draft that is not a description can be tried")
	}

	var d shapes.Declaration
	for _, b := range shapes.Builtin() {
		if b.ID == "us-consolidated-checking-a" {
			d = b
		}
	}

	d.ID, d.Name = "a-draft", "A draft of a consolidated statement"

	rec = admin.post(draft, url.Values{"text": {shapebus.Format(d)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving: %d", rec.Code)
	}

	wantBody(t, admin.get(rec.Header().Get("Location")), "Saved.", "Try the draft", "Copy it out", "a-draft.json")
	wantBody(t, admin.get("/admin"), "A draft of a consolidated statement")

	tried := admin.upload(draft+"/try", "september.pdf", string(pdfsourcetest.Consolidated()))
	wantBody(t, tried, "Tried on september.pdf", "The draft found the same rows as the site finds now.",
		"Consolidated checking statement, layout A", "Account ending "+pdfsourcetest.First, "would be imported", "Remote Online Deposit")

	if rec := admin.upload(draft+"/try", "", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("trying on no file: %d", rec.Code)
	}

	for name, b := range map[string]*browser{"an owner": e.owner} {
		for _, rec := range []*httptest.ResponseRecorder{
			b.get(draft),
			b.get("/admin/drafts/new"),
			b.post(draft, url.Values{"text": {"{}"}}),
			b.post(draft+"/remove", nil),
			b.upload(draft+"/try", "september.pdf", string(pdfsourcetest.Consolidated())),
		} {
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: %d", name, rec.Code)
			}
		}
	}

	if rec := admin.post(draft+"/remove", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("removing: %d", rec.Code)
	}

	if rec := admin.get(draft); rec.Code != http.StatusNotFound {
		t.Errorf("a removed draft: %d", rec.Code)
	}
}
