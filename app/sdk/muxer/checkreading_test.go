package muxer

import (
	"bytes"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Claude sees a receipt's page: the waiting list says which are the images
// of checks and how many pages each has, and a page is an image, at most
// 1600 pixels on its longer side, both from the API and as a tool's image
// on /mcp. A PDF is refused with the receipt's page for a person, and a
// stranger's key finds nothing.
func TestClaudeSeesAReceipt(t *testing.T) {
	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Claude", "books-read")

	rec := e.owner.receipts(e.account+"/checks", nil, [2]string{"Screenshot_20260712-101500.jpg", drawnPhoto(t, 2000, 1000)})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a check's screenshot: %d", rec.Code)
	}

	rec = e.owner.receipts(e.project+"/receipts", nil, [2]string{"invoice.pdf", "%PDF-1.7\n%invented\n"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a PDF: %d", rec.Code)
	}

	var check, invoice string

	for _, rc := range list(t, s.get(t, "/api/v1/receipts/waiting", key), "receipts") {
		switch {
		case field(rc, "check_image") == true && field(rc, "check") == "" && field(rc, "pages") == 1.0:
			check = field(rc, "id").(string)
		case field(rc, "check_image") == nil && field(rc, "check") == nil:
			invoice = field(rc, "id").(string)
		}
	}

	if check == "" || invoice == "" {
		t.Fatalf("the waiting list does not tell the check's image from the invoice: %v", s.get(t, "/api/v1/receipts/waiting", key))
	}

	page := s.api(http.MethodGet, "/api/v1/receipts/"+check+"/image", key, "")
	if page.Code != http.StatusOK || page.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("the check's page: %d %q\n%s", page.Code, page.Header().Get("Content-Type"), page.Body)
	}

	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(page.Body.Bytes())); err != nil || cfg.Width != 1600 || cfg.Height != 800 {
		t.Errorf("the check's page is %dx%d: %v", cfg.Width, cfg.Height, err)
	}

	for path, want := range map[string]int{
		"/api/v1/receipts/" + check + "/image?page=2":            http.StatusNotFound,
		"/api/v1/receipts/" + check + "/image?page=0":            http.StatusBadRequest,
		"/api/v1/receipts/" + invoice + "/image":                 http.StatusUnsupportedMediaType,
		"/api/v1/receipts/" + strings.Repeat("a", 32) + "/image": http.StatusNotFound,
	} {
		if rec := s.api(http.MethodGet, path, key, ""); rec.Code != want {
			t.Errorf("GET %s: %d, want %d\n%s", path, rec.Code, want, rec.Body)
		}
	}

	if body := s.api(http.MethodGet, "/api/v1/receipts/"+invoice+"/image", key, "").Body.String(); !strings.Contains(body, "/receipts/"+invoice) {
		t.Errorf("a PDF is refused without its page: %s", body)
	}

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	if rec := s.api(http.MethodGet, "/api/v1/receipts/"+check+"/image", keyFor(t, stranger, "Claude", "books-read"), ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's key: %d", rec.Code)
	}

	// The same page, as Claude on claude.ai is given it.
	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_receipt_image", Arguments: map[string]any{"receipt": check}})
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("get_receipt_image: %v %s", err, toolText(res))
	}

	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/jpeg" {
		t.Fatalf("get_receipt_image answered %T", res.Content[0])
	}

	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(img.Data)); err != nil || cfg.Width != 1600 {
		t.Errorf("the tool's image is %dx%d: %v", cfg.Width, cfg.Height, err)
	}

	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_receipt_image", Arguments: map[string]any{"receipt": invoice}})
	if err != nil || !res.IsError || !strings.Contains(toolText(res), "PDF") {
		t.Errorf("a PDF through the tool: %v %s", err, toolText(res))
	}
}

