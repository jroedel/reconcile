package ledgerbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// July sorted by hand teaches August: the coffee is suggested from two
// earlier ones, the grocery is a rule's made since, the electric bill has
// too little behind it; one save sorts what was chosen and leaves the rest.
func TestSortingAMonth(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()

	july := byDescription(w, e.owner, e.account, "2026-07")

	for _, tx := range append(july["COFFEE CART"], july["ELECTRIC CO"]...) {
		category := e.groceries
		if tx.Description == "ELECTRIC CO" {
			category = e.utilities
		}

		if _, err := w.ledger.SetSplits(ctx, now, e.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount, CategoryID: category}}); err != nil {
			t.Fatal(err)
		}
	}

	w.imports(e.owner, e.account, "checking-august.csv")

	grocer, err := w.rules.Save(ctx, now, e.owner, e.account, rulebus.Fields{Match: "corner grocery", Direction: rulebus.Out, CategoryID: e.groceries, ProjectID: e.project})
	if err != nil {
		t.Fatal(err)
	}

	m, err := w.ledger.ToSort(ctx, now, e.owner, e.account, "2026-08")
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Rows) != 3 || m.More != 0 || len(m.Projects) != 1 || m.CategoryName(e.utilities) != "Utilities" {
		t.Fatalf("the month: %+v", m)
	}

	rows := map[string]ledgerbus.SortRow{}
	for _, r := range m.Rows {
		rows[r.Transaction.Description] = r
	}

	if g := rows["SQ *COFFEE CART 0802"].Guess; g.By != ledgerbus.BySuggestion || g.CategoryID != e.groceries ||
		g.Suggestion.Agree != 2 || g.Suggestion.Of != 2 || g.Suggestion.Payee != "COFFEE CART" {
		t.Errorf("the coffee's guess: %+v", g)
	}

	if g := rows["CORNER GROCERY"].Guess; g.By != ledgerbus.ByRule || g.Rule.ID != grocer.ID || g.ProjectID != e.project {
		t.Errorf("the grocery's guess: %+v", g)
	}

	if g := rows["ELECTRIC CO"].Guess; g.Made() {
		t.Errorf("one electric bill is not evidence: %+v", g)
	}

	// The transaction's own page guesses the same.
	ed, _ := w.ledger.Transaction(ctx, e.owner, rows["SQ *COFFEE CART 0802"].Transaction.ID)
	if g, err := w.ledger.Guess(ctx, now, e.owner, ed); err != nil || g.CategoryID != e.groceries {
		t.Errorf("the page's guess: %+v %v", g, err)
	}

	// The coffee is chosen and the grocery left; the electric bill, into a
	// category that is not on the list is refused on its own.

	saved, refused, err := w.ledger.SortMany(ctx, now, e.owner, e.account, []ledgerbus.Choice{
		{TransactionID: rows["SQ *COFFEE CART 0802"].Transaction.ID, CategoryID: e.groceries},
		{TransactionID: rows["ELECTRIC CO"].Transaction.ID, CategoryID: types.NewID()},
		{TransactionID: rows["CORNER GROCERY"].Transaction.ID},
	})
	if err != nil || saved != 1 || len(refused) != 1 {
		t.Fatalf("saved %d, refused %v: %v", saved, refused, err)
	}

	if inv, ok := errors.AsType[ledgerbus.Invalid](refused[rows["ELECTRIC CO"].Transaction.ID]); !ok || inv.Field != "category" {
		t.Errorf("the electric bill's refusal: %v", refused)
	}

	if m, _ := w.ledger.ToSort(ctx, now, e.owner, e.account, "2026-08"); len(m.Rows) != 2 {
		t.Errorf("%d left to sort", len(m.Rows))
	}

	aug := byDescription(w, e.owner, e.account, "2026-08")
	if sp := aug["SQ *COFFEE CART 0802"][0].Splits[0]; sp.CategoryID != e.groceries || !sp.RuleID.Zero() {
		t.Errorf("the coffee: %+v", sp)
	}

	// Saving what is stored already changes nothing: a rule's mark stays.
	if _, err := w.ledger.SortUnsorted(ctx, now, e.owner, e.account); err != nil {
		t.Fatal(err)
	}

	grocery := byDescription(w, e.owner, e.account, "2026-08")["CORNER GROCERY"][0]
	if !grocery.ByRule() || grocery.Splits[0].CategoryID != e.groceries {
		t.Fatalf("the grocery: %+v", grocery.Splits)
	}

	if saved, _, _ := w.ledger.SortMany(ctx, now, e.owner, e.account, []ledgerbus.Choice{
		{TransactionID: grocery.ID, CategoryID: e.groceries, ProjectID: e.project},
	}); saved != 0 {
		t.Errorf("a sorted transaction was saved again")
	}

	// A viewer sorts nothing, a stranger finds nothing.
	viewer := w.user("viewer@example.org")
	w.grant(e.owner, types.AccountScope(e.account), "viewer@example.org", tenancybus.Viewer)

	for who, want := range map[types.ID]error{viewer: ledgerbus.ErrForbidden, w.user("stranger@example.org"): ledgerbus.ErrNotFound} {
		if _, err := w.ledger.ToSort(ctx, now, who, e.account, "2026-08"); !errors.Is(err, want) {
			t.Errorf("ToSort: %v", err)
		}

		if _, _, err := w.ledger.SortMany(ctx, now, who, e.account, []ledgerbus.Choice{{TransactionID: grocery.ID, CategoryID: e.utilities}}); !errors.Is(err, want) {
			t.Errorf("SortMany: %v", err)
		}
	}
}
