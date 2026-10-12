package ledgerbus_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// A card's export with one signed column, which way round (ledgerbus.read).
// The shops and amounts are invented.

// positivePurchases is a card's month as an issuer that writes a purchase
// as a positive number exports it: three purchases and a refund.
var positivePurchases = csvOf(
	"2025-12-02,HARDWARE BARN,139.07",
	"2025-12-03,COFFEE CART,4.50",
	"2025-12-04,BOOK STALL,20.00",
	"2025-12-05,HARDWARE BARN REFUND,-139.07",
)

func TestACardsPositivePurchasesAreTurnedRound(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	d, _ := w.preview(me, card, "december.csv", positivePurchases)
	if !d.Mapping.Invert || d.Result.Records[0].Amount != money.MustParse("-139.07") || d.Result.Records[3].Amount != money.MustParse("139.07") {
		t.Fatalf("draft: invert %v, records %+v", d.Mapping.Invert, d.Result.Records)
	}

	if st := w.add(me, card, "december.csv", positivePurchases); st.Added != 4 {
		t.Fatalf("statement = %+v", st)
	}

	txs, err := w.ledger.Transactions(t.Context(), me, card, "2025-12")
	if err != nil || len(txs) != 4 {
		t.Fatalf("December: %d, %v", len(txs), err)
	}

	for _, tx := range txs {
		if tx.Description == "COFFEE CART" && tx.Amount != money.MustParse("-4.50") {
			t.Errorf("a purchase was kept as money in: %+v", tx)
		}
	}

	// The next file with the same headings is read by the columns
	// remembered, turned round with them.
	d, _ = w.preview(me, card, "january.csv", csvOf("2026-01-02,COFFEE CART,4.50"))
	if !d.Remembered || !d.Mapping.Invert || d.Result.Records[0].Amount != money.MustParse("-4.50") {
		t.Errorf("next file: remembered %v, invert %v, records %+v", d.Remembered, d.Mapping.Invert, d.Result.Records)
	}
}

func TestACardsSignsAreLeftAloneOtherwise(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")
	checking := w.account(me, "checking")

	// Purchases already money out.
	d, _ := w.preview(me, card, "negative.csv", csvOf("2025-12-02,COFFEE CART,-4.50", "2025-12-03,PAYMENT THANK YOU,200.00", "2025-12-04,BOOK STALL,-20.00"))
	if d.Mapping.Invert || d.Result.Records[0].Amount != money.MustParse("-4.50") {
		t.Errorf("negative purchases: invert %v, records %+v", d.Mapping.Invert, d.Result.Records)
	}

	// A checking account's mostly deposits are deposits.
	d, _ = w.preview(me, checking, "deposits.csv", positivePurchases)
	if d.Mapping.Invert || d.Result.Records[0].Amount != money.MustParse("139.07") {
		t.Errorf("checking: invert %v, records %+v", d.Mapping.Invert, d.Result.Records)
	}

	// A person who unticks the box is taken at their word.
	file := w.save(me, "untick.csv", positivePurchases)

	d, err := w.ledger.Prepare(t.Context(), me, card, file, nil)
	if err != nil || !d.Mapping.Invert {
		t.Fatalf("guess: invert %v, %v", d.Mapping.Invert, err)
	}

	m := d.Mapping
	m.Invert = false

	if d, err = w.ledger.Prepare(t.Context(), me, card, file, &ledgerbus.Options{Mapping: m}); err != nil || d.Mapping.Invert || d.Result.Records[0].Amount != money.MustParse("139.07") {
		t.Errorf("unticked: invert %v, records %+v, %v", d.Mapping.Invert, d.Result.Records, err)
	}
}