// Claude on claude.ai reads a month's screenshots of checks: lists them,
// looks at each, and says what it read. A reading the bank agrees with
// attaches the image, with whom it was paid to beside the check in the
// month; one it does not is refused with both amounts and changes
// nothing; one not cleared waits. The account's history says what was
// read, and through which key, and so does list_changes.
func TestClaudeReadsChecksThroughMCP(t *testing.T) {
	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Claude", "books-keep")

	preview := uploaded(t, e.owner, e.account, "july.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n"+
		"2026-07-02,CHECK,-120.00,880.00,1176\n"+
		"2026-07-09,CHECK,-30.00,850.00,1177\n")
	form := columns("import")
	form.Set("check", "Check Number")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d", rec.Code)
	}

	rec := e.owner.receipts(e.account+"/checks", nil,
		[2]string{"Screenshot_1.jpg", drawnPhoto(t, 900, 400)}, [2]string{"Screenshot_2.jpg", drawnPhoto(t, 901, 400)}, [2]string{"Screenshot_3.jpg", drawnPhoto(t, 902, 400)})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the screenshots: %d", rec.Code)
	}

	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	call := func(name string, args map[string]any) (*mcp.CallToolResult, string) {
		t.Helper()

		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		return res, toolText(res)
	}

	var shots []string

	for _, rc := range list(t, s.get(t, "/api/v1/receipts/waiting", key), "receipts") {
		if field(rc, "check_image") == true {
			shots = append(shots, field(rc, "id").(string))
		}
	}

	if len(shots) != 3 {
		t.Fatalf("%d check images waiting, want 3", len(shots))
	}

	for _, id := range shots {
		if res, _ := call("get_receipt_image", map[string]any{"receipt": id}); res.IsError {
			t.Errorf("looking at %s: %s", id, toolText(res))
		}
	}

	res, text := call("read_check", map[string]any{"receipt": shots[0], "number": "1176", "amount": "120.00", "payee": "Hilltop Plumbing", "date": "2026-06-30", "memo": "Summer work"})
	if res.IsError || !strings.Contains(text, `"attached": true`) || !strings.Contains(text, `"check": "1176"`) {
		t.Fatalf("read_check 1176: %s", text)
	}

	res, text = call("read_check", map[string]any{"receipt": shots[1], "number": "1177", "amount": "80.00"})
	if !res.IsError || !strings.Contains(text, "The bank paid check 1177 for 30.00, and you read 80.00") {
		t.Errorf("a misread: %s", text)
	}

	res, text = call("read_check", map[string]any{"receipt": shots[2], "number": "1190", "amount": "45.50", "payee": "Diocesan Office", "date": "2026-07-05", "memo": "Cleaning"})
	if res.IsError || !strings.Contains(text, `"attached": false`) {
		t.Errorf("a check not cleared: %s", text)
	}

	res, text = call("read_check", map[string]any{"receipt": shots[0], "number": "1176", "amount": "120.00"})
	if !res.IsError || !strings.Contains(text, "attached to a transaction already") {
		t.Errorf("reading an attached check again: %s", text)
	}

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), "Paid to Hilltop Plumbing", "Memo: Summer work")

	// The day it was written stays beside the day it cleared, and the check
	// not cleared is outstanding: on the account's months, on the
	// statement whose period it was written in, and to Claude.
	acct := strings.TrimPrefix(e.account, "/accounts/")

	var paid any
	for _, tx := range list(t, s.get(t, "/api/v1/accounts/"+acct+"/transactions?month=2026-07", key), "transactions") {
		if field(tx, "check") == "1176" {
			paid = tx
		}
	}

	if field(paid, "check_memo") != "Summer work" || field(paid, "written") != "2026-06-30" || field(paid, "date") != "2026-07-02" {
		t.Fatalf("check 1176 to Claude: %v", paid)
	}

	wantBody(t, e.owner.get("/transactions/"+field(paid, "id").(string)), "The check was written on 2026-06-30 and cleared on 2026-07-02.")
	wantBody(t, e.owner.get(e.account+"/months"), "Checks outstanding", "Check 1190", "written 2026-07-05", "memo: Cleaning", "Checks outstanding: 1, for $45.50 in all.")

	statement := field(list(t, s.get(t, "/api/v1/accounts/"+acct+"/statements", key), "statements")[0], "id").(string)
	wantBody(t, e.owner.get("/statements/"+statement), "Checks written by 2026-07-09", "Check 1190")

	months := s.get(t, "/api/v1/accounts/"+acct+"/months", key)
	if out := field(months, "outstanding_checks"); field(out, "total") != "45.50" || len(field(out, "checks").([]any)) != 1 || field(field(out, "checks").([]any)[0], "memo") != "Cleaning" {
		t.Errorf("the outstanding checks to Claude: %v", out)
	}

	// A person corrects the memo and the day on the image's page, and the
	// transaction follows, with a line in the account's history.
	wantBody(t, e.owner.get("/receipts/"+shots[0]), "Memo: Summer work.", "Written on 2026-06-30.", `name="written_on"`)

	if rec := e.owner.post("/receipts/"+shots[0]+"/details", url.Values{
		"amount": {"120.00"}, "spent_on": {"2026-07-02"}, "merchant": {"Hilltop Plumbing"}, "note": {""},
		"memo": {"Summer work, July"}, "written_on": {"2026-06-29"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("correcting the check: %d\n%s", rec.Code, rec.Body)
	}

	wantBody(t, e.owner.get("/transactions/"+field(paid, "id").(string)), "memo: Summer work, July", "The check was written on 2026-06-29")

	wantBody(t, e.owner.get(e.account), "read the image of check 1176, for 120.00", "read the image of check 1190, for 45.50",
		"was written on 2026-06-29, for “Summer work, July”")

	_, text = call("list_changes", map[string]any{})
	if !strings.Contains(text, "receipt.check_read") {
		t.Errorf("list_changes does not say what was read: %s", text)
	}

	// A key that only reads may look, and may not say.
	reader := keyFor(t, e.owner, "Claude", "books-read")
	if rec := s.api(http.MethodPost, "/api/v1/receipts/"+shots[1]+"/check", reader, `{"number": "1177", "amount": "30.00"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a reading key: %d", rec.Code)
	}

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")
	if rec := s.api(http.MethodPost, "/api/v1/receipts/"+shots[1]+"/check", keyFor(t, stranger, "Claude", "books-keep"), `{"number": "1177", "amount": "30.00"}`); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's key: %d", rec.Code)
	}

	if rec := s.api(http.MethodPost, "/api/v1/receipts/"+shots[1]+"/check", key, `{"number": "1177"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no amount: %d", rec.Code)
	}
}
