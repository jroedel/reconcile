package ledgerbus_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/types"
)

// One card's month from its bank's OFX, which cuts every name at twelve
// characters, and from its CSV, which does not (ledgerdb's complete). The
// shops and amounts are invented.

type cardRow struct{ date, whole, cut, amount string }

var cardMonth = []cardRow{
	{"20251202", "HARDWARE BARN #12 SPRINGFIELD", "HARDWARE BAR", "-139.07"},
	{"20251203", "COFFEE CART 0902 SEATTLE", "COFFEE CART ", "-3.50"},
	{"20251203", "COFFEE CART 0902 SEATTLE", "COFFEE CART ", "-3.50"},
	{"20251204", "BOOK STALL #4 PORTLAND", "BOOK STALL #", "-20.00"},
	{"20251205", "BAKERY", "BAKERY", "-4.00"},
}

func cardOFX() []byte {
	var b strings.Builder

	b.WriteString("OFXHEADER:100\nDATA:OFXSGML\nVERSION:102\n\n<OFX>\n<CREDITCARDMSGSRSV1><CCSTMTTRNRS><TRNUID>1\n<CCSTMTRS>\n<CURDEF>USD\n<BANKTRANLIST>\n<DTSTART>20251201<DTEND>20251231\n")

	for i, r := range cardMonth {
		fmt.Fprintf(&b, "<STMTTRN>\n<TRNTYPE>DEBIT\n<DTPOSTED>%s\n<TRNAMT>%s\n<FITID>DEC-%d\n<NAME>%s\n</STMTTRN>\n", r.date, r.amount, i, r.cut)
	}

	b.WriteString("</BANKTRANLIST>\n</CCSTMTRS></CCSTMTTRNRS></CREDITCARDMSGSRSV1>\n</OFX>\n")

	return []byte(b.String())
}

func cardCSV() []byte {
	rows := make([]string, 0, len(cardMonth))
	for _, r := range cardMonth {
		rows = append(rows, r.date[:4]+"-"+r.date[4:6]+"-"+r.date[6:]+","+r.whole+","+r.amount)
	}

	return csvOf(rows...)
}

// wholeAndIdentified checks the account ends with every charge once, in
// its whole words, and with the bank's identifier.
func (w *world) wholeAndIdentified(actor, account types.ID) {
	w.t.Helper()

	txs, err := w.ledger.Transactions(w.t.Context(), actor, account, "2025-12")
	if err != nil || len(txs) != len(cardMonth) {
		w.t.Fatalf("December: %d, %v", len(txs), err)
	}

	var got, want []string
	for _, tx := range txs {
		got = append(got, tx.Description)

		if tx.ExternalID == "" {
			w.t.Errorf("no identifier: %+v", tx)
		}
	}

	for _, r := range cardMonth {
		want = append(want, r.whole)
	}

	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		w.t.Errorf("descriptions %q, want %q", got, want)
	}
}

func TestTheOFXThenTheCSV(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	if got := counts(w.add(me, card, "december.ofx", cardOFX())); got != [3]int{5, 0, 0} {
		t.Fatalf("OFX: added, already, set aside = %v", got)
	}

	// The coffees as well, each by its own: two charges alike are two.
	d, _ := w.preview(me, card, "december.csv", cardCSV())
	if d.Statement.Completed != 4 {
		t.Errorf("the preview completes %d", d.Statement.Completed)
	}

	if st := w.add(me, card, "december.csv", cardCSV()); counts(st) != [3]int{0, 5, 0} || st.Completed != 4 {
		t.Fatalf("CSV after OFX: %v, completed %d", counts(st), st.Completed)
	}

	w.wholeAndIdentified(me, card)

	// Each file again, downloaded another day, matches exactly.
	if got := counts(w.add(me, card, "december-again.csv", append(cardCSV(), '\n'))); got != [3]int{0, 5, 0} {
		t.Errorf("CSV again: %v", got)
	}

	if got := counts(w.add(me, card, "december-again.ofx", append(cardOFX(), '\n'))); got != [3]int{0, 5, 0} {
		t.Errorf("OFX again: %v", got)
	}

	w.wholeAndIdentified(me, card)
}

func TestTheCSVThenTheOFX(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	w.add(me, card, "december.csv", cardCSV())

	if st := w.add(me, card, "december.ofx", cardOFX()); counts(st) != [3]int{0, 5, 0} || st.Completed != 0 {
		t.Fatalf("OFX after CSV: %v, completed %d", counts(st), st.Completed)
	}

	w.wholeAndIdentified(me, card)
}

// What was reconciled is left as it was: matched, and not rewritten.
func TestAReconciledPeriodKeepsItsCutDescriptions(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	st := w.add(me, card, "december.ofx", cardOFX())

	if _, err := w.db.ExecContext(t.Context(), `
INSERT INTO reconciliations (statement_id, account_id, period_start, period_end, reconciled_by, reconciled_at)
VALUES (?, ?, '2025-12-01', '2025-12-31', ?, 0)`, st.ID.String(), card.String(), me.String()); err != nil {
		t.Fatal(err)
	}

	if st := w.add(me, card, "december.csv", cardCSV()); counts(st) != [3]int{0, 5, 0} || st.Completed != 0 {
		t.Fatalf("CSV into a reconciled period: %v, completed %d", counts(st), st.Completed)
	}

	txs, _ := w.ledger.Transactions(t.Context(), me, card, "2025-12")
	for _, tx := range txs {
		if tx.Description != "BAKERY" && !strings.HasSuffix(tx.Description, "…") {
			t.Errorf("rewritten in a reconciled period: %q", tx.Description)
		}
	}
}

