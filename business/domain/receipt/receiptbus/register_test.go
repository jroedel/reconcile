package receiptbus_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// summerChecks is a checking account's summer, invented; the clock is 15
// August:
//
//	2001  written 20 June                 still out
//	2002  written 2 July                  cleared 10 July
//	2003  written 20 July                 cleared 3 August
//	2004  written 21 July                 voided 1 August, 01:30 UTC
//	2005  written 22 July                 voided 25 July
//	2006  written 2 August                still out
//	2007  no day read, added 15 August    still out
//	2008  written 23 July                 removed
func summerChecks(t *testing.T) (*world, types.ID) {
	t.Helper()

	w := newWorld(t)
	acct := w.checking()

	var shots []types.ID
	for _, name := range []string{"Screenshot_1.png", "Screenshot_2.png", "Screenshot_3.png", "Screenshot_4.png", "Screenshot_5.png", "Screenshot_6.png", "Screenshot_7.png", "Screenshot_8.png"} {
		shots = append(shots, w.upload(w.owner, name, photo+name, receiptbus.Accept))
	}

	added, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, shots, "", "")
	if err != nil || len(added) != len(shots) {
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

	read(0, "2001", "10.00", "2026-06-20")
	read(1, "2002", "20.00", "2026-07-02")
	read(2, "2003", "30.00", "2026-07-20")
	voidLate := read(3, "2004", "40.00", "2026-07-21")
	voidEarly := read(4, "2005", "50.00", "2026-07-22")
	read(5, "2006", "60.00", "2026-08-02")
	removed := read(7, "2008", "80.00", "2026-07-23")

	if _, err := w.receipts.NumberCheck(w.ctx, now, w.owner, added[6].ID, "2007"); err != nil {
		t.Fatal(err)
	}

	if _, err := w.receipts.SetCheckVoided(w.ctx, time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC), w.owner, voidEarly.ID, true); err != nil {
		t.Fatal(err)
	}

	// Voided at 01:30 on 1 August in UTC, which is still 31 July in New
	// York: where the reader is decides which day it fell on.
	if _, err := w.receipts.SetCheckVoided(w.ctx, time.Date(2026, 8, 1, 1, 30, 0, 0, time.UTC), w.owner, voidLate.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := w.receipts.SetRemoved(w.ctx, now, w.owner, removed.ID, true); err != nil {
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

	return w, acct
}

// line is a CheckLine as a test compares it.
type line struct{ number, state, cleared, voided string }

func lines(cs []receiptbus.CheckLine) []line {
	var out []line
	for _, c := range cs {
		out = append(out, line{c.Check, string(c.State), c.ClearedOn.String(), c.VoidedOn.String()})
	}

	return out
}

// A statement from July asks which checks were out when July ended, and
// the answer must not change because some of them cleared or were voided
// since.
func TestChecksOutstandingOnADay(t *testing.T) {
	w, acct := summerChecks(t)

	got := func(day string, loc *time.Location) ([]line, string) {
		t.Helper()

		out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, mustDate(t, day), loc)
		if err != nil {
			t.Fatal(err)
		}

		return lines(out.Checks), out.Total.String()
	}

	want := []line{{"2001", "outstanding", "", ""}, {"2003", "outstanding", "2026-08-03", ""}, {"2004", "outstanding", "", "2026-08-01"}}
	if ls, total := got("2026-07-31", time.UTC); !slices.Equal(ls, want) || total != "80.00" {
		t.Errorf("at the end of July: %v, %s; want %v, 80.00", ls, total, want)
	}

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	want = []line{{"2001", "outstanding", "", ""}, {"2003", "outstanding", "2026-08-03", ""}}
	if ls, _ := got("2026-07-31", newYork); !slices.Equal(ls, want) {
		t.Errorf("at the end of July in New York: %v; want %v", ls, want)
	}

	// Now, with no day, is what waits now: 2001, 2006 and 2007.
	out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{}, time.UTC)
	if err != nil || len(out.Checks) != 3 || out.Unpriced != 1 || out.Total.String() != "70.00" {
		t.Errorf("now: %+v, %v", out, err)
	}
}

// The accountant's list for a period: what was written in it, whatever
// became of it, and what was written before it and still out at its end.
func TestTheCheckRegister(t *testing.T) {
	w, acct := summerChecks(t)

	register := func(from, to string) []line {
		t.Helper()

		cs, err := w.receipts.CheckRegister(w.ctx, w.owner, acct, mustDate(t, from), mustDate(t, to), time.UTC)
		if err != nil {
			t.Fatal(err)
		}

		return lines(cs)
	}

	// July: 2001 from June, still out; the four written in July as each was
	// on the 31st. Not 2006, written in August; not 2007, added in August
	// with no day read; not 2008, removed.
	want := []line{
		{"2001", "outstanding", "", ""},
		{"2002", "attached", "2026-07-10", ""},
		{"2003", "outstanding", "2026-08-03", ""},
		{"2004", "outstanding", "", "2026-08-01"},
		{"2005", "void", "", "2026-07-25"},
	}
	if got := register("2026-07-01", "2026-07-31"); !slices.Equal(got, want) {
		t.Errorf("July:\n got %v\nwant %v", got, want)
	}

	// August: 2001, still out, and the two written in August. Not 2003,
	// written before August and cleared in it, which is on the month's
	// transactions; nor 2004 and 2005, written before and void by its end.
	want = []line{
		{"2001", "outstanding", "", ""},
		{"2006", "outstanding", "", ""},
		{"2007", "outstanding", "", ""},
	}
	if got := register("2026-08-01", "2026-08-31"); !slices.Equal(got, want) {
		t.Errorf("August:\n got %v\nwant %v", got, want)
	}

	if _, err := w.receipts.CheckRegister(w.ctx, w.stranger, acct, mustDate(t, "2026-07-01"), mustDate(t, "2026-07-31"), time.UTC); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}
