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
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
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

// A bank's page printed to PDF: read, checked against the total it states,
// shown with the switch for its signs, and imported.
func TestImportingAPrintedPDF(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	page := pdftexttest.Row(40, 50, "Example Bank Account Activity, Aug 1, 2026 to Aug 31, 2026")
	page = append(page, pdftexttest.Row(70, 50, "Date", 150, "Description", 330, "Name", 470, "Amount")...)
	page = append(page, pdftexttest.Row(100, 50, "Aug 20, 2026", 150, "Corner Hardware", 330, "PAT EXAMPLE", 470, "-$45.10")...)
	page = append(page, pdftexttest.Row(130, 50, "Aug 22, 2026", 150, "Parish Office Supply", 330, "PAT EXAMPLE", 470, "-$12.40")...)
	page = append(page, pdftexttest.Row(160, 50, "Aug 25, 2026", 150, "Bake sale deposit", 330, "PAT EXAMPLE", 470, "$310.00")...)
	page = append(page, pdftexttest.Row(200, 150, "Total activity", 470, "$252.50")...)

	preview := uploaded(t, e.owner, e.account, "august.pdf", string(pdftexttest.Draw(page)))
	wantBody(t, e.owner.get(preview), "august.pdf", "Corner Hardware", "PAT EXAMPLE", "-$45.10", "Signs",
		`name="invert" value="1">`, "The rows add up to the total the document states", "3 new transactions")

	rec := e.owner.post(preview, url.Values{"action": {"import"}})

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=imported") {
		t.Fatalf("import: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	wantBody(t, e.owner.get(loc), "2026-08-01 to 2026-08-31", "adds up to its stated total")
	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-08"), "Bake sale deposit", "$310.00", "-$12.40")
}

// A bank's statement of two accounts in one file, imported into an account
// whose number ends as neither does: the preview asks which, and the one
// chosen is imported, balanced day by day.
func TestImportingAStatementOfSeveralAccounts(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	preview := uploaded(t, e.owner, e.account, "statements.pdf", string(pdfsourcetest.Consolidated()))
	wantBody(t, e.owner.get(preview), "Which account is this?", "The account ending in 1111, with 8 transactions",
		"The account ending in 2222, with 2 transactions")

	if rec := e.owner.post(preview, url.Values{"action": {"import"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("importing without choosing: %d", rec.Code)
	} else {
		wantBody(t, rec, "Choose which of them is this account")
	}

	wantBody(t, e.owner.post(preview, url.Values{"action": {"preview"}, "part": {"1111"}}),
		"Check 1003", "Every row&#39;s balance follows from the one before", "8 new transactions")

	rec := e.owner.post(preview, url.Values{"action": {"import"}, "part": {"1111"}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=imported") {
		t.Fatalf("import: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-09"), "Check 1002", "Remote Online Deposit", "-$25.00")
}

// One charge worded two ways in two files is left out of the second as
// probably here already, and can be imported anyway (docs/duplicates.md).
func TestTheSameChargeWordedTwoWays(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	imported(t, e.owner, e.account, "checking-july.csv", july)

	plain := func(action string, include ...string) url.Values {
		return url.Values{
			"date": {"Date"}, "description": {"Description"}, "columns": {"one"}, "amount": {"Amount"},
			"separator": {","}, "skip": {"0"}, "action": {action}, "include": include,
		}
	}

	other := "Date,Description,Amount\n" +
		"2026-07-03,SQ *COFFEE CART #12,-3.50\n" +
		"2026-07-03,SQ *COFFEE CART #12,-3.50\n" +
		"2026-07-30,BANK FEE,-5.00\n"

	preview := uploaded(t, e.owner, e.account, "export.csv", other)
	wantBody(t, e.owner.post(preview, plain("preview")), "Probably here already",
		"in this file, where the account has “COFFEE CART”", `name="include" value="0"`, `name="include" value="1"`,
		"1 new transactions, and 2 that are here already")

	// One of the two coffees is said to be another charge.
	rec := e.owner.post(preview, plain("import", "1"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d\n%s", rec.Code, rec.Body.String())
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), "SQ *COFFEE CART #12", "BANK FEE")
	wantBody(t, e.owner.get(e.account), "1 left out as probably here already")
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

	// A PDF that is not one is stored, and then refused with the advice.
	rec = e.owner.get(uploaded(t, e.owner, e.account, "statement.pdf", "%PDF-1.7\n%invented\n"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a PDF: %d", rec.Code)
	}

	if pdftext.Available() {
		wantBody(t, rec, "That PDF could not be opened", "as CSV or OFX instead")
	} else {
		wantBody(t, rec, "PDFs cannot be read on this site at the moment")
	}

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

	gets := []string{e.account + "/transactions", e.account + "/transactions?month=2026-07", e.account + "/months", preview, statement, statement + "/file"}

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

		if rec := b.post(statement+"/reconcile", url.Values{"from": {"2026-07-01"}, "to": {"2026-07-31"}}); rec.Code != http.StatusNotFound {
			t.Errorf("%s: reconcile = %d", name, rec.Code)
		}

		if rec := b.post(statement+"/reopen", url.Values{"reason": {"mine"}}); rec.Code != http.StatusNotFound {
			t.Errorf("%s: reopen = %d", name, rec.Code)
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
