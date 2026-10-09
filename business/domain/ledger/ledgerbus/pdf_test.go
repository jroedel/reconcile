package ledgerbus_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
)

func needPoppler(t *testing.T) {
	t.Helper()

	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}
}

// printout draws a card's activity page as a browser prints it, with
// invented charges: a header and an address on each page, the cardholder
// in a column of their own, one row whose description went to the next
// page, and the total at the end.
func printout(total string) []byte {
	head := func(n int) pdftexttest.Page {
		p := pdftexttest.Row(30, 40, "Example Card Services Account Activity", 470, "10/2/26, 4:15 PM")
		p = append(p, pdftexttest.Row(770, 20, "https://cards.example.invalid/activity?accountId=0000-0000", 520, "Page "+string(rune('0'+n))+" of 2")...)

		return p
	}

	row := func(y float64, date, desc, amount string) []pdftexttest.Text {
		r := pdftexttest.Row(y, 50, date)
		if desc != "" {
			r = append(r, pdftexttest.Row(y, 150, desc)...)
		}

		return append(r, pdftexttest.Row(y, 330, "PAT EXAMPLE…", 470, amount)...)
	}

	p1 := head(1)
	p1 = append(p1, pdftexttest.Row(60, 50, "Sep 1, 2026 to Sep 30, 2026 | PAT EXAMPLE")...)
	p1 = append(p1, pdftexttest.Row(90, 50, "Date", 150, "Description", 330, "Name", 470, "Amount")...)
	p1 = append(p1, row(120, "Sep 30, 2026", "Corner Hardware", "$3.78")...)
	p1 = append(p1, row(150, "Sep 29, 2026", "", "$40.00")...)

	p2 := head(2)
	p2 = append(p2, pdftexttest.Row(70, 150, "Hilltop Clinic Spri…")...)
	p2 = append(p2, row(100, "Sep 23, 2026", "Café Lumen", "-$0.44")...)
	p2 = append(p2, row(130, "Sep 02, 2026", "Parish Office Supply", "$1,203.22")...)
	p2 = append(p2, pdftexttest.Row(170, 50, "End of Activity", 150, "Total Activity Date range", 470, total)...)

	return pdftexttest.Draw(p1, p2)
}

func (w *world) save(actor types.ID, name string, data []byte) types.ID {
	w.t.Helper()

	f, err := w.files.Save(w.t.Context(), now, actor, name, bytes.NewReader(data), ledgerbus.MaxFile, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	return f.ID
}

// A card's printed activity imports with its purchases as money out,
// checked against the total it states, and with the cardholder kept as
// each part's memo.
func TestAPrintedCardPageImports(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "card")
	file := w.save(me, "activity.pdf", printout("$1,246.56"))

	d, err := w.ledger.Prepare(t.Context(), me, acct, file, nil)
	if err != nil {
		t.Fatal(err)
	}

	if d.Format != ledgerbus.PDF || !d.Invert || d.Check != (ledgerbus.Check{Method: ledgerbus.BySum, OK: true}) || !d.Ready() {
		t.Fatalf("draft: format %s, invert %v, check %+v, ready %v", d.Format, d.Invert, d.Check, d.Ready())
	}

	var got []string
	for _, r := range d.Result.Records {
		got = append(got, r.Description+" "+r.Amount.String())
	}

	want := "Corner Hardware -3.78|Hilltop Clinic Spri… -40.00|Café Lumen 0.44|Parish Office Supply -1203.22"
	if strings.Join(got, "|") != want {
		t.Errorf("rows = %q", strings.Join(got, "|"))
	}

	st, err := w.ledger.Import(t.Context(), now, me, acct, file, ledgerbus.Options{Invert: true})
	if err != nil {
		t.Fatal(err)
	}

	if st.Format != ledgerbus.PDF || st.Checked != ledgerbus.BySum || st.Added != 4 || st.HasClosing ||
		st.Start.String() != "2026-09-01" || st.End.String() != "2026-09-30" {
		t.Errorf("statement = %+v", st)
	}

	txs, err := w.ledger.Transactions(t.Context(), me, acct, "2026-09")
	if err != nil || len(txs) != 4 {
		t.Fatalf("transactions: %d, %v", len(txs), err)
	}

	for _, tx := range txs {
		if len(tx.Splits) != 1 || tx.Splits[0].Memo != "PAT EXAMPLE…" {
			t.Errorf("%s: splits %+v", tx.Description, tx.Splits)
		}
	}
}

