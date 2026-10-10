package muxer

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Every receipt here is invented: the start of a JPEG, which is all that is
// read of it, and a line of words.
func photoOf(s string) string { return "\xff\xd8\xff\xe0\x00\x10JFIF\x00" + s }

// receipts sends files as the receipt upload form would, with its other
// fields first.
func (b *browser) receipts(path string, fields url.Values, files ...[2]string) *httptest.ResponseRecorder {
	b.t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	for name, values := range fields {
		for _, v := range values {
			mw.WriteField(name, v)
		}
	}

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

var receiptLink = regexp.MustCompile(`href="(/receipts/[0-9a-f]{32})"`)

// receiptsOn is the receipt pages linked from a page, in order, once each.
func receiptsOn(body string) []string {
	var out []string

	seen := map[string]bool{}
	for _, m := range receiptLink.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}

	return out
}

// The design's test, as far as a test can take it: somebody given only the
// project photographs three receipts into it from the front page, and the
// treasurer matches one to its charge.
func TestThreeReceiptsIntoTheProjectAndOneMatched(t *testing.T) {
	e, txs, _, signUpAs := sorted(t)

	wantRedirect(t, e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"contributor"}}), e.project+"?done=granted")
	pilgrim := signUpAs("pilgrim@example.org")

	wantBody(t, pilgrim.get("/"), `href="`+e.project+`/receipts"`, "Add receipts")

	rec := pilgrim.receipts(e.project+"/receipts", nil,
		[2]string{"IMG_0001.jpg", photoOf("one")},
		[2]string{"IMG_0002.jpg", photoOf("two")},
		[2]string{"IMG_0003.jpg", photoOf("three")},
		[2]string{"notes.txt", "not a receipt"},
	)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, e.project+"/receipts?") {
		t.Fatalf("upload: %d to %q\n%s", rec.Code, loc, rec.Body.String())
	}

	inbox := pilgrim.get(loc).Body.String()
	for _, want := range []string{"3 receipts added", "notes.txt", "were not kept"} {
		if !strings.Contains(inbox, want) {
			t.Errorf("the inbox lacks %q", want)
		}
	}

	added := receiptsOn(inbox)
	if len(added) != 3 {
		t.Fatalf("%d receipts on the inbox", len(added))
	}

	// The treasurer sees them waiting, adds what is on the paper, and
	// matches the one that is the grocery.
	wantBody(t, e.owner.get("/"), "3 receipts waiting for a match")

	wantRedirect(t, e.owner.post(added[0]+"/details", url.Values{"amount": {"33,99"}, "spent_on": {"2026-07-05"}, "merchant": {"Corner Grocery"}}),
		added[0]+"?done=saved")

	waiting := e.owner.get("/receipts").Body.String()
	if !strings.Contains(waiting, "Might be:") || !strings.Contains(waiting, "CORNER GROCERY") {
		t.Fatalf("no suggestion on the waiting list:\n%s", waiting)
	}

	grocery := txs["CORNER GROCERY"]
	id := strings.TrimPrefix(grocery, "/transactions/")

	wantRedirect(t, e.owner.post(added[0]+"/attach", url.Values{"transaction": {id}, "back": {"/receipts"}}), "/receipts?done=attached")
	wantBody(t, e.owner.get("/"), "2 receipts waiting for a match")
	wantBody(t, e.owner.get(grocery), "Corner Grocery", "Take it off")
	wantBody(t, e.owner.get(e.account+"/transactions"), "1 receipts")
	wantBody(t, e.owner.get(added[0]), "Attached to", "CORNER GROCERY", "Added by")

	// The photo is shown in the page; it is served as what it is.
	f := e.owner.get(added[0] + "/files/0")
	if f.Code != http.StatusOK || f.Header().Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(f.Header().Get("Content-Disposition"), "inline") {
		t.Errorf("the photo: %d %q %q", f.Code, f.Header().Get("Content-Type"), f.Header().Get("Content-Disposition"))
	}

	// These photos are only a JPEG's first bytes, so they have no smaller
	// picture: asked for one, the page is sent to the original.
	wantRedirect(t, e.owner.get(added[0]+"/files/0/small"), added[0]+"/files/0")

	// The pilgrim, who cannot see the card, still sees their receipt and
	// where it went -- and is given no way into the card.
	page := pilgrim.get(added[0]).Body.String()
	if !strings.Contains(page, "CORNER GROCERY") || strings.Contains(page, `href="`+grocery+`"`) {
		t.Errorf("the pilgrim's view of the matched receipt:\n%s", page)
	}

	// Taken off, it waits again.
	wantRedirect(t, e.owner.post(added[0]+"/detach", url.Values{"transaction": {id}}), added[0]+"?done=detached")
	wantBody(t, e.owner.get("/"), "3 receipts waiting for a match")
}

