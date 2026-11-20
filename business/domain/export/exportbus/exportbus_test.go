package exportbus

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
)

// Everything here is invented.

func day(s string) types.Date {
	d, err := types.ParseDate(s)
	if err != nil {
		panic(err)
	}

	return d
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"CORNER GROCERY #42":    "corner-grocery-42",
		"Café São José":         "café-são-josé",
		"  ../../etc/passwd  ":  "etc-passwd",
		"!!!":                   "receipt",
		strings.Repeat("a", 90): strings.Repeat("a", MaxSlug),
	} {
		if got := slug(in, "receipt"); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}

	if got := safeName("../july/statement.csv"); got != "_july_statement.csv" {
		t.Errorf("safeName = %q", got)
	}

	n := newNamer()
	for _, want := range []string{"receipts/a_1.jpg", "receipts/a_1-2.jpg", "receipts/a_1-3.jpg"} {
		if got := n.free("receipts/a_1.jpg"); got != want {
			t.Errorf("free = %q, want %q", got, want)
		}
	}

	grocery := ledgerbus.Transaction{PostedOn: day("2026-07-03"), Description: "CORNER GROCERY", Amount: -3399}

	// From the transaction, when nothing was typed on the receipt.
	if got := receiptPath(receiptbus.Receipt{}, grocery, 1, filebus.JPEG); got != "receipts/2026-07-03_33.99_corner-grocery_1.jpg" {
		t.Errorf("receiptPath = %q", got)
	}

	// From the receipt, when something was: the shop's own date and total.
	typed := receiptbus.Receipt{Details: receiptbus.Details{SpentOn: day("2026-07-02"), Amount: 3000, HasAmount: true, Merchant: "Corner Grocery"}}
	if got := receiptPath(typed, grocery, 2, filebus.PDF); got != "receipts/2026-07-02_30.00_corner-grocery_2.pdf" {
		t.Errorf("receiptPath = %q", got)
	}

	// A check no transaction of the period paid: by its number, the day
	// written on it and its amount, as far as those were read.
	void := receiptbus.Receipt{Check: "1190", Details: receiptbus.Details{WrittenOn: day("2026-07-05"), Amount: 4550, HasAmount: true, Merchant: "Diocesan Office"}}
	if got := checkImagePath(void, 1, filebus.JPEG); got != "checks/1190_2026-07-05_45.50_diocesan-office_1.jpg" {
		t.Errorf("checkImagePath = %q", got)
	}

	if got := checkImagePath(receiptbus.Receipt{Check: "1191"}, 2, filebus.PNG); got != "checks/1191_2.png" {
		t.Errorf("checkImagePath, nothing read but the number = %q", got)
	}
}

// files is invented bytes by file ID.
type files map[types.ID]string

func (f files) ByID(_ context.Context, id types.ID) (filebus.File, error) {
	return filebus.File{ID: id}, nil
}

func (f files) Open(file filebus.File) (io.ReadSeekCloser, error) {
	return readSeekCloser{strings.NewReader(f[file.ID])}, nil
}

type readSeekCloser struct{ *strings.Reader }

func (readSeekCloser) Close() error { return nil }

