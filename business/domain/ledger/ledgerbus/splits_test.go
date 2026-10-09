package ledgerbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// estate is an organization with an account that has July in it, a
// category list, and a project.
type estate struct {
	owner                    types.ID
	org, account, project    types.ID
	utilities, groceries     types.ID
	grocery, deposit, coffee ledgerbus.Transaction
}

func newEstate(w *world) estate {
	w.t.Helper()

	ctx := w.t.Context()
	e := estate{owner: w.user("treasurer@example.org")}

	org, err := w.ten.CreateOrg(ctx, now, e.owner, "St. Joseph Parish")
	if err != nil {
		w.t.Fatal(err)
	}

	acct, err := w.ten.CreateAccount(ctx, now, e.owner, org.ID, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		w.t.Fatal(err)
	}

	p, err := w.ten.CreateProject(ctx, now, e.owner, org.ID, tenancybus.ProjectFields{Name: "World Youth Day"})
	if err != nil {
		w.t.Fatal(err)
	}

	e.org, e.account, e.project = org.ID, acct.ID, p.ID

	for name, into := range map[string]*types.ID{"Utilities": &e.utilities, "Groceries": &e.groceries} {
		c, err := w.cats.Create(ctx, now, e.owner, org.Scope(), name, categorybus.Expense)
		if err != nil {
			w.t.Fatal(err)
		}

		*into = c.ID
	}

	w.imports(e.owner, e.account, "checking-july.csv")

	txs, err := w.ledger.Transactions(ctx, e.owner, e.account, "2026-07")
	if err != nil {
		w.t.Fatal(err)
	}

	e.deposit, e.grocery, e.coffee = txs[0], txs[1], txs[2]

	return e
}

func TestEveryTransactionArrivesWithOnePart(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)

	if len(e.grocery.Splits) != 1 || e.grocery.Splits[0].Amount != e.grocery.Amount || e.grocery.Sorted() {
		t.Errorf("parts: %+v", e.grocery.Splits)
	}

	months, _ := w.ledger.Months(t.Context(), e.owner, e.account)
	if months[0].Unsorted != 6 {
		t.Errorf("%d unsorted, want all 6", months[0].Unsorted)
	}
}

func TestSplittingATransaction(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()

	set := func(parts ...ledgerbus.Part) error {
		_, err := w.ledger.SetSplits(ctx, now, e.owner, e.grocery.ID, parts)

		return err
	}

	// -33.99 into groceries and utilities.
	if err := set(
		ledgerbus.Part{Amount: money.MustParse("-30.00"), CategoryID: e.groceries},
		ledgerbus.Part{Amount: money.MustParse("-3.99"), CategoryID: e.utilities, Memo: "light bulbs"},
	); err != nil {
		t.Fatal(err)
	}

	ed, err := w.ledger.Transaction(ctx, e.owner, e.grocery.ID)
	if err != nil {
		t.Fatal(err)
	}

	if sp := ed.Transaction.Splits; len(sp) != 2 || sp[1].Memo != "light bulbs" || !ed.Transaction.Sorted() {
		t.Errorf("parts: %+v", sp)
	}

	months, _ := w.ledger.Months(ctx, e.owner, e.account)
	if months[0].Unsorted != 5 {
		t.Errorf("%d unsorted", months[0].Unsorted)
	}

	for name, c := range map[string]struct {
		parts []ledgerbus.Part
		field string
	}{
		"not adding up": {[]ledgerbus.Part{{Amount: money.MustParse("-30.00")}}, "sum"},
		"the wrong way round": {[]ledgerbus.Part{
			{Amount: money.MustParse("-40.00")}, {Amount: money.MustParse("6.01")},
		}, "amount"},
		"a zero part":              {[]ledgerbus.Part{{Amount: money.MustParse("-33.99")}, {}}, "amount"},
		"none":                     {nil, "count"},
		"somebody else's category": {[]ledgerbus.Part{{Amount: money.MustParse("-33.99"), CategoryID: types.NewID()}}, "category"},
	} {
		err := set(c.parts...)
		if invalid, ok := errors.AsType[ledgerbus.Invalid](err); !ok || invalid.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}

	// An archived category is off the choices -- except where it is already.
	if _, err := w.cats.SetArchived(ctx, now, e.owner, e.utilities, true); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.SetSplits(ctx, now, e.owner, e.coffee.ID, []ledgerbus.Part{{Amount: e.coffee.Amount, CategoryID: e.utilities}}); err == nil {
		t.Error("an archived category was chosen")
	}

	if err := set(
		ledgerbus.Part{Amount: money.MustParse("-31.00"), CategoryID: e.groceries},
		ledgerbus.Part{Amount: money.MustParse("-2.99"), CategoryID: e.utilities},
	); err != nil {
		t.Errorf("keeping an archived category that was there: %v", err)
	}
}

