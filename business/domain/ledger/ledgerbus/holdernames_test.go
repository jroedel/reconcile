package ledgerbus_test

import (
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// Holders' names cut short by a narrow printed column, and files that
// name no holder (holders.go). The names, shops and amounts are invented.

func (w *world) holderNames(actor, account types.ID) []string {
	w.t.Helper()

	m, err := w.ledger.Holders(w.t.Context(), actor, account, "2026-09")
	if err != nil {
		w.t.Fatal(err)
	}

	var out []string
	for _, h := range m.Totals {
		out = append(out, h.Holder)
	}

	return out
}

func TestANameCutShortIsTheWholeName(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	if err := w.ledger.SetByHolder(ctx, now, me, card, true); err != nil {
		t.Fatal(err)
	}

	// A month printed with the name cut short, before any file had it
	// whole.
	cut := withHolders("2026-09-04,CITY GARAGE,-12.00,ANA MARIA SA…", "2026-09-05,COFFEE CART,-3.50,ANA MARIA SA…")
	w.add(me, card, "cut.csv", cut)

	// The next month, whole: the rows here take the whole name.
	st := w.add(me, card, "whole.csv", withHolders("2026-09-20,BOOK STALL,-20.00,ANA MARIA SANTOS"))
	if st.Renamed != 2 || st.Added != 1 {
		t.Fatalf("the whole name: renamed %d, added %d", st.Renamed, st.Added)
	}

	if got := w.holderNames(me, card); !slices.Equal(got, []string{"ANA MARIA SANTOS"}) {
		t.Errorf("holders %q", got)
	}

	// The cut printing again is the same charges, read under the whole
	// name.
	if got := counts(w.add(me, card, "cut-again.csv", append(cut, '\n'))); got != [3]int{0, 2, 0} {
		t.Errorf("the cut printing again: %v", got)
	}
}

func TestWhoseAFileIsThatNamesNobody(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	if err := w.ledger.SetByHolder(ctx, now, me, card, true); err != nil {
		t.Fatal(err)
	}

	w.add(me, card, "ana.csv", withHolders(
		"2026-09-02,HARDWARE BARN,-139.07,Ana",
		"2026-09-03,COFFEE CART,-3.50,Ana",
		"2026-09-04,BOOK STALL,-20.00,Ana",
		"2026-09-05,BAKERY,-4.00,Ana",
	))
	w.add(me, card, "ben.csv", withHolders("2026-09-03,GARAGE,-60.00,Ben", "2026-09-06,FLORIST,-9.00,Ben"))

	// The bank's download of Ana's card, worded its own way, with a
	// charge her printed month did not have.
	download := csvOf(
		"2026-09-02,THE HARDWARE BARN #12 SPRINGFIELD,-139.07",
		"2026-09-03,SQ *COFFEE CART 0902,-3.50",
		"2026-09-04,BOOK STALL #4 PORTLAND,-20.00",
		"2026-09-05,BAKERY,-4.00",
		"2026-09-30,TWILIO INC,-10.19",
	)

	d, file := w.preview(me, card, "download.csv", download)
	if !d.Inferred || d.Holder != "Ana" || d.Matched != 4 || d.Statement.Added != 1 || d.Statement.SetAside() != 3 {
		t.Fatalf("inferred %v as %q by %d; added %d, set aside %d", d.Inferred, d.Holder, d.Matched, d.Statement.Added, d.Statement.SetAside())
	}

	if _, err := w.ledger.Import(ctx, now, me, card, file, ledgerbus.Options{Mapping: d.Mapping, Holder: d.Holder}); err != nil {
		t.Fatal(err)
	}

	txs, _ := w.ledger.Transactions(ctx, me, card, "2026-09")
	for _, tx := range txs {
		if tx.Description == "TWILIO INC" && tx.Holder != "Ana" {
			t.Errorf("the new charge's holder: %q", tx.Holder)
		}
	}

	// Without anything to tell it by, or with as much of Ben's as of
	// Ana's, it is asked about.
	for name, data := range map[string][]byte{
		"new.csv":  csvOf("2026-10-02,COFFEE CART,-3.50", "2026-10-03,BAKERY,-4.00", "2026-10-04,GARAGE,-60.00"),
		"both.csv": csvOf("2026-09-03,COFFEE,-3.50", "2026-09-04,BOOKS,-20.00", "2026-09-05,BREAD,-4.00", "2026-09-03,CAR,-60.00", "2026-09-06,FLOWERS,-9.00"),
	} {
		if d, _ := w.preview(me, card, name, data); d.Inferred || d.Holder != "" {
			t.Errorf("%s: inferred %v as %q", name, d.Inferred, d.Holder)
		}
	}

	// A download that adds nothing goes in with a batch, as Ana's.
	again := csvOf("2026-09-02,THE HARDWARE BARN #12 SPRINGFIELD,-139.07", "2026-09-03,SQ *COFFEE CART 0902,-3.50", "2026-09-04,BOOK STALL #4 PORTLAND,-20.00")
	props := w.propose(me, card, w.save(me, "again.csv", again))

	if n, err := w.ledger.ImportProposals(ctx, now, me, props); err != nil || n != 1 {
		t.Errorf("the batch imported %d: %v (unattended %v, inferred %v)", n, err, props[0].Unattended(), props[0].Draft.Inferred)
	}
}
