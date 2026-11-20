package receiptbus_test

import (
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A statement from July asks which checks were out when July ended, and
// the answer must not change because some of them cleared or were voided
// since. Everything here is invented; the clock is 15 August.
func TestChecksOutstandingOnADay(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	names := []string{"Screenshot_1.png", "Screenshot_2.png", "Screenshot_3.png", "Screenshot_4.png", "Screenshot_5.png", "Screenshot_6.png", "Screenshot_7.png"}

	var shots []types.ID
	for _, name := range names {
		shots = append(shots, w.upload(w.owner, name, photo+name, receiptbus.Accept))
	}

	added, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, shots, "", "")
	if err != nil || len(added) != len(names) {
		t.Fatalf("the screenshots: %d, %v", len(added), err)
	}

	read := func(i int, number, amount, on string) receiptbus.Receipt {
		t.Helper()

		rd := receiptbus.CheckReading{Number: number, Payee: "Hilltop Plumbing", On: mustDate(t, on)}
		if rd.Amount, err = money.Parse(amount); err != nil {
			t.Fatal(err)
		}

		r, err := w.receipts.ReadCheck(w.ctx, now, w.owner, added[i].ID, rd)
		if err != nil {
			t.Fatalf("reading check %s: %v", number, err)
		}

		return r
	}

	read(0, "2001", "10.00", "2026-06-20")              // written in June, still out
	read(1, "2002", "20.00", "2026-07-02")              // cleared in July
	read(2, "2003", "30.00", "2026-07-20")              // cleared in August
	voidLate := read(3, "2004", "40.00", "2026-07-21")  // voided in August
	voidEarly := read(4, "2005", "50.00", "2026-07-22") // voided in July
	read(5, "2006", "60.00", "2026-08-02")              // written in August

	if _, err := w.receipts.NumberCheck(w.ctx, now, w.owner, added[6].ID, "2007"); err != nil { // no day read, added in August
		t.Fatal(err)
	}

	july25 := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	if _, err := w.receipts.SetCheckVoided(w.ctx, july25, w.owner, voidEarly.ID, true); err != nil {
		t.Fatal(err)
	}

	// Voided at 01:30 on 1 August in UTC, which is still 31 July in New
	// York: where the reader is decides which day it fell on.
	aug1 := time.Date(2026, 8, 1, 1, 30, 0, 0, time.UTC)
	if _, err := w.receipts.SetCheckVoided(w.ctx, aug1, w.owner, voidLate.ID, true); err != nil {
		t.Fatal(err)
	}

	const statement = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-07-10,CHECK,-20.00,980.00,2002\n" +
		"2026-08-03,CHECK,-30.00,950.00,2003\n"

	f := w.upload(w.owner, "checks.csv", statement, nil)

	d, err := w.ledger.Prepare(w.ctx, w.owner, acct, f, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Import(w.ctx, now, w.owner, acct, f, ledgerbus.Options{Mapping: d.Mapping}); err != nil {
		t.Fatal(err)
	}

	type line struct{ number, cleared, voided string }

	got := func(day string, loc *time.Location) ([]line, string) {
		t.Helper()

		out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, mustDate(t, day), loc)
		if err != nil {
			t.Fatal(err)
		}

		var ls []line
		for _, c := range out.Checks {
			if c.State != receiptbus.NumberOutstanding {
				t.Errorf("check %s is %s", c.Check, c.State)
			}

			ls = append(ls, line{c.Check, c.ClearedOn.String(), c.VoidedOn.String()})
		}

		return ls, out.Total.String()
	}

	want := []line{{"2001", "", ""}, {"2003", "2026-08-03", ""}, {"2004", "", "2026-08-01"}}
	if ls, total := got("2026-07-31", time.UTC); !slices.Equal(ls, want) || total != "80.00" {
		t.Errorf("at the end of July: %v, %s; want %v, 80.00", ls, total, want)
	}

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	want = []line{{"2001", "", ""}, {"2003", "2026-08-03", ""}}
	if ls, _ := got("2026-07-31", newYork); !slices.Equal(ls, want) {
		t.Errorf("at the end of July in New York: %v; want %v", ls, want)
	}

	// Now, with no day, is what waits now: 2001, 2006 and 2007.
	out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{}, time.UTC)
	if err != nil || len(out.Checks) != 3 || out.Unpriced != 1 || out.Total.String() != "70.00" {
		t.Errorf("now: %+v, %v", out, err)
	}
}
