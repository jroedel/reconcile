package muxer

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/app/domain/ledgerapp"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Every statement here is invented.

const july = "Date,Description,Amount,Balance\n" +
	"2026-07-01,OPENING DEPOSIT,1000.00,1000.00\n" +
	"2026-07-03,CORNER GROCERY,-33.99,966.01\n" +
	"2026-07-03,COFFEE CART,-3.50,962.51\n" +
	"2026-07-03,COFFEE CART,-3.50,959.01\n" +
	"2026-07-10,PARISH OFFERTORY,250.00,1209.01\n" +
	"2026-07-28,ELECTRIC CO,-120.00,1089.01\n"

// julyMissingARow is july without the grocery.
const julyMissingARow = "Date,Description,Amount,Balance\n" +
	"2026-07-01,OPENING DEPOSIT,1000.00,1000.00\n" +
	"2026-07-03,COFFEE CART,-3.50,962.51\n" +
	"2026-07-03,COFFEE CART,-3.50,959.01\n"

// upload sends a file as the account page's form would.
func (b *browser) upload(path, name, content string) *httptest.ResponseRecorder {
	b.t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	if name != "" {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			b.t.Fatal(err)
		}

		fw.Write([]byte(content))
	}

	mw.Close()

	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Sec-Fetch-Site", "same-origin")

	return b.do(r)
}

// columns is the preview form for july's columns, as the page sends it.
func columns(action string) url.Values {
	return url.Values{
		"date": {"Date"}, "description": {"Description"}, "columns": {"one"}, "amount": {"Amount"},
		"balance": {"Balance"}, "separator": {","}, "skip": {"0"}, "action": {action},
	}
}

var statementPath = regexp.MustCompile(`^/statements/[0-9a-f]+$`)