// A printed page's tidy name and the bank's own line for one charge: the
// count rule sets the second aside, and the row here takes the line that
// says more, whichever came first, and keeps it through the holder
// option's changes.
func TestTheRowHereTakesTheWordsThatSayMore(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	page := csvOf("2026-01-29,Home Depot,-4.56", "2026-01-30,Coffee Cart,-3.50")
	bank := []byte("Status,Date,Description,Debit,Credit\nCleared,01/29/2026,THE HOME DEPOT #6531 BEE CAVES TX,4.56,\nCleared,01/30/2026,SQ *COFFEE CART 0902 SEATTLE WA,3.50,\n")

	w.add(me, card, "page.csv", page)

	if st := w.add(me, card, "bank.csv", bank); counts(st) != [3]int{0, 2, 2} || st.Completed != 2 {
		t.Fatalf("the bank's file: %v, completed %d", counts(st), st.Completed)
	}

	descriptions := func() []string {
		txs, err := w.ledger.Transactions(t.Context(), me, card, "2026-01")
		if err != nil {
			t.Fatal(err)
		}

		var out []string
		for _, tx := range txs {
			out = append(out, tx.Description)
		}

		slices.Sort(out)

		return out
	}

	want := []string{"SQ *COFFEE CART 0902 SEATTLE WA", "THE HOME DEPOT #6531 BEE CAVES TX"}
	if got := descriptions(); !slices.Equal(got, want) {
		t.Fatalf("descriptions %q", got)
	}

	// Each file again matches exactly, with the option changed between.
	for _, on := range []bool{true, false} {
		if err := w.ledger.SetByHolder(t.Context(), now, me, card, on); err != nil {
			t.Fatal(err)
		}

		if got := counts(w.add(me, card, "page-again.csv", append(page, '\n'))); got != [3]int{0, 2, 0} {
			t.Errorf("the page again, option %v: %v", on, got)
		}

		if got := counts(w.add(me, card, "bank-again.csv", append(bank, '\n'))); got != [3]int{0, 2, 0} {
			t.Errorf("the bank's file again, option %v: %v", on, got)
		}

		page, bank = append(page, '\n'), append(bank, '\n')
	}

	// The other way round, the bank's line stays.
	other := w.account(me, "card")
	w.add(me, other, "bank.csv", bank)

	if st := w.add(me, other, "page.csv", page); counts(st) != [3]int{0, 2, 2} || st.Completed != 0 {
		t.Errorf("the page after the bank's file: %v, completed %d", counts(st), st.Completed)
	}
}

// A pending row the same file lists as posted too is kept, cleared by the
// posted one, so that the file is all there; a pending row with nothing
// posted for it waits.
func TestAPendingRowPostedInTheSameFile(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	month := []byte("Date,Description,Amount,Status\n" +
		"2026-01-28,TST* THE PIZZA PRESS - AUSTIN,-11.54,Pending\n" +
		"2026-01-29,TST* THE PIZZA PRESS - AUSTIN,-11.54,Posted\n" +
		"2026-06-11,CAR HIRE CHANIA,-925.92,Pending\n" +
		"2026-06-11,CAR HIRE CHANIA,-753.64,Posted\n" +
		"2026-01-18,GOOGLE *GPAY TEMP,-1.00,Pending\n")

	d, _ := w.preview(me, card, "month.csv", month)
	if d.Statement.Superseded != 2 || d.Statement.Added != 5 {
		t.Fatalf("preview: superseded %d, added %d", d.Statement.Superseded, d.Statement.Added)
	}

	w.add(me, card, "month.csv", month)

	june, err := w.ledger.Transactions(t.Context(), me, card, "2026-06")
	if err != nil || len(june) != 2 {
		t.Fatalf("June: %+v, %v", june, err)
	}

	hold, posted := june[0], june[1]
	if !hold.Pending {
		hold, posted = posted, hold
	}

	if hold.PostedAs != posted.ID || len(hold.Splits) != 0 || len(posted.Splits) != 1 {
		t.Errorf("the car hire: hold %+v, posted %+v", hold, posted)
	}

	// Each month spends its posted charges and the hold that never posted.
	months, _ := w.ledger.Months(t.Context(), me, card)
	for _, m := range months {
		if want := map[string]int64{"2026-01": -1254, "2026-06": -75364}[m.Month]; int64(m.Out) != want {
			t.Errorf("%s: out %s, want %d", m.Month, m.Out, want)
		}
	}

	pending, err := w.ledger.StillPending(t.Context(), me, card)
	if err != nil || len(pending) != 1 || pending[0].Description != "GOOGLE *GPAY TEMP" {
		t.Errorf("still pending: %+v, %v", pending, err)
	}

	// The same rows again: nothing new, and nothing set aside.
	if got := counts(w.add(me, card, "month-again.csv", append(month, '\n'))); got != [3]int{0, 5, 0} {
		t.Errorf("again: %v", got)
	}
}
