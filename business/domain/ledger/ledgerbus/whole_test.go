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
