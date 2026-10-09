package ledgerbus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Statements split by cardholder (docs/clearing.md, 3). The cardholders,
// shops and amounts are invented.

// addAs imports a file that names no cardholder as the given one's, as a
// person answers the preview's question.
func (w *world) addAs(actor, account types.ID, name string, data []byte, holder string) ledgerbus.Statement {
	w.t.Helper()

	file := w.save(actor, name, data)

	d, err := w.ledger.Prepare(w.t.Context(), actor, account, file, nil)
	if err != nil {
		w.t.Fatalf("Prepare %s: %v", name, err)
	}

	w.tick++

	st, err := w.ledger.Import(w.t.Context(), now.Add(time.Duration(w.tick)*time.Minute), actor, account, file,
		ledgerbus.Options{Mapping: d.Mapping, Holder: holder})
	if err != nil {
		w.t.Fatalf("Import %s: %v", name, err)
	}

	return st
}

func withHolders(rows ...string) []byte {
	return []byte("Date,Description,Amount,Card Member\n" + strings.Join(rows, "\n") + "\n")
}

func TestCardholdersAreKeptApart(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	if err := w.ledger.SetByHolder(ctx, now, me, card, true); err != nil {
		t.Fatal(err)
	}

	// Ana's month, a printout that does not say whose it is: the preview
	// asks.
	ana := csvOf("2026-09-04,CITY GARAGE,-12.00", "2026-09-05,COFFEE CART,-3.50")

	d, _ := w.preview(me, card, "ana.csv", ana)
	if !d.ByHolder || d.Unnamed != 2 || len(d.Holders) != 0 {
		t.Errorf("the first preview: by holder %v, %d unnamed, holders %q", d.ByHolder, d.Unnamed, d.Holders)
	}

	if st := w.addAs(me, card, "ana.csv", ana, "Ana"); counts(st) != [3]int{2, 0, 0} {
		t.Errorf("Ana's month: %v", counts(st))
	}

	// Ben parked in the same garage on the same day for the same price:
	// another charge. His file says whose it is in a column.
	if st := w.add(me, card, "ben.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ben")); counts(st) != [3]int{1, 0, 0} {
		t.Errorf("Ben's month: %v", counts(st))
	}

	if d, _ := w.preview(me, card, "ana-later.csv", ana); len(d.Holders) != 2 || d.Holders[0] != "Ana" {
		t.Errorf("the cardholders offered: %q", d.Holders)
	}

	// Ana's again, later and wider: only what is new.
	if st := w.addAs(me, card, "ana-wider.csv", csvOf("2026-09-04,CITY GARAGE,-12.00", "2026-09-05,COFFEE CART,-3.50", "2026-09-10,BOOK SHOP,-20.00"), "Ana"); counts(st) != [3]int{1, 2, 0} {
		t.Errorf("Ana's wider month: %v", counts(st))
	}

	// A file of the whole card, worded otherwise and naming nobody, is
	// compared with every cardholder's: both garage charges are here.
	if st := w.add(me, card, "card.csv", csvOf("2026-09-04,CITY GARAGE #12,-12.00", "2026-09-04,CITY GARAGE #12,-12.00")); counts(st) != [3]int{0, 2, 2} {
		t.Errorf("the whole card: %v", counts(st))
	}

	// The month by cardholder, and who is missing next month.
	m, err := w.ledger.Cardholders(ctx, me, card, "2026-09")
	if err != nil {
		t.Fatal(err)
	}

	if m.Expected() != 2 || m.Present() != 2 || len(m.Totals) != 2 || m.Totals[0].Sum != money.MustParse("-35.50") || m.Totals[1].Count != 1 {
		t.Errorf("September: %+v", m)
	}

	if m, _ := w.ledger.Cardholders(ctx, me, card, "2026-10"); m.Present() != 0 || m.MissingNames() != "Ana, Ben" {
		t.Errorf("October: %+v", m)
	}

	// Off, every row's identity is worked out again without cardholders:
	// Ben's garage is then Ana's to a new file, as it would have been.
	if err := w.ledger.SetByHolder(ctx, now, me, card, false); err != nil {
		t.Fatal(err)
	}

	if st := w.add(me, card, "ben-later.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ben", "2026-09-12,HARDWARE BARN,-8.00,Ben")); counts(st) != [3]int{1, 1, 0} {
		t.Errorf("Ben's later month, the option off: %v", counts(st))
	}

	if m, _ := w.ledger.Cardholders(ctx, me, card, "2026-09"); len(m.Totals) != 0 {
		t.Errorf("the month by cardholder with the option off: %+v", m)
	}

	// And on again, the same: what is stored is found.
	if err := w.ledger.SetByHolder(ctx, now, me, card, true); err != nil {
		t.Fatal(err)
	}

	if st := w.add(me, card, "ben-again.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ben", "2026-09-12,HARDWARE BARN,-8.00,Ben", "2026-09-13,BAKERY,-4.00,Ben")); counts(st) != [3]int{1, 2, 0} {
		t.Errorf("Ben's month, on again: %v", counts(st))
	}

	events, err := w.history.Recent(ctx, types.AccountScope(card), 50)
	if err != nil {
		t.Fatal(err)
	}

	split := 0
	for _, e := range events {
		if e.Action == ledgerbus.AccountSplit {
			split++
		}
	}

	if split != 3 {
		t.Errorf("%d history lines for the option, want 3", split)
	}
}

// Without the option, a cardholder column is read and kept, and is part of
// nothing: two people's identical charges in two files are one, as on any
// account that is not split.
func TestWithoutTheOptionNothingChanges(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	w.add(me, card, "ana.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ana"))

	if st := w.add(me, card, "ben.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ben")); counts(st) != [3]int{0, 1, 0} {
		t.Errorf("Ben's file: %v", counts(st))
	}

	txs, _ := w.ledger.Transactions(t.Context(), me, card, "2026-09")
	if len(txs) != 1 || txs[0].Holder != "Ana" {
		t.Errorf("the month: %+v", txs)
	}

	if d, _ := w.preview(me, card, "plain.csv", csvOf("2026-09-20,X,-1.00")); d.ByHolder || d.Unnamed != 0 {
		t.Errorf("a plain account's preview asks about cardholders: %+v", d)
	}
}

func TestWhoMaySplitAnAccount(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	bookkeeper := w.user("bookkeeper@example.org")
	w.grant(me, types.AccountScope(card), "bookkeeper@example.org", tenancybus.Bookkeeper)

	stranger := w.user("stranger@example.org")

	if err := w.ledger.SetByHolder(ctx, now, bookkeeper, card, true); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a bookkeeper: %v", err)
	}

	if err := w.ledger.SetByHolder(ctx, now, stranger, card, true); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.ledger.Cardholders(ctx, stranger, card, "2026-09"); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger's month: %v", err)
	}

	if on, err := w.ledger.ByHolder(ctx, me, card); err != nil || on {
		t.Errorf("the option after both: %v, %v", on, err)
	}
}