// Money into a project: written into the project's history, added up in its
// book, and only by somebody who keeps the books of both.
func TestAProjectsBook(t *testing.T) {
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

	book, err := w.ledger.ProjectBook(ctx, e.owner, e.project)
	if err != nil {
		t.Fatal(err)
	}

	if len(book.Lines) != 2 || book.Lines[0].AccountName != "Parish checking" || book.Lines[1].CategoryName != "Groceries" {
		t.Errorf("lines: %+v", book.Lines)
	}

	// The deposit has no category, so it is money in and not yet income:
	// the project is short by the groceries until somebody says what the
	// deposit was.
	want := ledgerbus.Total{
		Currency: "USD", In: money.MustParse("1000.00"), Out: money.MustParse("-20.00"),
		Operations: ledgerbus.Operations{Expenses: money.MustParse("-20.00"), Unsorted: money.MustParse("1000.00")},
	}
	if len(book.Totals) != 1 || book.Totals[0] != want || book.Totals[0].Net() != money.MustParse("-20.00") {
		t.Errorf("totals: %+v", book.Totals)
	}

	if len(book.ByCategory) != 2 || book.ByCategory[0].Key != "" || book.ByCategory[1].Key != "Groceries" {
		t.Errorf("by category: %+v", book.ByCategory)
	}

	events, _ := w.history.Recent(ctx, types.ProjectScope(e.project), 5)
	if len(events) < 2 || events[0].Action != ledgerbus.SplitAdded || events[0].Detail["account"] != "Parish checking" {
		t.Errorf("history: %+v", events)
	}

	// A pilgrim given the project sees its book, and not the account.
	pilgrim := w.user("pilgrim@example.org")
	w.grant(e.owner, types.ProjectScope(e.project), "pilgrim@example.org", tenancybus.Viewer)

	if b, err := w.ledger.ProjectBook(ctx, pilgrim, e.project); err != nil || len(b.Lines) != 2 {
		t.Errorf("the pilgrim's book: %v", err)
	}

	if _, err := w.ledger.Transactions(ctx, pilgrim, e.account, "2026-07"); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("the pilgrim read the account: %v", err)
	}

	// A bookkeeper of the account alone may not put money into the project,
	// and may re-sort a transaction that is in it without taking it out.
	clerk := w.user("clerk@example.org")
	w.grant(e.owner, types.AccountScope(e.account), "clerk@example.org", tenancybus.Bookkeeper)

	ed, _ := w.ledger.Transaction(ctx, clerk, e.coffee.ID)
	for _, p := range ed.Projects {
		if p.ID == e.project {
			t.Error("the clerk is offered a project they cannot keep")
		}
	}

	_, err = w.ledger.SetSplits(ctx, now, clerk, e.coffee.ID, []ledgerbus.Part{{Amount: e.coffee.Amount, ProjectID: e.project}})
	if invalid, ok := errors.AsType[ledgerbus.Invalid](err); !ok || invalid.Field != "project" {
		t.Errorf("the clerk put money into the project: %v", err)
	}

	ed, _ = w.ledger.Transaction(ctx, clerk, e.deposit.ID)
	if ed.ProjectName(e.project) != "World Youth Day" {
		t.Errorf("the clerk cannot tell where the deposit went: %+v", ed.Projects)
	}

	if _, err := w.ledger.SetSplits(ctx, now, clerk, e.deposit.ID, []ledgerbus.Part{{Amount: e.deposit.Amount, ProjectID: e.project, CategoryID: e.groceries}}); err != nil {
		t.Errorf("re-sorting what is already in the project: %v", err)
	}

	// Taking it out is written down too.
	if _, err := w.ledger.SetSplits(ctx, now.Add(time.Hour), e.owner, e.deposit.ID, []ledgerbus.Part{{Amount: e.deposit.Amount}}); err != nil {
		t.Fatal(err)
	}

	events, _ = w.history.Recent(ctx, types.ProjectScope(e.project), 1)
	if events[0].Action != ledgerbus.SplitRemoved || events[0].Detail["before"] != "1000.00" {
		t.Errorf("history: %+v", events[0])
	}

	// A viewer of the account reads and changes nothing.
	if _, err := w.ledger.SetSplits(ctx, now, pilgrim, e.coffee.ID, []ledgerbus.Part{{Amount: e.coffee.Amount}}); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("somebody without the account: %v", err)
	}
}

// Removing a statement takes the parts with the transactions.
func TestRemovingAStatementTakesItsParts(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)

	if _, err := w.ledger.SetSplits(t.Context(), now, e.owner, e.grocery.ID, []ledgerbus.Part{{Amount: e.grocery.Amount, ProjectID: e.project}}); err != nil {
		t.Fatal(err)
	}

	sts, _ := w.ledger.Statements(t.Context(), e.owner, e.account)
	if _, err := w.ledger.RemoveStatement(t.Context(), now, e.owner, sts[0].ID); err != nil {
		t.Fatal(err)
	}

	if book, _ := w.ledger.ProjectBook(t.Context(), e.owner, e.project); len(book.Lines) != 0 {
		t.Errorf("%d lines left in the project", len(book.Lines))
	}
}
