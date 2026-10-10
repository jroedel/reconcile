package receiptbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// What a check's image says beside its number and amount -- its memo line
// and the day it was written -- is kept on the image and written on the
// transaction that paid it, whose date stays the day it cleared; carried
// across when the check clears later; and corrected on the image's page.
// The checks read and not cleared are the account's outstanding ones,
// with their total.
func TestAChecksMemoAndDayWritten(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	var shots []types.ID
	for _, name := range []string{"Screenshot_1.png", "Screenshot_2.png", "Screenshot_3.png", "Screenshot_4.png"} {
		shots = append(shots, w.upload(w.owner, name, photo+name, receiptbus.Accept))
	}

	added, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, shots, "", "")
	if err != nil || len(added) != 4 {
		t.Fatalf("the screenshots: %d, %v", len(added), err)
	}

	read := func(i int, number, amount, on, memo string) receiptbus.Receipt {
		t.Helper()

		rd := receiptbus.CheckReading{Number: number, Memo: memo}

		var err error
		if rd.Amount, err = money.Parse(amount); err != nil {
			t.Fatal(err)
		}

		if rd.On, err = types.ParseDate(on); err != nil {
			t.Fatal(err)
		}

		r, err := w.receipts.ReadCheck(w.ctx, now, w.owner, added[i].ID, rd)
		if err != nil {
			t.Fatalf("reading check %s: %v", number, err)
		}

		return r
	}

	// Check 1176 cleared on 2 July, and was written on 28 June.
	r := read(0, "1176", "120.00", "2026-06-28", "  Summer   work ")
	if r.Waiting() || r.Memo != "Summer work" || r.WrittenOn.String() != "2026-06-28" || r.SpentOn.String() != "2026-07-02" {
		t.Fatalf("check 1176: %+v", r)
	}

	if c, _ := w.ledger.Lookup(w.ctx, r.Links[0].TransactionID); c.CheckMemo != "Summer work" || c.WrittenOn.String() != "2026-06-28" || c.PostedOn.String() != "2026-07-02" {
		t.Errorf("check 1176's transaction: memo %q, written %s, posted %s", c.CheckMemo, c.WrittenOn, c.PostedOn)
	}

	// Three not cleared: one in July, one in August, and one numbered on
	// its page with no amount or day.
	read(1, "1195", "75.00", "2026-07-20", "Cleaning")
	read(2, "1199", "10.00", "2026-08-05", "")

	if _, err := w.receipts.NumberCheck(w.ctx, now, w.owner, added[3].ID, "1200"); err != nil {
		t.Fatal(err)
	}

	out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{})
	if err != nil || len(out.Checks) != 3 || out.Total.String() != "85.00" || out.Unpriced != 1 {
		t.Fatalf("outstanding: %+v, %v", out, err)
	}

	if out.Checks[0].Check != "1195" || out.Checks[1].Check != "1199" || out.Checks[2].Check != "1200" {
		t.Errorf("outstanding, in order: %s, %s, %s", out.Checks[0].Check, out.Checks[1].Check, out.Checks[2].Check)
	}

	july, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, mustDate(t, "2026-07-31"))
	if err != nil || len(july.Checks) != 2 || july.Total.String() != "75.00" || july.Unpriced != 1 {
		t.Errorf("outstanding at the end of July: %+v, %v", july, err)
	}

	if _, err := w.receipts.OutstandingChecks(w.ctx, w.pilgrim, acct, types.Date{}); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("the pilgrim: %v", err)
	}

	// Check 1195 clears in August: its memo and day go with it.
	const august = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-08-03,CHECK,-75.00,753.00,1195\n"

	f := w.upload(w.owner, "august.csv", august, nil)

	d, err := w.ledger.Prepare(w.ctx, w.owner, acct, f, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Import(w.ctx, now, w.owner, acct, f, ledgerbus.Options{Mapping: d.Mapping}); err != nil {
		t.Fatal(err)
	}

	got, err := w.receipts.Receipt(w.ctx, w.owner, added[1].ID)
	if err != nil || got.Receipt.Waiting() || len(got.Transactions) != 1 {
		t.Fatalf("check 1195 after its import: %+v, %v", got, err)
	}

	if c := got.Transactions[0]; c.CheckMemo != "Cleaning" || c.WrittenOn.String() != "2026-07-20" || c.PostedOn.String() != "2026-08-03" {
		t.Errorf("check 1195's transaction: memo %q, written %s, posted %s", c.CheckMemo, c.WrittenOn, c.PostedOn)
	}

	if out, _ := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{}); len(out.Checks) != 2 || out.Total.String() != "10.00" {
		t.Errorf("outstanding after August: %+v", out)
	}

	// Corrected on the image's page, the transaction follows.
	d1 := r.Details
	d1.Memo = "Summer work, July"

	if _, err := w.receipts.SetDetails(w.ctx, now, w.owner, r.ID, d1); err != nil {
		t.Fatal(err)
	}

	if c, _ := w.ledger.Lookup(w.ctx, r.Links[0].TransactionID); c.CheckMemo != "Summer work, July" || c.WrittenOn.String() != "2026-06-28" {
		t.Errorf("corrected: memo %q, written %s", c.CheckMemo, c.WrittenOn)
	}

	// A receipt that is no check keeps no memo.
	plain, err := w.receipts.Add(w.ctx, now, w.owner, types.AccountScope(acct), []types.ID{w.upload(w.owner, "grocery.jpg", photo+"grocery", receiptbus.Accept)}, false, receiptbus.Details{})
	if err != nil {
		t.Fatal(err)
	}

	if g, err := w.receipts.SetDetails(w.ctx, now, w.owner, plain[0].ID, receiptbus.Details{Memo: "x", WrittenOn: mustDate(t, "2026-07-01")}); err != nil || g.Memo != "" || !g.WrittenOn.Zero() {
		t.Errorf("a grocery receipt's memo: %+v, %v", g, err)
	}
}

func mustDate(t *testing.T, s string) types.Date {
	t.Helper()

	d, err := types.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}

	return d
}
