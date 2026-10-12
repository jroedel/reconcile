package ledgerbus_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The figures beside each account and project in a list (issue #106): the
// account's as cash with its statement's balance, the project's as its
// book adds it up, and nothing of either for somebody who may not read it.
func TestListFigures(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()

	if _, err := w.ledger.SetSplits(ctx, now, e.owner, e.grocery.ID, []ledgerbus.Part{
		{Amount: money.MustParse("-20.00"), CategoryID: e.groceries, ProjectID: e.project},
		{Amount: money.MustParse("-13.99"), CategoryID: e.groceries},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.SetSplits(ctx, now, e.owner, e.deposit.ID, []ledgerbus.Part{
		{Amount: money.MustParse("1000.00"), ProjectID: e.project},
	}); err != nil {
		t.Fatal(err)
	}

	accounts, err := w.ledger.AccountFigures(ctx, e.owner, []types.ID{e.account})
	if err != nil {
		t.Fatal(err)
	}

	// The July file's last balance is the one it closed on.
	st := w.statements(e.owner, e.account)[0]
	a := accounts[e.account]

	if a.Count != 6 || a.First.String() != "2026-07-01" || a.Last.String() != "2026-07-28" {
		t.Errorf("the account's days and count: %+v", a)
	}

	cash := ledgerbus.Total{Currency: "USD", In: money.MustParse("1250.00"), Out: money.MustParse("-160.99")}
	if len(a.Totals) != 1 || a.Totals[0] != cash {
		t.Errorf("the account's totals: %+v", a.Totals)
	}

	if !a.HasBalance() || a.Balance != money.MustParse("1089.01") || a.BalanceOn != st.End {
		t.Errorf("the account's balance: %s on %s, want 1089.01 on %s", a.Balance, a.BalanceOn, st.End)
	}

	// Two transactions, one with two parts of which one is in the project:
	// counted once each, and added up as the book adds them.
	projects, err := w.ledger.ProjectFigures(ctx, e.owner, []types.ID{e.project})
	if err != nil {
		t.Fatal(err)
	}

	book, err := w.ledger.ProjectBook(ctx, e.owner, e.project)
	if err != nil {
		t.Fatal(err)
	}

	p := projects[e.project]
	if p.Count != 2 || p.First.String() != "2026-07-01" || p.Last.String() != "2026-07-03" || p.HasBalance() {
		t.Errorf("the project's days and count: %+v", p)
	}

	if len(p.Totals) != 1 || len(book.Totals) != 1 || p.Totals[0] != book.Totals[0] {
		t.Errorf("the project's totals %+v, its book's %+v", p.Totals, book.Totals)
	}

	// A pilgrim given the project has its figures and not the account's; a
	// stranger has neither, the same as for ids that are nothing.
	pilgrim := w.user("pilgrim@example.org")
	w.grant(e.owner, types.ProjectScope(e.project), "pilgrim@example.org", tenancybus.Viewer)
	stranger := w.user("stranger@example.org")

	for who, c := range map[string]struct {
		actor              types.ID
		accounts, projects int
	}{
		"the pilgrim":  {pilgrim, 0, 1},
		"the stranger": {stranger, 0, 0},
	} {
		accounts, err := w.ledger.AccountFigures(ctx, c.actor, []types.ID{e.account, types.NewID()})
		if err != nil || len(accounts) != c.accounts {
			t.Errorf("%s's accounts: %+v, %v", who, accounts, err)
		}

		projects, err := w.ledger.ProjectFigures(ctx, c.actor, []types.ID{e.project, types.NewID()})
		if err != nil || len(projects) != c.projects {
			t.Errorf("%s's projects: %+v, %v", who, projects, err)
		}
	}
}
