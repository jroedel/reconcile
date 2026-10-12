package ledgerbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// An account for reference is kept to explain an organization's books and
// is not part of them (docs/plan.md, "Whose books"): once it is marked, its
// money is in none of the organization's sums, past months' included, and
// none of it goes in a project. Taking the mark off counts it again.
func TestAnAccountForReferenceCountsNowhere(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()

	personal, err := w.cats.Create(ctx, now, e.owner, types.OrgScope(e.org), "Personal, repaid", categorybus.PassThrough)
	if err != nil {
		t.Fatal(err)
	}

	// Sorted before the mark: groceries in the project, a personal coffee.
	for id, parts := range map[types.ID][]ledgerbus.Part{
		e.grocery.ID: {{Amount: e.grocery.Amount, CategoryID: e.groceries, ProjectID: e.project}},
		e.coffee.ID:  {{Amount: e.coffee.Amount, CategoryID: personal.ID}},
	} {
		if _, err := w.ledger.SetSplits(ctx, now, e.owner, id, parts); err != nil {
			t.Fatal(err)
		}
	}

	july := func() (int, money.Amount) {
		t.Helper()

		lines, totals, err := w.ledger.OrgPeriod(ctx, e.owner, e.org, date(t, "2026-07-01"), date(t, "2026-08-01"))
		if err != nil {
			t.Fatal(err)
		}

		var expenses money.Amount
		for _, tot := range totals {
			expenses += tot.Expenses
		}

		return len(lines), expenses
	}

	counted, expenses := july()
	if counted == 0 || expenses != e.grocery.Amount {
		t.Fatalf("before the mark: %d parts, expenses %s", counted, expenses)
	}

	if _, err := w.ten.SetAccountReference(ctx, now, e.owner, e.account, true); err != nil {
		t.Fatal(err)
	}

	if counted, expenses := july(); counted != 0 || expenses != 0 {
		t.Errorf("the organization's July still counts it: %d parts, expenses %s", counted, expenses)
	}

	if s, err := w.ledger.Settling(ctx, e.owner, types.OrgScope(e.org)); err != nil || len(s.PassThrough) != 0 || len(s.Transfers) != 0 {
		t.Errorf("what should come back to zero still counts it: %+v %v", s, err)
	}

	if book, err := w.ledger.ProjectBook(ctx, e.owner, e.project); err != nil || len(book.Lines) != 0 {
		t.Errorf("the project's book still counts it: %+v %v", book.Lines, err)
	}

	// Nothing new goes in a project, and the editor offers none; the
	// project a part is in already is still named.
	_, err = w.ledger.SetSplits(ctx, now, e.owner, e.deposit.ID, []ledgerbus.Part{{Amount: e.deposit.Amount, ProjectID: e.project}})
	if invalid, ok := errors.AsType[ledgerbus.Invalid](err); !ok || invalid.Field != "project" {
		t.Errorf("a part put in a project: %v", err)
	}

	if ed, err := w.ledger.Transaction(ctx, e.owner, e.deposit.ID); err != nil || len(ed.Projects) != 0 {
		t.Errorf("the deposit's project choices: %+v %v", ed.Projects, err)
	}

	if ed, err := w.ledger.Transaction(ctx, e.owner, e.grocery.ID); err != nil || len(ed.Projects) != 1 || ed.Projects[0].ID != e.project {
		t.Errorf("the groceries' project choices: %+v %v", ed.Projects, err)
	}

	// Its own pages still read it: it is checked and reconciled as ever.
	if months, err := w.ledger.Months(ctx, e.owner, e.account); err != nil || len(months) != 1 {
		t.Errorf("the account's own months: %+v %v", months, err)
	}

	if _, err := w.ten.SetAccountReference(ctx, now, e.owner, e.account, false); err != nil {
		t.Fatal(err)
	}

	if counted, expenses := july(); counted == 0 || expenses != e.grocery.Amount {
		t.Errorf("after the mark came off: %d parts, expenses %s", counted, expenses)
	}
}

// Only an owner of an organization's account may mark it, and an account
// on its own cannot be: it is its own books.
func TestWhoMarksAnAccountForReference(t *testing.T) {
	w := newWorld(t)
	e := newEstate(w)
	ctx := t.Context()

	own, err := w.ten.CreateAccount(ctx, now, e.owner, types.ID{}, tenancybus.AccountFields{Name: "My card", Kind: "card"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ten.SetAccountReference(ctx, now, e.owner, own.ID, true); err == nil {
		t.Error("an account on its own was marked for reference")
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.ten.SetAccountReference(ctx, now, stranger, e.account, true); !errors.Is(err, tenancybus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	a, err := w.ten.SetAccountReference(ctx, now, e.owner, e.account, true)
	if err != nil || !a.Reference {
		t.Fatalf("the owner: %+v %v", a, err)
	}

	if again, _, err := w.ten.Account(ctx, e.owner, e.account); err != nil || !again.Reference {
		t.Errorf("read back: %+v %v", again, err)
	}
}
