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

// Statements split by holder (docs/clearing.md, 3). The holders,
// shops and amounts are invented.

// addAs imports a file that names no holder as the given one's, as a
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

func TestHoldersAreKeptApart(t *testing.T) {
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
		t.Errorf("the holders offered: %q", d.Holders)
	}

	// Ana's again, later and wider: only what is new.
	if st := w.addAs(me, card, "ana-wider.csv", csvOf("2026-09-04,CITY GARAGE,-12.00", "2026-09-05,COFFEE CART,-3.50", "2026-09-10,BOOK SHOP,-20.00"), "Ana"); counts(st) != [3]int{1, 2, 0} {
		t.Errorf("Ana's wider month: %v", counts(st))
	}

	// A file of the whole card, worded otherwise and naming nobody, is
	// compared with every holder's: both garage charges are here.
	if st := w.add(me, card, "card.csv", csvOf("2026-09-04,CITY GARAGE #12,-12.00", "2026-09-04,CITY GARAGE #12,-12.00")); counts(st) != [3]int{0, 2, 2} {
		t.Errorf("the whole card: %v", counts(st))
	}

	// The month by holder, and who is missing next month.
	m, err := w.ledger.Holders(ctx, me, card, "2026-09")
	if err != nil {
		t.Fatal(err)
	}

	if m.Expected() != 2 || m.Present() != 2 || len(m.Totals) != 2 || m.Totals[0].Sum != money.MustParse("-35.50") || m.Totals[1].Count != 1 {
		t.Errorf("September: %+v", m)
	}

	if m, _ := w.ledger.Holders(ctx, me, card, "2026-10"); m.Present() != 0 || m.MissingNames() != "Ana, Ben" {
		t.Errorf("October: %+v", m)
	}

	// Off, every row's identity is worked out again without holders, and
	// Ben's garage is still his own to a file of his: a holder's row is
	// matched only with that holder's, or with one that names nobody.
	if err := w.ledger.SetByHolder(ctx, now, me, card, false); err != nil {
		t.Fatal(err)
	}

	if st := w.add(me, card, "ben-later.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ben", "2026-09-12,HARDWARE BARN,-8.00,Ben")); counts(st) != [3]int{1, 1, 0} {
		t.Errorf("Ben's later month, the option off: %v", counts(st))
	}

	if m, _ := w.ledger.Holders(ctx, me, card, "2026-09"); len(m.Totals) != 0 {
		t.Errorf("the month by holder with the option off: %+v", m)
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

// Without the option, a holder column is read and kept, and is no part of
// a row's identity -- but two people's charges are never quietly one
// (issue #81). Ben's garage, worded and priced as Ana's on the same day,
// shares her row's hash; matching it alone lost his. It is a doubt that
// names both, imported because his file is one person's, and the preview
// says the account looks like one file per holder.
func TestWithoutTheOptionHoldersAreNotLost(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	w.add(me, card, "ana.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ana", "2026-09-05,LUNCH PLACE,-12.34,Ana"))

	// Ben's month: the same garage charge, and another charge of Ana's
	// day and amount worded otherwise.
	ben := withHolders("2026-09-04,CITY GARAGE,-12.00,Ben", "2026-09-05,BOOK STALL,-12.34,Ben", "2026-09-06,BAKERY,-4.00,Ben")

	d, _ := w.preview(me, card, "ben.csv", ben)
	if !d.LooksByHolder || d.FileHolder != "Ben" || d.OtherNames() != "Ana" || !d.CanSplit {
		t.Errorf("the preview of Ben's file: looks %v, holder %q, others %q, may split %v", d.LooksByHolder, d.FileHolder, d.OtherNames(), d.CanSplit)
	}

	if n := len(d.Statement.Doubts); n != 2 || !d.Statement.Doubts[0].OtherHolder || !d.Statement.Doubts[0].Imported || d.Statement.Doubts[1].Twin.Holder != "Ana" {
		t.Errorf("the doubts: %+v", d.Statement.Doubts)
	}

	if st := w.add(me, card, "ben.csv", ben); counts(st) != [3]int{3, 0, 0} {
		t.Errorf("Ben's file: %v", counts(st))
	}

	txs, _ := w.ledger.Transactions(t.Context(), me, card, "2026-09")
	if len(txs) != 5 {
		t.Errorf("the month has %d charges, want 5: %+v", len(txs), txs)
	}

	// Ben's again, wider: his own charges are his, and only the new one is
	// added; nothing is asked about.
	d, _ = w.preview(me, card, "ben-wider.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ben", "2026-09-05,BOOK STALL,-12.34,Ben", "2026-09-06,BAKERY,-4.00,Ben", "2026-09-07,FLORIST,-9.00,Ben"))
	if counts(d.Statement) != [3]int{1, 3, 0} || len(d.Statement.Doubts) != 0 {
		t.Errorf("Ben's wider file: %v, doubts %+v", counts(d.Statement), d.Statement.Doubts)
	}

	// A person who knows better leaves one out, and it is set aside.
	cara := withHolders("2026-09-04,CITY GARAGE,-12.00,Cara")

	file := w.save(me, "cara.csv", cara)

	pre, err := w.ledger.Prepare(t.Context(), me, card, file, nil)
	if err != nil {
		t.Fatal(err)
	}

	st, err := w.ledger.Import(t.Context(), now, me, card, file, ledgerbus.Options{Mapping: pre.Mapping, Leave: []int{0}})
	if err != nil || counts(st) != [3]int{0, 1, 1} {
		t.Errorf("Cara's file, left out: %v, %v", counts(st), err)
	}

	// A file of the whole card names several holders, and is not taken
	// for one person's; nor is a file on an account with nobody else.
	if d, _ := w.preview(me, card, "card.csv", withHolders("2026-09-08,X,-1.00,Ana", "2026-09-08,Y,-1.00,Ben")); d.LooksByHolder {
		t.Error("a file of two holders looks like one holder's")
	}

	other := w.account(me, "checking")
	if d, _ := w.preview(me, other, "first.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ana")); d.LooksByHolder || d.ByHolder || d.Unnamed != 0 {
		t.Errorf("the first holder's file of an account: %+v", d)
	}

	if d, _ := w.preview(me, card, "plain.csv", csvOf("2026-09-20,X,-1.00")); d.ByHolder || d.Unnamed != 0 || d.LooksByHolder {
		t.Errorf("a plain file's preview asks about holders: %+v", d)
	}
}

