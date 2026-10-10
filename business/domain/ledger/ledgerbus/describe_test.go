package ledgerbus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A transaction's own description is a bookkeeper's to write, shown in the
// bank's place while the bank's stays what rules and finding read; through
// a key it is marked until a person saves it on the web, and a person who
// saves it unchanged takes the mark off without a line of history.
func TestDescribingATransaction(t *testing.T) {
	w := newWorld(t)
	r := newRuled(w)
	ctx := t.Context()

	w.add(r.owner, r.account, "checks.csv", checks)

	found, err := w.ledger.WithCheck(ctx, r.account, "1177")
	if err != nil || len(found) != 1 {
		t.Fatalf("check 1177: %+v, %v", found, err)
	}

	check := found[0]

	stranger := w.user("stranger@example.org")
	contributor := w.user("contributor@example.org")
	w.grant(r.owner, types.AccountScope(r.account), "contributor@example.org", tenancybus.Contributor)

	if _, err := w.ledger.Describe(ctx, now, stranger, check.ID, "Mine"); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.ledger.Describe(ctx, now, contributor, check.ID, "Mine"); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a contributor: %v", err)
	}

	if _, err := w.ledger.Describe(ctx, now, r.owner, check.ID, strings.Repeat("x", ledgerbus.MaxDescription+1)); !errors.Is(err, ledgerbus.ErrDescription) {
		t.Errorf("too long: %v", err)
	}

	claude := eventbus.WithVia(ctx, "Claude")

	got, err := w.ledger.Describe(claude, now.Add(time.Hour), r.owner, check.ID, "  Summer   work ")
	if err != nil || got.OwnDescription != "Summer work" || got.OwnVia != "Claude" || got.Shown() != "Summer work" || got.Description != "Check 1177" {
		t.Fatalf("described: %+v, %v", got, err)
	}

	if got.ByProgram() != "Claude" || got.Words() != "Check 1177" {
		t.Errorf("by program %q, words %q", got.ByProgram(), got.Words())
	}

	if line := w.line(r.account, ledgerbus.TransactionDescribed); line["own"] != "Summer work" || line["description"] != "Check 1177" || line["before"] != "" {
		t.Errorf("history: %v", line)
	}

	months, err := w.ledger.Months(ctx, r.owner, r.account)
	if err != nil || len(months) != 1 || months[0].ThroughKey != 1 {
		t.Errorf("the month's changes to check: %+v, %v", months, err)
	}

	if f, err := w.ledger.Find(ctx, r.owner, ledgerbus.Query{Text: "summer WORK"}); err != nil || len(f.Found) != 1 || f.Found[0].Transaction.ID != check.ID {
		t.Errorf("found by its own description: %+v, %v", f, err)
	}

	// The same words again through the key change nothing; saved on the
	// web, the mark goes and the history has nothing new.
	again, err := w.ledger.Describe(claude, now.Add(2*time.Hour), r.owner, check.ID, "Summer work")
	if err != nil || again.OwnVia != "Claude" {
		t.Errorf("again through the key: %+v, %v", again, err)
	}

	saved, err := w.ledger.Describe(ctx, now.Add(3*time.Hour), r.owner, check.ID, "Summer work")
	if err != nil || saved.OwnVia != "" || saved.OwnDescription != "Summer work" {
		t.Errorf("saved on the web: %+v, %v", saved, err)
	}

	events, err := w.history.Recent(ctx, types.AccountScope(r.account), 50)
	if err != nil {
		t.Fatal(err)
	}

	lines := 0
	for _, e := range events {
		if e.Action == ledgerbus.TransactionDescribed {
			lines++
		}
	}

	if lines != 1 {
		t.Errorf("%d lines of history, want 1", lines)
	}

	// Taken away through a key, it carries no mark: there is nothing
	// left to check.
	gone, err := w.ledger.Describe(claude, now.Add(4*time.Hour), r.owner, check.ID, "")
	if err != nil || gone.OwnDescription != "" || gone.OwnVia != "" || gone.Shown() != "Check 1177" {
		t.Errorf("taken away: %+v, %v", gone, err)
	}
}

// A reconciled period's transactions keep the descriptions they had: the
// accountant may have its package already.
func TestADescriptionInAReconciledPeriod(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()
	july := w.statements(e.owner, e.account)[0]

	rv, err := w.ledger.Review(ctx, e.owner, july.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Reconcile(ctx, now, e.owner, july.ID, date(t, "2026-07-01"), date(t, "2026-07-31"), ""); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Describe(ctx, now, e.owner, rv.Transactions[0].ID, "Summer work"); !errors.Is(err, ledgerbus.ErrLocked) {
		t.Errorf("in a reconciled period: %v", err)
	}
}