// uploaded uploads a file and returns its preview's path.
func uploaded(t *testing.T, b *browser, account, name, content string) string {
	t.Helper()

	rec := b.upload(account+"/statements", name, content)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, account+"/imports/") {
		t.Fatalf("upload: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	return loc
}

// imported uploads and imports a file, and returns its statement's path.
func imported(t *testing.T, b *browser, account, name, content string) string {
	t.Helper()

	rec := b.post(uploaded(t, b, account, name, content), columns("import"))

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=imported") || !statementPath.MatchString(strings.TrimSuffix(loc, "?done=imported")) {
		t.Fatalf("import: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	return strings.TrimSuffix(loc, "?done=imported")
}

func TestImportingAStatement(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	wantBody(t, e.owner.get(e.account), `href="`+e.account+`/transactions"`)

	// What was read, checked, and counted, before anything is stored.
	preview := uploaded(t, e.owner, e.account, "checking-july.csv", july)
	wantBody(t, e.owner.get(preview), "checking-july.csv", "CORNER GROCERY", "-$33.99", "$1,209.01",
		"Every row&#39;s balance follows from the one before", "6 new transactions, and 0 that are here already")
	wantBody(t, e.owner.get(e.account+"/transactions"), "No transactions yet")

	// Looking again with other columns imports nothing either.
	if rec := e.owner.post(preview, columns("preview")); rec.Code != http.StatusOK {
		t.Errorf("preview again: %d", rec.Code)
	}

	wantBody(t, e.owner.get(e.account+"/transactions"), "No transactions yet")

	rec := e.owner.post(preview, columns("import"))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=imported") {
		t.Fatalf("import: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	statement := strings.TrimSuffix(loc, "?done=imported")

	wantBody(t, e.owner.get(loc), "Statement, 2026-07-01 to 2026-07-28", "Imported: 6 new transactions", "balanced row by row")
	wantBody(t, e.owner.get(e.account+"/transactions"), "2026-07", "PARISH OFFERTORY", "-$120.00", "Money in", "$1,250.00", statement)
	wantBody(t, e.owner.get(e.account), "imported checking-july.csv, 2026-07-01 to 2026-07-28: 6 new transactions")

	// The original, as an attachment, whatever it is.
	dl := e.owner.get(statement + "/file")
	if dl.Code != http.StatusOK || dl.Body.String() != july ||
		dl.Header().Get("Content-Disposition") != `attachment; filename=checking-july.csv` ||
		dl.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("download: %d %q %q", dl.Code, dl.Header().Get("Content-Disposition"), dl.Header().Get("Content-Type"))
	}

	// The same file again is said, and refused.
	again := uploaded(t, e.owner, e.account, "july-again.csv", july)
	wantBody(t, e.owner.get(again), "This file was imported into this account already", `value="import" disabled`)

	if rec := e.owner.post(again, columns("import")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("the same file imported twice: %d", rec.Code)
	}

	// Removed, and it can come in again.
	wantRedirect(t, e.owner.post(statement+"/remove", nil), e.account+"/transactions?done=removed")
	wantBody(t, e.owner.get(e.account+"/transactions?done=removed"), "No transactions yet", "were removed")
	imported(t, e.owner, e.account, "checking-july.csv", july)
}

// A statement with a row missing says which line broke, and imports
// nothing.
func TestAStatementThatDoesNotBalance(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	preview := uploaded(t, e.owner, e.account, "short.csv", julyMissingARow)
	wantBody(t, e.owner.get(preview), "The balance on line 3 should be $996.50, but the file says $962.51", `value="import" disabled`)

	rec := e.owner.post(preview, columns("import"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("import: %d", rec.Code)
	}

	wantBody(t, rec, "does not balance, so nothing was imported")
	wantBody(t, e.owner.get(e.account+"/transactions"), "No transactions yet")
}

func TestWhatCannotBeUploaded(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	rec := e.owner.upload(e.account+"/statements", "", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no file: %d", rec.Code)
	}

	wantBody(t, rec, "Choose a file first")

	// A PDF is stored and then said to be a later version's.
	rec = e.owner.get(uploaded(t, e.owner, e.account, "statement.pdf", "%PDF-1.7\n%invented\n"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a PDF: %d", rec.Code)
	}

	wantBody(t, rec, "That is a PDF")

	rec = e.owner.upload(e.account+"/statements", "huge.csv", strings.Repeat("x", ledgerapp.MaxUpload))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("too big: %d", rec.Code)
	}

	wantBody(t, rec, "too big for a statement")

	// The form's own posts are not multipart, and an upload is nothing else.
	if rec := e.owner.post(e.account+"/statements", url.Values{"file": {"x"}}); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a form-encoded upload: %d", rec.Code)
	}
}

// Statements are as private as their account: a stranger and the site
// administrator get the same 404 everywhere, a viewer reads and changes
// nothing, and a file's identifier is no way into it.
func TestStatementsAreAsPrivateAsTheirAccount(t *testing.T) {
	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { c.Bootstrap = secret })
	e := newEstate(t, h, sent)

	statement := imported(t, e.owner, e.account, "checking-july.csv", july)
	preview := uploaded(t, e.owner, e.account, "short.csv", julyMissingARow)
	file := preview[strings.LastIndex(preview, "/")+1:]

	stranger := signUp(t, h, sent, "stranger@example.org")
	theirs := made(t, stranger, "/accounts", url.Values{"name": {"Stranger's"}, "kind": {"cash"}})

	admin := newBrowser(t, h)
	admin.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}})

	gets := []string{e.account + "/transactions", e.account + "/transactions?month=2026-07", preview, statement, statement + "/file"}

	for name, b := range map[string]*browser{"a stranger": stranger, "the site administrator": admin} {
		for _, path := range gets {
			if rec := b.get(path); rec.Code != http.StatusNotFound {
				t.Errorf("%s: GET %s = %d, want 404", name, path, rec.Code)
			}
		}

		if rec := b.post(preview, columns("import")); rec.Code != http.StatusNotFound {
			t.Errorf("%s: import = %d", name, rec.Code)
		}

		if rec := b.post(statement+"/remove", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: remove = %d", name, rec.Code)
		}

		if rec := b.upload(e.account+"/statements", "mine.csv", july); rec.Code != http.StatusNotFound {
			t.Errorf("%s: upload = %d", name, rec.Code)
		}
	}

	// The owner's file, read into the stranger's own account.
	if rec := stranger.get(theirs + "/imports/" + file); rec.Code != http.StatusNotFound {
		t.Errorf("somebody else's upload: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUp(t, h, sent, "viewer@example.org")

	wantBody(t, viewer.get(e.account+"/transactions"), "CORNER GROCERY")
	wantBody(t, viewer.get(statement), "balanced row by row")

	if body := viewer.get(e.account + "/transactions").Body.String(); strings.Contains(body, "Add a statement") {
		t.Error("a viewer is offered the upload form")
	}

	if body := viewer.get(statement).Body.String(); strings.Contains(body, "/remove") {
		t.Error("a viewer is offered removal")
	}

	for what, rec := range map[string]*httptest.ResponseRecorder{
		"upload": viewer.upload(e.account+"/statements", "mine.csv", july),
		"remove": viewer.post(statement+"/remove", nil),
	} {
		if rec.Code != http.StatusForbidden {
			t.Errorf("a viewer's %s: %d", what, rec.Code)
		}
	}

	// Signed out, nothing.
	nobody := newBrowser(t, h)
	if rec := nobody.get(statement + "/file"); rec.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", rec.Code)
	}

	wantBody(t, e.owner.get(e.account+"/transactions"), "CORNER GROCERY")
}