func TestPagesOfOneReceiptAndRemoving(t *testing.T) {
	e, _, _, _ := sorted(t)

	rec := e.owner.receipts(e.account+"/receipts", url.Values{"together": {"1"}, "note": {"hostel, two nights"}},
		[2]string{"page1.jpg", photoOf("1")},
		[2]string{"invoice.pdf", "%PDF-1.7\n%invented\n"},
	)

	inbox := e.owner.get(rec.Header().Get("Location")).Body.String()

	added := receiptsOn(inbox)
	if len(added) != 1 || !strings.Contains(inbox, "2 pages") || !strings.Contains(inbox, "hostel, two nights") {
		t.Fatalf("one receipt of two pages: %v\n%s", added, inbox)
	}

	// The PDF is a download, never a document on this site.
	pdf := e.owner.get(added[0] + "/files/1")
	if pdf.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(pdf.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("the PDF: %q %q", pdf.Header().Get("Content-Type"), pdf.Header().Get("Content-Disposition"))
	}

	wantRedirect(t, e.owner.post(added[0]+"/remove", url.Values{"removed": {"1"}}), e.account+"/receipts?done=removed")
	wantBody(t, e.owner.get(e.account+"/receipts"), "Removed receipts", "None. Every receipt here is attached")
	wantBody(t, e.owner.get(e.account), "added 1 receipts", "removed the receipt hostel")

	wantRedirect(t, e.owner.post(added[0]+"/remove", url.Values{"removed": {"0"}}), e.account+"/receipts?done=restored")
}

func TestAReceiptStraightOntoATransaction(t *testing.T) {
	e, txs, _, _ := sorted(t)
	coffee := txs["COFFEE CART"]

	rec := e.owner.receipts(coffee+"/receipts", nil, [2]string{"coffee.jpg", photoOf("coffee")})
	loc := rec.Header().Get("Location")

	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, coffee+"?") {
		t.Fatalf("upload: %d to %q", rec.Code, loc)
	}

	wantBody(t, e.owner.get(loc), "1 receipts added to this transaction", "coffee.jpg", "Take it off")
}

// A receipt is as private as its inbox and the transactions it is on.
func TestReceiptsAreAsPrivateAsTheirInbox(t *testing.T) {
	e, _, _, signUpAs := sorted(t)

	rec := e.owner.receipts(e.project+"/receipts", nil, [2]string{"a.jpg", photoOf("a")})
	receipt := receiptsOn(e.owner.get(rec.Header().Get("Location")).Body.String())[0]

	stranger := signUpAs("stranger@example.org")

	for _, path := range []string{receipt, receipt + "/files/0", receipt + "/files/0/small", e.project + "/receipts", e.account + "/receipts"} {
		if rec := stranger.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger: GET %s = %d", path, rec.Code)
		}
	}

	for path, form := range map[string]url.Values{
		receipt + "/details": {"merchant": {"Mine"}},
		receipt + "/number":  {"number": {"1176"}},
		receipt + "/attach":  {"transaction": {strings.Repeat("a", 32)}},
		receipt + "/detach":  {"transaction": {strings.Repeat("a", 32)}},
		receipt + "/remove":  {"removed": {"1"}},
	} {
		if rec := stranger.post(path, form); rec.Code != http.StatusNotFound {
			t.Errorf("a stranger: POST %s = %d", path, rec.Code)
		}
	}

	if rec := stranger.receipts(e.project+"/receipts", nil, [2]string{"b.jpg", photoOf("b")}); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's upload: %d", rec.Code)
	}

	if body := stranger.get("/receipts").Body.String(); strings.Contains(body, receipt) {
		t.Error("a stranger's waiting list shows the owner's receipt")
	}

	wantRedirect(t, e.owner.post(e.project+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.project+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	if rec := viewer.receipts(e.project+"/receipts", nil, [2]string{"c.jpg", photoOf("c")}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer's upload: %d", rec.Code)
	}

	if rec := viewer.get(receipt); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `action="`+receipt+`/details"`) {
		t.Errorf("a viewer reads, and is offered no changes: %d", rec.Code)
	}

	if rec := viewer.post(receipt+"/details", url.Values{"merchant": {"Mine"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer's edit: %d", rec.Code)
	}
}

// drawnPhoto is a JPEG drawn here, never a real photo.
func drawnPhoto(t *testing.T, w, h int) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// A list shows a receipt's small picture and its page the large one; both
// are made on the first request, and the original is still a tap away.
func TestReceiptsAreShownSmaller(t *testing.T) {
	e, _, _, _ := sorted(t)

	rec := e.owner.receipts(e.project+"/receipts", nil, [2]string{"till.jpg", drawnPhoto(t, 1200, 2400)})
	inbox := e.owner.get(rec.Header().Get("Location")).Body.String()
	receipt := receiptsOn(inbox)[0]

	if !strings.Contains(inbox, receipt+`/files/0/small"`) {
		t.Errorf("the inbox does not show the small picture:\n%s", inbox)
	}

	wantBody(t, e.owner.get(receipt), receipt+`/files/0/large"`, `href="`+receipt+`/files/0"`)

	for size, want := range map[string][2]int{"small": {400, 800}, "large": {800, 1600}} {
		got := e.owner.get(receipt + "/files/0/" + size)
		if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s: %d %q", size, got.Code, got.Header().Get("Content-Type"))
		}

		cfg, err := jpeg.DecodeConfig(got.Body)
		if err != nil || cfg.Width != want[0] || cfg.Height != want[1] {
			t.Errorf("%s: %dx%d %v", size, cfg.Width, cfg.Height, err)
		}
	}

	if got := e.owner.get(receipt + "/files/0/huge"); got.Code != http.StatusSeeOther {
		t.Errorf("a size that is not one: %d", got.Code)
	}
}
