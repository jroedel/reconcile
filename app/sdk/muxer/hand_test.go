package muxer

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// entry sends the transactions page's "Enter a transaction by hand" form.
func (b *browser) entry(path string, fields url.Values, docName, doc string) *httptest.ResponseRecorder {
	b.t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	for k, vs := range fields {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}

	if docName != "" {
		fw, err := mw.CreateFormFile("document", docName)
		if err != nil {
			b.t.Fatal(err)
		}

		fw.Write([]byte(doc))
	}

	mw.Close()

	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Sec-Fetch-Site", "same-origin")

	return b.do(r)
}

// Invented: a ticket somebody else paid, with its document.
const ticket = "%PDF-1.4\n% an invented ticket\n"

func TestEnteringATransactionByHand(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	wantBody(t, e.owner.get(e.ownAccount+"/transactions"), "Enter a transaction by hand", `name="document"`)

	fields := url.Values{"date": {"2026-09-12"}, "description": {"Plane ticket, paid by the province"}, "amount": {"1,200.00"}, "direction": {"out"}}

	rec := e.owner.entry(e.ownAccount+"/entries", fields, "ticket.pdf", ticket)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/transactions/") || !strings.HasSuffix(loc, "?done=entered") {
		t.Fatalf("enter: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	wantBody(t, e.owner.get(loc), "Entered. Its document is kept with it.", "Plane ticket, paid by the province", "-$1,200.00")
	wantBody(t, e.owner.get(e.ownAccount+"/transactions?month=2026-09"), "Plane ticket, paid by the province", "entered by hand")
	wantBody(t, e.owner.get(e.ownAccount), "entered by hand Plane ticket, paid by the province, -$1,200.00 on 2026-09-12")

	// The same again, a document that is not one, and no document.
	if rec := e.owner.entry(e.ownAccount+"/entries", fields, "again.pdf", ticket); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("the same again: %d", rec.Code)
	} else {
		wantBody(t, rec, "is in this account already")
	}

	if rec := e.owner.entry(e.ownAccount+"/entries", fields, "notes.csv", "a,b\n"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a CSV: %d", rec.Code)
	} else {
		wantBody(t, rec, "must be a photo or a PDF")
	}

	if rec := e.owner.entry(e.ownAccount+"/entries", fields, "", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no document: %d", rec.Code)
	} else {
		wantBody(t, rec, "Choose the document")
	}

	// A stranger gets nothing, and stores nothing.
	stranger := signUp(t, h, sent, "stranger@example.org")
	for _, account := range []string{e.account, e.ownAccount} {
		if rec := stranger.entry(account+"/entries", fields, "ticket.pdf", ticket); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger entering in %s: %d", account, rec.Code)
		}
	}
}