// A total the rows do not come to imports nothing, and says both figures.
func TestAPrintedPageThatDoesNotAddUp(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "card")
	file := w.save(me, "activity.pdf", printout("$1,250.00"))

	d, err := w.ledger.Prepare(t.Context(), me, acct, file, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := ledgerbus.Check{Method: ledgerbus.BySum, Expected: money.MustParse("1246.56"), Stated: money.MustParse("1250.00")}
	if d.Check != want || d.Ready() {
		t.Errorf("check = %+v", d.Check)
	}

	if _, err := w.ledger.Import(t.Context(), now, me, acct, file, ledgerbus.Options{Invert: true}); !errors.Is(err, ledgerbus.ErrUnbalanced) {
		t.Errorf("import = %v", err)
	}
}

// The same charge, cut short on the printed page and whole in the
// statement's CSV a month later, is one charge.
func TestACutShortDescriptionIsNotCountedTwice(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "card")

	if _, err := w.ledger.Import(t.Context(), now, me, acct, w.save(me, "activity.pdf", printout("$1,246.56")), ledgerbus.Options{Invert: true}); err != nil {
		t.Fatal(err)
	}

	csv := "Date,Description,Amount\n2026-09-29,HILLTOP CLINIC SPRINGFIELD 0412,-40.00\n2026-09-30,CORNER HARDWARE,-3.78\n2026-09-30,Parish Office Supply,-12.00\n"
	file := w.save(me, "september.csv", []byte(csv))

	d, err := w.ledger.Prepare(t.Context(), me, acct, file, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The clinic is the printout's cut short; Corner Hardware differs only
	// in case, which the hash ignores; the office supply is a new charge.
	if d.Statement.Already != 2 || d.Statement.Added != 1 {
		t.Errorf("already %d, added %d", d.Statement.Already, d.Statement.Added)
	}
}

// What cannot be read is refused with the reason, before anything else.
func TestPDFsItCannotRead(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	letter := pdftexttest.Page{}
	for i, s := range []string{"Dear member,", "Thank you for banking with us. Your new card is on its way", "and will arrive within ten days. Please sign it when it does,", "and call the number on its back to start using it.", "Our branches are open from nine to five on weekdays,", "and from nine to noon on Saturdays, except on holidays."} {
		letter = append(letter, pdftexttest.Text{X: 50, Y: 60 + float64(i)*14, S: s})
	}

	for name, c := range map[string]struct {
		data []byte
		want error
	}{
		"a scan":          {pdftexttest.Draw(pdftexttest.Page{}, pdftexttest.Page{}), ledgerbus.ErrPDFScan},
		"damaged":         {[]byte("%PDF-1.7\n%invented and nothing more\n"), ledgerbus.ErrPDFUnreadable},
		"not a statement": {pdftexttest.Draw(letter), ledgerbus.ErrPDFNoRows},
	} {
		file := w.save(me, name+".pdf", c.data)
		if _, err := w.ledger.Prepare(t.Context(), me, acct, file, nil); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestTruncated(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Hilltop Clinic Spri…", "HILLTOP CLINIC SPRINGFIELD 0412", true},
		{"HILLTOP CLINIC SPRINGFIELD 0412", "Hilltop  Clinic Spri...", true},
		{"Hilltop Clinic…", "Hilltop Cli…", true},
		{"SHELL", "SHELL OIL 123", false},
		{"Hilltop Clinic Spri…", "Corner Hardware", false},
		{"…", "anything", false},
	} {
		if got := ledgerbus.Truncated(c.a, c.b); got != c.want {
			t.Errorf("Truncated(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
