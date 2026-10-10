package ledgerbus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Explaining an amount (docs/clearing.md, 2). Every account, charge and
// amount here is invented: a house whose card is paid by somebody else,
// who then takes one amount out of the house's checking for it.

type clearingWorld struct {
	*world
	owner, org, checking, card types.ID
	card1                      ledgerbus.Statement

	// The card's charges, and the two withdrawals that pay for them.
	grocery, fuel, hardware ledgerbus.Transaction
	august, september       ledgerbus.Transaction
}

func newClearing(t *testing.T) clearingWorld {
	t.Helper()

	w := clearingWorld{world: newWorld(t)}
	ctx := t.Context()

	w.owner = w.user("treasurer@example.org")

	org, err := w.ten.CreateOrg(ctx, now, w.owner, "Invented House")
	if err != nil {
		t.Fatal(err)
	}

	w.org = org.ID

	for name, into := range map[string]*types.ID{"House checking": &w.checking, "House card": &w.card} {
		a, err := w.ten.CreateAccount(ctx, now, w.owner, org.ID, tenancybus.AccountFields{Name: name, Kind: "checking"})
		if err != nil {
			t.Fatal(err)
		}

		*into = a.ID
	}

	w.card1 = w.add(w.owner, w.card, "card-august.csv", csvOf("2026-08-03,CORNER GROCERY,-40.00", "2026-08-20,FUEL STOP,-60.00"))
	w.add(w.owner, w.card, "card-september.csv", csvOf("2026-09-02,HARDWARE BARN,-25.00"))
	w.add(w.owner, w.checking, "checking.csv", csvOf("2026-09-15,PROVINCE CARDS 0825,-100.00", "2026-10-15,PROVINCE CARDS 0925,-30.00"))

	find := func(account types.ID, month, desc string) ledgerbus.Transaction {
		txs, err := w.ledger.Transactions(ctx, w.owner, account, month)
		if err != nil {
			t.Fatal(err)
		}

		for _, tx := range txs {
			if tx.Description == desc {
				return tx
			}
		}

		t.Fatalf("no %s in %s", desc, month)

		return ledgerbus.Transaction{}
	}

	w.grocery = find(w.card, "2026-08", "CORNER GROCERY")
	w.fuel = find(w.card, "2026-08", "FUEL STOP")
	w.hardware = find(w.card, "2026-09", "HARDWARE BARN")
	w.august = find(w.checking, "2026-09", "PROVINCE CARDS 0825")
	w.september = find(w.checking, "2026-10", "PROVINCE CARDS 0925")

	return w
}

func (w clearingWorld) explain(actor, id types.ID) ledgerbus.Explanation {
	w.t.Helper()

	x, err := w.ledger.Explain(w.t.Context(), actor, id)
	if err != nil {
		w.t.Fatal(err)
	}

	return x
}

func lineIDs(ls []ledgerbus.Line) []types.ID {
	out := make([]types.ID, len(ls))
	for i, l := range ls {
		out[i] = l.Transaction.ID
	}

	return out
}

