package ledgerbus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// checks is an invented statement with two checks, one of whose
// descriptions says nothing but "CHECK".
var checks = []byte("Date,Description,Amount,Balance,Check Number\n" +
	"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
	"2026-07-02,CHECK,-120.00,880.00,0001176\n" +
	"2026-07-09,Check 1177,-30.00,850.00,1177\n")

// A check is found by its number, and whoever may attach receipts to it
// may say whom it was paid to: what rules and suggestions then read, so
// that a rule for the payee sorts a check whose description is "CHECK".
func TestAChecksPayee(t *testing.T) {
	w := newWorld(t)
	r := newRuled(w)
	ctx := t.Context()

	w.add(r.owner, r.account, "checks.csv", checks)

	found, err := w.ledger.WithCheck(ctx, r.account, "1176")
	if err != nil || len(found) != 1 || found[0].Description != "CHECK" || found[0].CheckNumber != "1176" {
		t.Fatalf("check 1176: %+v, %v", found, err)
	}

	check := found[0]

	if none, _ := w.ledger.WithCheck(ctx, r.account, "nonsense"); len(none) != 0 {
		t.Errorf("a number that is no number found %d", len(none))
	}

	stranger := w.user("stranger@example.org")
	viewer := w.user("viewer@example.org")
	w.grant(r.owner, types.AccountScope(r.account), "viewer@example.org", tenancybus.Viewer)

	if _, err := w.ledger.SetPayee(ctx, now, stranger, check.ID, "Mine"); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.ledger.SetPayee(ctx, now, viewer, check.ID, "Mine"); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}

	if _, err := w.ledger.SetPayee(ctx, now, r.owner, check.ID, strings.Repeat("x", ledgerbus.MaxPayee+1)); !errors.Is(err, ledgerbus.ErrPayee) {
		t.Errorf("too long: %v", err)
	}

	paid, err := w.ledger.SetPayee(ctx, now.Add(time.Hour), r.owner, check.ID, "  Hilltop   Plumbing ")
	if err != nil || paid.Payee != "Hilltop Plumbing" || paid.Words() != "Hilltop Plumbing CHECK" {
		t.Fatalf("set: %+v, %v", paid, err)
	}

	if line := w.line(r.account, ledgerbus.TransactionPaidTo); line["payee"] != "Hilltop Plumbing" || line["description"] != "CHECK" {
		t.Errorf("history: %v", line)
	}

	if _, err := w.rules.Save(ctx, now, r.owner, r.account, rulebus.Fields{Match: "hilltop plumbing", Direction: rulebus.Out, CategoryID: r.utilities}); err != nil {
		t.Fatal(err)
	}

	if n, err := w.ledger.SortUnsorted(ctx, now.Add(2*time.Hour), r.owner, r.account); err != nil || n != 1 {
		t.Fatalf("sorted %d: %v", n, err)
	}

	e, err := w.ledger.Transaction(ctx, r.owner, check.ID)
	if got := e.Transaction; err != nil || got.Splits[0].CategoryID != r.utilities || got.Payee != "Hilltop Plumbing" {
		t.Errorf("the check: %+v, %v", got, err)
	}
}
