package categorybus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
)

// A new category has a kind; only the owner changes it, and the history says
// from what to what.
func TestEveryNewCategoryHasAKind(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")

	org, err := w.ten.CreateOrg(t.Context(), now, me, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	for _, k := range []categorybus.Kind{categorybus.Unsaid, "refund"} {
		_, err := w.cats.Create(t.Context(), now, me, org.Scope(), "Fuel", k)
		if inv, ok := errors.AsType[tenancybus.Invalid](err); !ok || inv.Field != "category-kind" {
			t.Errorf("a category of kind %q: %v", k, err)
		}
	}

	fuel, err := w.cats.Create(t.Context(), now, me, org.Scope(), "Fuel", categorybus.Income)
	if err != nil {
		t.Fatal(err)
	}

	// A bookkeeper sorts with it, and may not change what it is.
	bookkeeper := w.user("bookkeeper@example.org")
	w.grant(me, org.Scope(), "bookkeeper@example.org", tenancybus.Bookkeeper)

	if _, err := w.cats.SetKind(t.Context(), now, bookkeeper, fuel.ID, categorybus.Expense); !errors.Is(err, categorybus.ErrForbidden) {
		t.Errorf("a bookkeeper changing a kind: %v", err)
	}

	got, err := w.cats.SetKind(t.Context(), now, me, fuel.ID, categorybus.Expense)
	if err != nil || got.Kind != categorybus.Expense {
		t.Fatalf("the owner changing it: %+v %v", got, err)
	}

	list, _, _ := w.cats.List(t.Context(), me, org.Scope())
	if len(list) != 1 || list[0].Kind != categorybus.Expense {
		t.Errorf("the list: %+v", list)
	}

	if !categorybus.Expense.Operations() || categorybus.Transfer.Operations() || categorybus.Unsaid.Operations() {
		t.Error("Operations is income and expenses only")
	}
}

// A new list starts with a transfer and a pass-through category, once.
func TestANewListStartsWithTransfersAndPassThrough(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")

	org, err := w.ten.CreateOrg(t.Context(), now, me, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	starters := []categorybus.Starter{
		{Name: "Transfers between our accounts", Kind: categorybus.Transfer},
		{Name: "Personal, repaid", Kind: categorybus.PassThrough},
	}

	for range 2 {
		if err := w.cats.Start(t.Context(), now, me, org.Scope(), starters); err != nil {
			t.Fatal(err)
		}
	}

	list, _, _ := w.cats.List(t.Context(), me, org.Scope())
	if len(list) != 2 || list[0].Kind != categorybus.PassThrough || list[1].Kind != categorybus.Transfer {
		t.Errorf("the list: %+v", list)
	}
}