// The mark an earlier import left when one holder's posted charge took
// another's pending one: a charge named for one holder whose memo names
// another. A memo a person wrote names nobody, and is no mark.
func TestChargesWhoseHolderAndMemoDisagree(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	w.add(me, card, "ana.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ana"))
	w.add(me, card, "ben.csv", withHolders("2026-09-06,SHOP X,-25.00,Ben", "2026-09-07,BAKERY,-4.00,Ben"))

	shop, _ := w.find(me, card, "2026-09", "SHOP X")
	bakery, _ := w.find(me, card, "2026-09", "BAKERY")

	for _, p := range []struct {
		tx   ledgerbus.Transaction
		memo string
	}{{shop, "ana"}, {bakery, "for the hall"}} {
		if _, err := w.ledger.SetSplits(ctx, now, me, p.tx.ID, []ledgerbus.Part{{Amount: p.tx.Amount, Memo: p.memo}}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := w.ledger.Disagreeing(ctx, me, card)
	if err != nil || len(got) != 1 || got[0].Transaction.ID != shop.ID || got[0].Named != "Ana" {
		t.Errorf("the charges that disagree: %+v, %v", got, err)
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.ledger.Disagreeing(ctx, stranger, card); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}

// A bulk import, and so Claude's import from the inbox, never imports a
// file that looks like one holder's past the question; once the account
// keeps its holders apart, the same file is ready.
func TestABulkImportWaitsForOneHoldersFile(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	balanced := func(rows ...string) []byte {
		return []byte("Date,Description,Amount,Balance,Card Member\n" + strings.Join(rows, "\n") + "\n")
	}

	w.add(me, card, "ana.csv", balanced("2026-09-04,CITY GARAGE,-12.00,988.00,Ana"))

	ben := w.save(me, "ben.csv", balanced("2026-09-04,CITY GARAGE,-12.00,500.00,Ben", "2026-09-06,BAKERY,-4.00,496.00,Ben"))

	props, err := w.ledger.Propose(ctx, me, []types.ID{ben}, map[string]types.ID{ben.String(): card})
	if err != nil || len(props) != 1 {
		t.Fatalf("Propose: %+v, %v", props, err)
	}

	if p := props[0]; p.Unattended() || !p.Draft.LooksByHolder || !p.Draft.Check.OK {
		t.Errorf("Ben's file, the option off: unattended %v, looks %v, checked %v", p.Unattended(), p.Draft.LooksByHolder, p.Draft.Check.OK)
	}

	if err := w.ledger.SetByHolder(ctx, now, me, card, true); err != nil {
		t.Fatal(err)
	}

	props, err = w.ledger.Propose(ctx, me, []types.ID{ben}, map[string]types.ID{ben.String(): card})
	if err != nil || len(props) != 1 || !props[0].Unattended() {
		t.Errorf("Ben's file, the option on: %+v, %v", props, err)
	}
}

// A file of the whole card that names nobody is compared with every
// holder's, the option off as on: a charge worded otherwise is set aside
// as before, with no holder to tell it apart.
func TestAWholeCardFileWithoutTheOption(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	w.add(me, card, "ana.csv", withHolders("2026-09-04,CITY GARAGE,-12.00,Ana"))

	if st := w.add(me, card, "card.csv", csvOf("2026-09-04,CITY GARAGE #12,-12.00")); counts(st) != [3]int{0, 1, 1} {
		t.Errorf("the whole card: %v", counts(st))
	}

	if st := w.add(me, card, "card-same.csv", csvOf("2026-09-04,CITY GARAGE,-12.00", "2026-09-05,BAKERY,-4.00")); counts(st) != [3]int{1, 1, 0} {
		t.Errorf("the whole card, worded as Ana's: %v", counts(st))
	}
}

// Charges lost before the fix come back by turning the option on and
// importing the holder's file again, after removing the statement it made:
// with holders kept apart, nothing of another holder's is taken for it.
func TestTurningTheOptionOnBringsBackWhatWasLost(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	w.add(me, card, "ana.csv", withHolders("2026-09-05,LUNCH PLACE,-12.34,Ana"))

	// As it was before: Ben's charge taken for Ana's, which here is a
	// person leaving it out.
	ben := withHolders("2026-09-05,BOOK STALL,-12.34,Ben", "2026-09-06,BAKERY,-4.00,Ben")
	file := w.save(me, "ben.csv", ben)

	pre, err := w.ledger.Prepare(ctx, me, card, file, nil)
	if err != nil {
		t.Fatal(err)
	}

	st, err := w.ledger.Import(ctx, now, me, card, file, ledgerbus.Options{Mapping: pre.Mapping, Leave: []int{0}})
	if err != nil || counts(st) != [3]int{1, 1, 1} {
		t.Fatalf("Ben's file, the charge lost: %v, %v", counts(st), err)
	}

	if err := w.ledger.SetByHolder(ctx, now, me, card, true); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.RemoveStatement(ctx, now, me, st.ID); err != nil {
		t.Fatal(err)
	}

	if st := w.add(me, card, "ben.csv", ben); counts(st) != [3]int{2, 0, 0} {
		t.Errorf("Ben's file again, the option on: %v", counts(st))
	}

	if txs, _ := w.ledger.Transactions(ctx, me, card, "2026-09"); len(txs) != 3 {
		t.Errorf("the month has %d charges, want 3", len(txs))
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

	if _, err := w.ledger.Holders(ctx, stranger, card, "2026-09"); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger's month: %v", err)
	}

	if on, err := w.ledger.ByHolder(ctx, me, card); err != nil || on {
		t.Errorf("the option after both: %v, %v", on, err)
	}
}