func TestExplainingAnAmount(t *testing.T) {
	w := newClearing(t)
	ctx := t.Context()

	x := w.explain(w.owner, w.august.ID)
	if x.Count() != 0 || x.Settled() || !x.CanChange() || len(x.Choices) != 2 || x.From() != "2026-08" || x.To() != "2026-08" {
		t.Fatalf("to start: %+v", x)
	}

	offered, err := w.ledger.Candidates(ctx, w.owner, w.august.ID, []types.ID{w.card}, "2026-08", "2026-08")
	if err != nil || len(offered) != 2 {
		t.Fatalf("offered: %+v, %v", offered, err)
	}

	if err := w.ledger.Gather(ctx, now, w.owner, w.august.ID, lineIDs(offered), nil, []types.ID{w.card}, "2026-08", "2026-08"); err != nil {
		t.Fatal(err)
	}

	x = w.explain(w.owner, w.august.ID)
	if x.Count() != 2 || x.Sum != money.MustParse("-100.00") || x.Difference() != 0 || !x.Settled() {
		t.Errorf("explained: %+v", x)
	}

	// A charge explains one amount: September's payment is not offered
	// August's charges, and cannot take them.
	offered, err = w.ledger.Candidates(ctx, w.owner, w.september.ID, []types.ID{w.card}, "2026-08", "2026-09")
	if err != nil || len(offered) != 1 || offered[0].Transaction.ID != w.hardware.ID {
		t.Fatalf("offered for September: %+v, %v", offered, err)
	}

	if err := w.ledger.Gather(ctx, now, w.owner, w.september.ID, []types.ID{w.grocery.ID}, nil, []types.ID{w.card}, "2026-08", "2026-09"); !errors.Is(err, ledgerbus.ErrExplained) {
		t.Errorf("taking August's grocery: %v", err)
	}

	// Nor can a line be explained itself.
	if err := w.ledger.Gather(ctx, now, w.owner, w.grocery.ID, nil, nil, []types.ID{w.card}, "2026-08", "2026-08"); !errors.Is(err, ledgerbus.ErrExplained) {
		t.Errorf("explaining a line: %v", err)
	}

	// September's is gathered as August's was, a month back, from the
	// card: the description's payee is the same.
	x = w.explain(w.owner, w.september.ID)
	if len(x.Sources) != 1 || x.Sources[0] != w.card || x.From() != "2026-09" || x.To() != "2026-09" {
		t.Errorf("remembered: %+v from %s to %s", x.Sources, x.From(), x.To())
	}

	if err := w.ledger.Gather(ctx, now, w.owner, w.september.ID, []types.ID{w.hardware.ID}, nil, x.Sources, x.From(), x.To()); err != nil {
		t.Fatal(err)
	}

	// 25 of 30: open, until the difference is found or accepted.
	x = w.explain(w.owner, w.september.ID)
	if x.Difference() != money.MustParse("-5.00") || x.Settled() {
		t.Errorf("September: difference %s, settled %v", x.Difference(), x.Settled())
	}

	if err := w.ledger.Settle(ctx, now, w.owner, w.september.ID, strings.Repeat("x", ledgerbus.MaxExplainNote+1), true); !errors.Is(err, ledgerbus.ErrExplainNote) {
		t.Errorf("a long note: %v", err)
	}

	if err := w.ledger.Settle(ctx, now, w.owner, w.september.ID, "a parking hold, settled lower", true); err != nil {
		t.Fatal(err)
	}

	if x = w.explain(w.owner, w.september.ID); !x.Settled() || x.Note != "a parking hold, settled lower" {
		t.Errorf("accepted: %+v", x)
	}

	// What is not on any statement is entered by hand, with its document,
	// and added: the difference is gone without accepting it.
	if err := w.ledger.Settle(ctx, now, w.owner, w.september.ID, "", false); err != nil {
		t.Fatal(err)
	}

	ticket, err := w.ledger.Enter(ctx, now, w.owner, w.card, entry(t, "2026-09-28", "Parking, paid on another card", "-5.00", w.save(w.owner, "parking.pdf", aDocument)))
	if err != nil {
		t.Fatal(err)
	}

	if err := w.ledger.AddEntry(ctx, now, w.owner, w.september.ID, ticket); err != nil {
		t.Fatal(err)
	}

	if x = w.explain(w.owner, w.september.ID); x.Difference() != 0 || !x.Settled() || x.Count() != 2 {
		t.Errorf("with the entry: %+v", x)
	}

	// The lines say what they are cleared by, and the explained ones
	// whether they are settled.
	c, err := w.ledger.Clearing(ctx, w.owner, []types.ID{w.grocery.ID, w.august.ID, w.fuel.ID})
	if err != nil {
		t.Fatal(err)
	}

	if c.By[w.grocery.ID].Transaction.ID != w.august.ID || c.By[w.grocery.ID].Hidden || c.State(w.august.ID) != "explained" || c.State(w.fuel.ID) != "" {
		t.Errorf("clearing: %+v", c)
	}

	// Removing a statement takes its lines out of what they explained.
	if _, err := w.ledger.RemoveStatement(ctx, now, w.owner, w.card1.ID); err != nil {
		t.Fatal(err)
	}

	if x = w.explain(w.owner, w.august.ID); x.Count() != 0 {
		t.Errorf("after removing August's card statement: %d lines", x.Count())
	}

	// The history says who gathered and who settled.
	events, err := w.history.Recent(ctx, types.AccountScope(w.checking), 50)
	if err != nil {
		t.Fatal(err)
	}

	var gathered, settled int

	for _, e := range events {
		switch e.Action {
		case ledgerbus.ExplanationChanged:
			gathered++
		case ledgerbus.ExplanationSettled:
			settled++
		}
	}

	if gathered != 3 || settled != 2 {
		t.Errorf("history: %d gathered, %d settled", gathered, settled)
	}
}