func TestWritingAPackage(t *testing.T) {
	photo := types.NewID()
	b := &Business{files: files{photo: "\xff\xd8\xffinvented"}}

	p := Package{
		Rows: []Row{{
			PostedOn: day("2026-07-03"), Account: "Parish checking", Description: "=HYPERLINK(\"http://x.invalid\")",
			Amount: -3000, Total: -3399, Currency: "USD", Category: "Groceries", Kind: categorybus.Expense, Project: "Café", Memo: "-light bulbs",
			Receipts: []string{"receipts/a_1.jpg"}, Statement: "july.csv", Reconciled: day("2026-08-03"),
			CheckNumber: "1176", Payee: "Hilltop Plumbing", OwnDescription: "+Summer work", CheckMemo: "Summer work", WrittenOn: day("2026-06-28"), ReportedPayee: "Hilltop Plumbing",
		}},
		Entries: []Entry{{Path: "receipts/a_1.jpg", File: filebus.File{ID: photo}}},
	}

	header := make([]string, Columns)
	for i := range header {
		header[i] = "c" + string(rune('a'+i))
	}

	words := Words{
		Columns: header, Kinds: map[categorybus.Kind]string{categorybus.Expense: "Spent"}, Explanations: make([]string, ExplanationColumns),
		Checks:      []string{"Number", "Written", "Paid to", "Amount", "Memo", "At the end", "Cleared", "Voided", "Image"},
		CheckStates: map[receiptbus.CheckState]string{receiptbus.NumberOutstanding: "Outstanding", receiptbus.NumberAttached: "Cleared", receiptbus.NumberVoid: "Void"},
	}

	var out bytes.Buffer
	if err := b.Write(t.Context(), time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC), &out, p, words); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}

	if len(zr.File) != 2 || zr.File[0].Name != "transactions.csv" || zr.File[1].Name != "receipts/a_1.jpg" || zr.File[1].Method != zip.Store {
		t.Fatalf("the zip holds %+v", zr.File)
	}

	read := func(f *zip.File) string {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()

		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}

	sheet := read(zr.File[0])
	if !strings.HasPrefix(sheet, "\ufeffca,cb,") {
		t.Errorf("the spreadsheet does not start with its mark and header: %q", sheet[:20])
	}

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(sheet, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"2026-07-03", "Parish checking", "'=HYPERLINK(\"http://x.invalid\")", "-30.00", "USD", "-33.99",
		"Groceries", "Spent", "Café", "'-light bulbs", "receipts/a_1.jpg", "july.csv", "2026-08-03", "", "", "", "1176", "Hilltop Plumbing", "'+Summer work", "Summer work", "2026-06-28", "Hilltop Plumbing"}

	if len(rows) != 2 || strings.Join(rows[1], "|") != strings.Join(want, "|") {
		t.Errorf("the row:\n got %q\nwant %q", rows[1], want)
	}

	if read(zr.File[1]) != "\xff\xd8\xffinvented" {
		t.Error("the photo changed on the way into the zip")
	}

	if err := b.Write(t.Context(), time.Now(), io.Discard, p, Words{Columns: header[:3], Explanations: words.Explanations, Checks: words.Checks}); err == nil {
		t.Error("a short header was written")
	}

	// With the period's checks: checks.csv after the transactions, a void
	// one among them, its memo kept from running as a formula.
	spoiled := receiptbus.Receipt{Check: "1190", Details: receiptbus.Details{Amount: 4550, HasAmount: true, Merchant: "Diocesan Office", Memo: "=SUM(A1)", WrittenOn: day("2026-07-05")}}
	p.Checks = []CheckRow{
		{CheckLine: receiptbus.CheckLine{Receipt: receiptbus.Receipt{Check: "1176", Details: receiptbus.Details{Amount: 12000, HasAmount: true, Merchant: "Hilltop Plumbing", WrittenOn: day("2026-06-28")}}, State: receiptbus.NumberAttached, ClearedOn: day("2026-07-02")}, Images: []string{"checks/1176_1.jpg", "checks/1176_2.jpg"}},
		{CheckLine: receiptbus.CheckLine{Receipt: spoiled, State: receiptbus.NumberVoid, VoidedOn: day("2026-07-06")}, Images: []string{"checks/1190_2026-07-05_45.50_diocesan-office_1.jpg"}},
		{CheckLine: receiptbus.CheckLine{Receipt: receiptbus.Receipt{Check: "1191"}, State: receiptbus.NumberOutstanding}},
	}

	out.Reset()
	if err := b.Write(t.Context(), time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC), &out, p, words); err != nil {
		t.Fatal(err)
	}

	if zr, err = zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len())); err != nil {
		t.Fatal(err)
	}

	if len(zr.File) != 3 || zr.File[1].Name != "checks.csv" {
		t.Fatalf("the zip with checks holds %+v", zr.File)
	}

	checks, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(read(zr.File[1]), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	wantChecks := [][]string{
		words.Checks,
		{"1176", "2026-06-28", "Hilltop Plumbing", "120.00", "", "Cleared", "2026-07-02", "", "checks/1176_1.jpg; checks/1176_2.jpg"},
		{"1190", "2026-07-05", "Diocesan Office", "45.50", "'=SUM(A1)", "Void", "", "2026-07-06", "checks/1190_2026-07-05_45.50_diocesan-office_1.jpg"},
		{"1191", "", "", "", "", "Outstanding", "", "", ""},
	}

	if fmt.Sprint(checks) != fmt.Sprint(wantChecks) {
		t.Errorf("checks.csv:\n got %q\nwant %q", checks, wantChecks)
	}

	if err := b.Write(t.Context(), time.Now(), io.Discard, p, Words{Columns: header, Explanations: words.Explanations, Checks: words.Checks[:8]}); err == nil {
		t.Error("a short checks header was written")
	}
}
