package categorybus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A house gathers its expenses under "Food": a grocery category goes under
// it and comes out of it; an income category cannot go under it; a group of
// another list is none of this one's; changing the grocery category's kind
// takes it out, and the history says each step. A viewer reads the groups
// and changes none of them.
func TestGroupsOfCategories(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	owner := w.user("treasurer@example.org")

	org, err := w.ten.CreateOrg(ctx, now, owner, "Holy Family House")
	if err != nil {
		t.Fatal(err)
	}

	scope := org.Scope()

	category := func(name string, k categorybus.Kind) categorybus.Category {
		t.Helper()

		c, err := w.cats.Create(ctx, now, owner, scope, name, k)
		if err != nil {
			t.Fatal(err)
		}

		return c
	}

	groceries, stipends := category("Groceries", categorybus.Expense), category("Mass stipends", categorybus.Income)

	if _, err := w.cats.AddHeading(ctx, now, owner, scope, " ", categorybus.Expense); err == nil {
		t.Error("a group with no name was added")
	}

	food, err := w.cats.AddHeading(ctx, now, owner, scope, "  Food ", categorybus.Expense)
	if err != nil || food.Name != "Food" {
		t.Fatalf("Food: %+v %v", food, err)
	}

	if _, err := w.cats.AddHeading(ctx, now, owner, scope, "food", categorybus.Expense); !errors.Is(err, categorybus.ErrDuplicate) {
		t.Errorf("a second Food: %v", err)
	}

	c, err := w.cats.SetHeading(ctx, now, owner, groceries.ID, food.ID)
	if err != nil || c.GroupID != food.ID {
		t.Fatalf("groceries under Food: %+v %v", c, err)
	}

	_, err = w.cats.SetHeading(ctx, now, owner, stipends.ID, food.ID)
	if invalid, ok := errors.AsType[tenancybus.Invalid](err); !ok || invalid.Field != "group-kind" {
		t.Errorf("income under an expense group: %v", err)
	}

	// Another list's group is not one this list's categories go under.
	other, err := w.ten.CreateOrg(ctx, now, owner, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	theirs, err := w.cats.AddHeading(ctx, now, owner, other.Scope(), "Food", categorybus.Expense)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.cats.SetHeading(ctx, now, owner, groceries.ID, theirs.ID); !errors.Is(err, categorybus.ErrNotFound) {
		t.Errorf("under another list's group: %v", err)
	}

	// An archived group takes no more, and keeps what it has.
	if _, err := w.cats.SetHeadingArchived(ctx, now, owner, food.ID, true); err != nil {
		t.Fatal(err)
	}

	market := category("Market", categorybus.Expense)

	_, err = w.cats.SetHeading(ctx, now, owner, market.ID, food.ID)
	if invalid, ok := errors.AsType[tenancybus.Invalid](err); !ok || invalid.Field != "group-archived" {
		t.Errorf("under an archived group: %v", err)
	}

	if list, _, _ := w.cats.List(ctx, owner, scope); !under(list, groceries.ID, food.ID) {
		t.Error("archiving Food took groceries out of it")
	}

	if _, err := w.cats.SetHeadingArchived(ctx, now, owner, food.ID, false); err != nil {
		t.Fatal(err)
	}

	if h, err := w.cats.RenameHeading(ctx, now, owner, food.ID, "Food and household"); err != nil || h.Name != "Food and household" {
		t.Errorf("renamed: %+v %v", h, err)
	}

	// Groceries becomes pass-through, and leaves the group of expenses.
	if c, err := w.cats.SetKind(ctx, now, owner, groceries.ID, categorybus.PassThrough); err != nil || !c.GroupID.Zero() {
		t.Errorf("after its kind changed: %+v %v", c, err)
	}

	if _, err := w.cats.SetKind(ctx, now, owner, groceries.ID, categorybus.Expense); err != nil {
		t.Fatal(err)
	}

	if _, err := w.cats.SetHeading(ctx, now, owner, groceries.ID, food.ID); err != nil {
		t.Fatal(err)
	}

	if c, err := w.cats.SetHeading(ctx, now, owner, groceries.ID, types.ID{}); err != nil || !c.GroupID.Zero() {
		t.Errorf("taken out: %+v %v", c, err)
	}

	events, err := w.history.Recent(ctx, scope, 50)
	if err != nil {
		t.Fatal(err)
	}

	said := map[string]int{}
	for _, e := range events {
		said[string(e.Action)]++

		if e.Action == categorybus.KindSet && e.Detail["kind"] == string(categorybus.PassThrough) && e.Detail["group"] != "Food and household" {
			t.Errorf("the kind's line does not say it left the group: %v", e.Detail)
		}
	}

	for action, n := range map[string]int{"group.added": 1, "group.archived": 1, "group.restored": 1, "group.renamed": 1, "category.grouped": 2, "category.ungrouped": 1} {
		if said[action] != n {
			t.Errorf("%s in the history %d times, want %d", action, said[action], n)
		}
	}

	viewer := w.user("viewer@example.org")
	w.grant(owner, scope, "viewer@example.org", tenancybus.Viewer)

	if hs, err := w.cats.Headings(ctx, viewer, scope); err != nil || len(hs) != 1 {
		t.Errorf("the viewer reading the groups: %+v %v", hs, err)
	}

	if _, err := w.cats.AddHeading(ctx, now, viewer, scope, "Mine", categorybus.Expense); !errors.Is(err, categorybus.ErrForbidden) {
		t.Errorf("the viewer adding a group: %v", err)
	}

	if _, err := w.cats.SetHeading(ctx, now, viewer, groceries.ID, food.ID); !errors.Is(err, categorybus.ErrForbidden) {
		t.Errorf("the viewer grouping a category: %v", err)
	}
}

func under(list []categorybus.Category, category, heading types.ID) bool {
	for _, c := range list {
		if c.ID == category {
			return c.GroupID == heading
		}
	}

	return false
}