// Somebody who may see the payment but not the card sees its lines as one
// sum; somebody who may see the card but not the checking account sees
// that a charge is cleared, and not by what.
func TestWhoSeesAnExplanation(t *testing.T) {
	w := newClearing(t)
	ctx := t.Context()

	if err := w.ledger.Gather(ctx, now, w.owner, w.august.ID, []types.ID{w.grocery.ID, w.fuel.ID}, nil, []types.ID{w.card}, "2026-08", "2026-08"); err != nil {
		t.Fatal(err)
	}

	viewer := w.user("viewer@example.org")
	w.grant(w.owner, types.AccountScope(w.checking), "viewer@example.org", tenancybus.Viewer)

	x := w.explain(viewer, w.august.ID)
	if len(x.Lines) != 0 || x.Hidden != 2 || x.HiddenSum != money.MustParse("-100.00") || x.CanChange() || len(x.Choices) != 0 {
		t.Errorf("the viewer sees %+v", x)
	}

	if err := w.ledger.Gather(ctx, now, viewer, w.august.ID, nil, []types.ID{w.grocery.ID}, nil, "", ""); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("the viewer gathering: %v", err)
	}

	if err := w.ledger.Settle(ctx, now, viewer, w.august.ID, "", true); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("the viewer settling: %v", err)
	}

	holder := w.user("holder@example.org")
	w.grant(w.owner, types.AccountScope(w.card), "holder@example.org", tenancybus.Bookkeeper)

	c, err := w.ledger.Clearing(ctx, holder, []types.ID{w.grocery.ID})
	if err != nil {
		t.Fatal(err)
	}

	if by := c.By[w.grocery.ID]; !by.Hidden || !by.Transaction.ID.Zero() {
		t.Errorf("the holder sees %+v", by)
	}

	// A bookkeeper of the card may not gather into a payment of an
	// account they cannot read, nor draw on one.
	if _, err := w.ledger.Candidates(ctx, holder, w.august.ID, []types.ID{w.card}, "2026-08", "2026-08"); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("the holder gathering: %v", err)
	}

	if _, err := w.ledger.Candidates(ctx, w.owner, w.august.ID, []types.ID{types.NewID()}, "2026-08", "2026-08"); !errors.Is(err, ledgerbus.ErrElsewhere) {
		t.Errorf("an account outside the organization: %v", err)
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.ledger.Explain(ctx, stranger, w.august.ID); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}

// A transfer that pays the card is met once it is explained: the card
// has charges, and no payment to answer it.
func TestAnExplainedTransferComesBackToZero(t *testing.T) {
	w := newClearing(t)
	ctx := t.Context()

	transfers, err := w.cats.Create(ctx, now, w.owner, types.OrgScope(w.org), "Paying the card", categorybus.Transfer)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.SetSplits(ctx, now, w.owner, w.august.ID, []ledgerbus.Part{{Amount: w.august.Amount, CategoryID: transfers.ID}}); err != nil {
		t.Fatal(err)
	}

	s, err := w.ledger.Settling(ctx, w.owner, types.OrgScope(w.org))
	if err != nil || len(s.Transfers) != 1 {
		t.Fatalf("before: %+v, %v", s.Transfers, err)
	}

	if err := w.ledger.Gather(ctx, now, w.owner, w.august.ID, []types.ID{w.grocery.ID, w.fuel.ID}, nil, []types.ID{w.card}, "2026-08", "2026-08"); err != nil {
		t.Fatal(err)
	}

	if s, err = w.ledger.Settling(ctx, w.owner, types.OrgScope(w.org)); err != nil || len(s.Transfers) != 0 {
		t.Errorf("after: %+v, %v", s.Transfers, err)
	}
}
