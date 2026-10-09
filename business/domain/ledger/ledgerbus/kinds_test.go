package ledgerbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A month adds up by kind beside its cash: the deposit as income, the
// groceries as an expense, a personal coffee as pass-through, and nothing
// sorted as nothing counted. What should come back to zero is listed.
func TestAMonthByKind(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()

	category := func(name string, k categorybus.Kind) types.ID {
		c, err := w.cats.Create(ctx, now, e.owner, types.OrgScope(e.org), name, k)
		if err != nil {
			t.Fatal(err)
		}

		return c.ID
	}

	offerings := category("Offerings", categorybus.Income)
	personal := category("Personal, repaid", categorybus.PassThrough)

	for id, parts := range map[types.ID][]ledgerbus.Part{
		e.deposit.ID: {{Amount: e.deposit.Amount, CategoryID: offerings}},
		e.grocery.ID: {
			{Amount: money.MustParse("-30.00"), CategoryID: e.groceries},
			{Amount: money.MustParse("-3.99"), CategoryID: personal},
		},
		e.coffee.ID: {{Amount: e.coffee.Amount, CategoryID: personal}},
	} {
		if _, err := w.ledger.SetSplits(ctx, now, e.owner, id, parts); err != nil {
			t.Fatal(err)
		}
	}

	months, err := w.ledger.Months(ctx, e.owner, e.account)
	if err != nil || len(months) != 1 {
		t.Fatalf("months: %+v %v", months, err)
	}

	// The second coffee, the offertory and the electric bill are not
	// sorted: counted as cash and not as operations.
	want := ledgerbus.Operations{
		Income:      money.MustParse("1000.00"),
		Expenses:    money.MustParse("-30.00"),
		PassThrough: money.MustParse("-7.49"),
		Unsorted:    money.MustParse("126.50"),
	}
	if got := months[0].Operations; got != want || got.Net() != money.MustParse("970.00") {
		t.Errorf("July: %+v", got)
	}

	s, err := w.ledger.Settling(ctx, e.owner, types.OrgScope(e.org))
	if err != nil {
		t.Fatal(err)
	}

	if len(s.PassThrough) != 1 || s.PassThrough[0].Category.ID != personal || s.PassThrough[0].Balance != money.MustParse("-7.49") ||
		len(s.PassThrough[0].Months) != 1 || len(s.Transfers) != 0 {
		t.Errorf("settling: %+v", s)
	}

	// Somebody with no role on the organization sees none of it.
	stranger := w.user("stranger@example.org")
	if _, err := w.ledger.Settling(ctx, stranger, types.OrgScope(e.org)); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}

// A refund is a negative expense, not income.
func TestARefundIsANegativeExpense(t *testing.T) {
	var o ledgerbus.Operations

	o.Add(categorybus.Expense, money.MustParse("-40.00"))
	o.Add(categorybus.Expense, money.MustParse("5.00"))
	o.Add(categorybus.Unsaid, money.MustParse("12.00"))

	if o.Expenses != money.MustParse("-35.00") || o.Income != 0 || o.Unsorted != money.MustParse("12.00") || o.Net() != money.MustParse("-35.00") {
		t.Errorf("%+v", o)
	}
}
