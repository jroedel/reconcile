package ledgerbus_test

import (
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// A batch whose files are of one account, and change each other's
// previews as they go in (ledgerbus.ImportProposals). The rows are
// invented.

func withBalances(rows ...string) []byte {
	return []byte("Date,Description,Amount,Balance\n" + strings.Join(rows, "\n") + "\n")
}

// propose is a batch of the actor's files, each for the account.
func (w *world) propose(actor, account types.ID, files ...types.ID) []ledgerbus.Proposal {
	w.t.Helper()

	chosen := map[string]types.ID{}
	for _, f := range files {
		chosen[f.String()] = account
	}

	props, err := w.ledger.Propose(w.t.Context(), actor, files, chosen)
	if err != nil {
		w.t.Fatal(err)
	}

	return props
}

// The bank's CSV of the days its OFX brought in adds nothing, and is
// imported with no balances, lending the OFX's rows their whole words.
func TestACSVThatAddsNothingNeedsNobody(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	w.add(me, card, "december.ofx", cardOFX())

	props := w.propose(me, card, w.save(me, "december.csv", cardCSV()))
	if d := props[0].Draft; !props[0].Unattended() || d.Check.OK || !d.AddsNothing() || d.Statement.Completed != 4 {
		t.Fatalf("unattended %v, check %+v, adds nothing %v, completes %d", props[0].Unattended(), d.Check, d.AddsNothing(), d.Statement.Completed)
	}

	if n, err := w.ledger.ImportProposals(t.Context(), now, me, props); err != nil || n != 1 {
		t.Fatalf("imported %d: %v", n, err)
	}

	w.wholeAndIdentified(me, card)

	// Unchecked and with something new, it still waits for a person.
	props = w.propose(me, card, w.save(me, "january.csv", csvOf("2026-01-02,COFFEE CART 0902 SEATTLE,-3.50")))
	if props[0].Unattended() || props[0].Draft.AddsNothing() {
		t.Errorf("a new unchecked row: unattended %v", props[0].Unattended())
	}
}

// A file that needed nobody when it was read alone is read again once
// another has gone into its account, and waits if they overlap; a file
// that was not ready is ready once the other is in.
func TestABatchIsReadAgainAsItGoesIn(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	// Two statements that check by their balances, the same coffee worded
	// two ways; and the bank's CSV of the first, with no balances.
	first := w.save(me, "first.csv", withBalances(
		"2026-09-01,DEPOSIT,100.00,100.00",
		"2026-09-02,SQ *COFFEE CART,-3.50,96.50",
	))
	second := w.save(me, "second.csv", []byte("Post Date,Payee,Amount,Running Balance\n"+
		"2026-09-02,COFFEE CART 0902,-3.50,96.50\n"+
		"2026-09-03,BOOK STALL,-6.50,90.00\n"))
	plain := w.save(me, "plain.csv", []byte("Date,Description,Amount\n2026-09-01,DEPOSIT,100.00\n2026-09-02,SQ *COFFEE CART,-3.50\n"))

	props := w.propose(me, acct, plain, first, second)

	ready := []bool{false, true, true}
	for i, p := range props {
		if p.Unattended() != ready[i] {
			t.Fatalf("%s read alone: unattended %v, check %+v, doubts %d", p.File.Name, p.Unattended(), p.Draft.Check, len(p.Draft.Statement.Doubts))
		}
	}

	n, err := w.ledger.ImportProposals(t.Context(), now, me, props)
	if err != nil || n != 2 {
		t.Fatalf("imported %d: %v", n, err)
	}

	// The plain CSV went in after the first, adding nothing; the second
	// waits, its coffee set aside for a person to look at.
	if !props[0].Unattended() || props[2].Unattended() || len(props[2].Draft.Statement.Doubts) != 1 {
		t.Errorf("after: plain %v, second %v with %d doubts", props[0].Unattended(), props[2].Unattended(), len(props[2].Draft.Statement.Doubts))
	}

	if got := w.count(me, acct); got != 2 {
		t.Errorf("%d transactions, want 2", got)
	}
}
