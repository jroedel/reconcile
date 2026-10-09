package ledgerbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Entries by hand (docs/clearing.md, 1). Every entry here is invented.

// aDocument is the smallest file that reads as a PDF.
var aDocument = []byte("%PDF-1.4\n% an invented ticket\n")

func entry(t *testing.T, day, desc, amount string, file types.ID) ledgerbus.Entry {
	t.Helper()

	return ledgerbus.Entry{Date: date(t, day), Description: desc, Amount: money.MustParse(amount), File: file}
}

func TestEnteringATransactionByHand(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "card")

	tx, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-09-12", "  Plane   ticket, paid by the  province ", "-1200.00", w.save(me, "ticket.pdf", aDocument)))
	if err != nil {
		t.Fatal(err)
	}

	if tx.Description != "Plane ticket, paid by the province" || tx.Amount != money.MustParse("-1200.00") || tx.PostedOn.String() != "2026-09-12" {
		t.Errorf("transaction = %+v", tx)
	}

	sts, err := w.ledger.Statements(t.Context(), me, acct)
	if err != nil || len(sts) != 1 {
		t.Fatalf("statements: %d, %v", len(sts), err)
	}

	if st := sts[0]; st.Format != ledgerbus.Hand || st.Checked != ledgerbus.ByHand || st.Added != 1 || st.FileName != "ticket.pdf" {
		t.Errorf("statement = %+v", st)
	}

	// It is in the month, sorted like any other, and no statement covers
	// the month for it.
	txs, _ := w.ledger.Transactions(t.Context(), me, acct, "2026-09")
	if len(txs) != 1 || len(txs[0].Splits) != 1 {
		t.Errorf("the month: %+v", txs)
	}

	cover, err := w.ledger.Coverage(t.Context(), me, acct, types.DateOf(now))
	if err != nil || len(cover) != 0 {
		t.Errorf("coverage = %+v, %v", cover, err)
	}

	// Not a statement to reconcile.
	if _, err := w.ledger.Reconcile(t.Context(), now, me, sts[0].ID, types.Date{}, types.Date{}, ""); !errors.Is(err, ledgerbus.ErrHand) {
		t.Errorf("reconciling it: %v", err)
	}

	// The same again is refused; a second, told apart, is not.
	if _, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-09-12", "Plane ticket, paid by the province", "-1200.00", w.save(me, "again.pdf", aDocument))); !errors.Is(err, ledgerbus.ErrEntered) {
		t.Errorf("entered twice: %v", err)
	}

	if _, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-09-12", "Plane ticket, return", "-1200.00", w.save(me, "return.pdf", aDocument))); err != nil {
		t.Errorf("a second ticket: %v", err)
	}

	// Removed as a statement is.
	if _, err := w.ledger.RemoveStatement(t.Context(), now, me, sts[0].ID); err != nil {
		t.Errorf("removing it: %v", err)
	}
}

// An entry on a day and amount a statement already has is a person's word
// that it is another charge: the count rule does not set it aside.
func TestAnEntryIsNotSetAside(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.add(me, acct, "september.csv", csvOf("2026-09-12,CORNER HARDWARE,-40.00"))

	if _, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-09-12", "Hall rental deposit", "-40.00", w.save(me, "deposit.pdf", aDocument))); err != nil {
		t.Fatal(err)
	}

	txs, _ := w.ledger.Transactions(t.Context(), me, acct, "2026-09")
	if len(txs) != 2 {
		t.Errorf("the month holds %d transactions, want 2", len(txs))
	}
}

func TestWhatAnEntryNeeds(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")
	doc := w.save(me, "doc.pdf", aDocument)

	for field, e := range map[string]ledgerbus.Entry{
		"date":        {Description: "x", Amount: money.MustParse("1.00"), File: doc},
		"description": entry(t, "2026-09-12", "   ", "1.00", doc),
		"amount":      entry(t, "2026-09-12", "x", "0", doc),
		"file":        entry(t, "2026-09-12", "x", "1.00", types.NewID()),
	} {
		if _, err := w.ledger.Enter(t.Context(), now, me, acct, e); err != (ledgerbus.EntryInvalid{Field: field}) {
			t.Errorf("%s: %v", field, err)
		}
	}

	// A file that is not a photo or a PDF, and somebody else's document.
	if _, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-09-12", "x", "1.00", w.save(me, "notes.csv", []byte("a,b\n")))); err != (ledgerbus.EntryInvalid{Field: "file"}) {
		t.Errorf("a CSV as the document: %v", err)
	}

	other := w.user("other@example.org")
	if _, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-09-12", "x", "1.00", w.save(other, "theirs.pdf", aDocument))); err != (ledgerbus.EntryInvalid{Field: "file"}) {
		t.Errorf("somebody else's document: %v", err)
	}
}

func TestWhoMayEnter(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	viewer := w.user("viewer@example.org")
	w.grant(me, types.AccountScope(acct), "viewer@example.org", tenancybus.Viewer)

	stranger := w.user("stranger@example.org")

	if _, err := w.ledger.Enter(t.Context(), now, viewer, acct, entry(t, "2026-09-12", "x", "1.00", w.save(viewer, "a.pdf", aDocument))); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}

	if _, err := w.ledger.Enter(t.Context(), now, stranger, acct, entry(t, "2026-09-12", "x", "1.00", w.save(stranger, "b.pdf", aDocument))); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}

// Nothing is entered on a day a reconciled statement covers.
func TestNoEntryInAReconciledPeriod(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	st := w.imports(me, acct, "checking-july.csv")
	if _, err := w.ledger.Reconcile(t.Context(), now, me, st.ID, types.Date{}, types.Date{}, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Enter(t.Context(), now, me, acct, entry(t, "2026-07-15", "x", "-1.00", w.save(me, "c.pdf", aDocument))); !errors.Is(err, ledgerbus.ErrLocked) {
		t.Errorf("in a reconciled period: %v", err)
	}
}
