package ledgerbus_test

import (
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// lines is how many lines of a scope's history are action.
func (w *world) lines(scope types.Scope, action eventbus.Action) int {
	w.t.Helper()

	events, err := w.history.Recent(w.t.Context(), scope, 200)
	if err != nil {
		w.t.Fatal(err)
	}

	n := 0

	for _, e := range events {
		if e.Action == action {
			n++
		}
	}

	return n
}

// Through a key, a join marks the payee joined into and the spellings it
// brought, a split marks both payees and the spelling, and a payment's
// payee is marked; the same again through the key changes nothing, and a
// person saving on the web takes the marks off without a line of history.
func TestWhatAKeyChangesInPayeesIsMarked(t *testing.T) {
	w := newWorld(t)
	p := newPaid(w)
	ctx := t.Context()
	claude := eventbus.WithVia(ctx, "Claude")

	p.pay(w, "1176", "Hilltop Plumbing")
	p.pay(w, "1177", "HILLTOP PLUMBING LLC")
	list := p.list(w)
	shop, llc := named(list, "Hilltop Plumbing"), named(list, "HILLTOP PLUMBING LLC")

	if shop.Marked() || llc.Marked() {
		t.Errorf("payees a person wrote are marked: %+v, %+v", shop, llc)
	}

	joined, err := w.ledger.JoinPayees(claude, now, p.owner, shop.ID, []types.ID{llc.ID})
	if err != nil || joined.Via != "Claude" || !slices.Equal(joined.BroughtVia, []string{"HILLTOP PLUMBING LLC"}) {
		t.Fatalf("joined through the key: %+v, %v", joined, err)
	}

	if stored := named(p.list(w), "Hilltop Plumbing"); stored.Via != "Claude" || !slices.Equal(stored.BroughtVia, []string{"HILLTOP PLUMBING LLC"}) {
		t.Errorf("stored: %+v", stored)
	}

	split, err := w.ledger.SplitPayee(claude, now, p.owner, shop.ID, "HILLTOP PLUMBING LLC", types.ID{})
	if err != nil || split.ID != llc.ID || split.Via != "Claude" || !slices.Equal(split.BroughtVia, []string{"HILLTOP PLUMBING LLC"}) {
		t.Fatalf("split through the key: %+v, %v", split, err)
	}

	if stored, _, _ := w.ledger.Payee(ctx, p.owner, llc.ID); stored.Via != "Claude" || !slices.Equal(stored.BroughtVia, []string{"HILLTOP PLUMBING LLC"}) {
		t.Errorf("the payee split off, stored: %+v", stored)
	}

	if left := named(p.list(w), "Hilltop Plumbing"); left.Via != "Claude" || len(left.BroughtVia) != 0 {
		t.Errorf("the payee it left: %+v", left)
	}

	// A payment's payee, through the key: marked once.
	set, err := w.ledger.SetPayee(claude, now, p.owner, p.checks["1178"], "Hilltop Plumbing")
	if err != nil || set.PayeeVia != "Claude" || set.PayeeID != shop.ID || set.ByProgram() != "Claude" {
		t.Fatalf("set through the key: %+v, %v", set, err)
	}

	if again, err := w.ledger.SetPayee(claude, now, p.owner, p.checks["1178"], "Hilltop Plumbing"); err != nil || again.PayeeVia != "Claude" {
		t.Errorf("again through the key: %+v, %v", again, err)
	}

	months, err := w.ledger.Months(ctx, p.owner, p.checking)
	if err != nil || len(months) != 1 || months[0].ThroughKey != 1 {
		t.Errorf("the month's changes to check: %+v, %v", months, err)
	}

	// Saved on the web, unchanged: the marks go, and nothing is said.
	account := types.AccountScope(p.checking)
	before := w.lines(account, ledgerbus.TransactionPaidTo)

	saved, err := w.ledger.SetPayee(ctx, now, p.owner, p.checks["1178"], "Hilltop Plumbing")
	if err != nil || saved.PayeeVia != "" || saved.PayeeID != shop.ID {
		t.Errorf("saved on the web: %+v, %v", saved, err)
	}

	if after := w.lines(account, ledgerbus.TransactionPaidTo); after != before {
		t.Errorf("saving unchanged wrote %d lines of history", after-before)
	}

	for _, id := range []types.ID{shop.ID, llc.ID} {
		got, _, err := w.ledger.Payee(ctx, p.owner, id)
		if err != nil {
			t.Fatal(err)
		}

		saved, err := w.ledger.RenamePayee(ctx, now, p.owner, id, got.Name)
		if err != nil || saved.Marked() {
			t.Errorf("%s saved on the web: %+v, %v", got.Name, saved, err)
		}

		if stored, _, _ := w.ledger.Payee(ctx, p.owner, id); stored.Marked() {
			t.Errorf("%s stored after saving: %+v", got.Name, stored)
		}
	}

	if n := w.lines(p.org, ledgerbus.PayeeRenamed); n != 0 {
		t.Errorf("saving payees unchanged wrote %d lines of history", n)
	}

	// A payee made for a payment through the key is not marked itself:
	// the payment is.
	made, err := w.ledger.SetPayee(claude, now, p.owner, p.checks["1179"], "Summit Roofing")
	if err != nil || made.PayeeVia != "Claude" {
		t.Fatalf("a new payee through the key: %+v, %v", made, err)
	}

	if roof := named(p.list(w), "Summit Roofing"); roof.ID != made.PayeeID || roof.Marked() {
		t.Errorf("the payee made: %+v", roof)
	}

	// Taken away through the key, no payee carries no mark.
	if gone, err := w.ledger.SetPayee(claude, now, p.owner, p.checks["1179"], ""); err != nil || gone.PayeeVia != "" || !gone.PayeeID.Zero() {
		t.Errorf("taken away through the key: %+v, %v", gone, err)
	}
}
