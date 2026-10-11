package muxer

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// uploadMany sends files as the bulk import's form would, name and
// content in turn.
func (b *browser) uploadMany(path string, files ...[2]string) *httptest.ResponseRecorder {
	b.t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	for _, f := range files {
		fw, err := mw.CreateFormFile("files", f[0])
		if err != nil {
			b.t.Fatal(err)
		}

		fw.Write([]byte(f[1]))
	}

	mw.Close()

	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Sec-Fetch-Site", "same-origin")

	return b.do(r)
}

// A treasurer sends a consolidated statement and a CSV together: each part
// of the statement finds its account by its number and is imported with
// one button; the CSV is asked about, and then waits for its own preview,
// since what it holds checks nothing. A stranger with the list's address
// finds nothing on it, and imports nothing.
func TestABulkImportOfSeveralFiles(t *testing.T) {
	t.Parallel()

	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)
	orgID := strings.TrimPrefix(e.org, "/orgs/")

	first := made(t, e.owner, "/accounts", url.Values{"org": {orgID}, "name": {"Checking A"}, "kind": {"checking"}, "last4": {pdfsourcetest.First}})
	second := made(t, e.owner, "/accounts", url.Values{"org": {orgID}, "name": {"Checking B"}, "kind": {"checking"}, "last4": {pdfsourcetest.Second}})

	wantBody(t, e.owner.get("/"), `href="/imports"`)
	wantBody(t, e.owner.get("/imports"), "Choose the files")

	if rec := e.owner.uploadMany("/imports"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no files: %d", rec.Code)
	}

	rec := e.owner.uploadMany("/imports",
		[2]string{"statements.pdf", string(pdfsourcetest.Consolidated())},
		[2]string{"export.csv", "Date,Description,Amount\n2026-09-01,CORNER GROCERY,-12.00\n"})

	list := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(list, "/imports?") {
		t.Fatalf("upload: %d to %q", rec.Code, list)
	}

	page := e.owner.get(list)
	wantBody(t, page, "statements.pdf", "the part for the account ending "+pdfsourcetest.First, "Found by the number it prints",
		"Ready to import", "export.csv", "No account could be told from the file", "Import the ready ones: 2")

	q, _ := url.ParseQuery(strings.TrimPrefix(list, "/imports?"))
	csv := ""

	for _, f := range q["f"] {
		if strings.Contains(page.Body.String(), `name="to-`+f+`"`) {
			csv = f
		}
	}

	if csv == "" {
		t.Fatal("the CSV's row has no choice of account")
	}

	// A stranger with the address: nothing on it, and nothing imported.
	stranger := signUp(t, h, sent, "stranger@example.org")

	if body := stranger.get(list).Body.String(); strings.Contains(body, "statements.pdf") || strings.Contains(body, "export.csv") {
		t.Error("a stranger sees the treasurer's files")
	}

	form := url.Values{"f": q["f"], "to-" + csv: {strings.TrimPrefix(first, "/accounts/")}}

	rec = stranger.post("/imports/import", form)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.Contains(loc, "imported=0") {
		t.Errorf("a stranger's import: %d to %q", rec.Code, loc)
	}

	// The treasurer chooses the CSV's account and imports.
	rec = e.owner.post("/imports/import", form)

	done := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.Contains(done, "imported=2") {
		t.Fatalf("import: %d to %q", rec.Code, done)
	}

	page = e.owner.get(done)
	wantBody(t, page, "Imported: 2.", "See the statement", "Nothing in it could be checked", "Look at it on its own page",
		`href="`+first+`/imports/`+csv+`"`)

	wantBody(t, e.owner.get(first+"/transactions"), "statements.pdf")
	wantBody(t, e.owner.get(second+"/transactions"), "statements.pdf")
}
