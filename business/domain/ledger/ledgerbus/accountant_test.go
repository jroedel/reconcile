package ledgerbus_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// accounted is an estate with a second account, the parish's card, holding
// the same July, and the electric bill of each named: the bill is what
// each case makes important or not, and gives context or not.
type accounted struct {
	estate

	w                  *world
	card               types.ID
	electric, cardBill ledgerbus.Transaction
	cardCoffee         ledgerbus.Transaction
}

func newAccounted(w *world) accounted {
	w.t.Helper()

	ctx := w.t.Context()
	a := accounted{estate: newEstate(w), w: w}

	card, err := w.ten.CreateAccount(ctx, now, a.owner, a.org, tenancybus.AccountFields{Name: "Parish card", Kind: "card"})
	if err != nil {
		w.t.Fatal(err)
	}

	a.card = card.ID
	w.imports(a.owner, a.card, "checking-july.csv")

	checking, err := w.ledger.Transactions(ctx, a.owner, a.account, "2026-07")
	if err != nil {
		w.t.Fatal(err)
	}

	cards, err := w.ledger.Transactions(ctx, a.owner, a.card, "2026-07")
	if err != nil {
		w.t.Fatal(err)
	}

	a.electric, a.cardBill, a.cardCoffee = checking[5], cards[5], cards[2]

	return a
}

// check gives a transaction a check number, as reading its image does.
func (a accounted) check(id types.ID) {
	a.w.t.Helper()

	if _, err := a.w.db.ExecContext(a.w.t.Context(), `UPDATE transactions SET check_number = '1322' WHERE id = ?`, id.String()); err != nil {
		a.w.t.Fatal(err)
	}
}

func (a accounted) split(id types.ID, parts ...ledgerbus.Part) {
	a.w.t.Helper()

	if _, err := a.w.ledger.SetSplits(a.w.t.Context(), now, a.owner, id, parts); err != nil {
		a.w.t.Fatal(err)
	}
}

func (a accounted) category(name string, kind categorybus.Kind) types.ID {
	a.w.t.Helper()

	c, err := a.w.cats.Create(a.w.t.Context(), now, a.owner, types.OrgScope(a.org), name, kind)
	if err != nil {
		a.w.t.Fatal(err)
	}

	return c.ID
}

func (a accounted) reference() {
	a.w.t.Helper()

	if _, err := a.w.ten.SetAccountReference(a.w.t.Context(), now, a.owner, a.card, true); err != nil {
		a.w.t.Fatal(err)
	}
}

// explain makes lines of one account explain a transaction of another.
func (a accounted) explain(id, line, from types.ID) {
	a.w.t.Helper()

	if err := a.w.ledger.Gather(a.w.t.Context(), now, a.owner, id, []types.ID{line}, nil, []types.ID{from}, "2026-07", "2026-07"); err != nil {
		a.w.t.Fatal(err)
	}
}

// listed is which of the accountant's lists the transaction is in, read
// over the organization, with the row's reasons and what it was given.
func (a accounted) listed(c ledgerbus.Criteria, id types.ID) (ledgerbus.ForAccountant, []ledgerbus.Reason, []ledgerbus.Given) {
	a.w.t.Helper()

	var (
		in    ledgerbus.ForAccountant
		why   []ledgerbus.Reason
		given []ledgerbus.Given
	)

	for _, list := range []ledgerbus.ForAccountant{ledgerbus.MoreContextNeeded, ledgerbus.ContextAdded} {
		c.Accountant = list

		ex, err := a.w.ledger.Explore(a.w.t.Context(), a.owner, types.OrgScope(a.org), c)
		if err != nil {
			a.w.t.Fatal(err)
		}

		for _, r := range ex.Rows {
			if r.Transaction.ID != id {
				continue
			}

			if in != "" {
				a.w.t.Errorf("in both lists: %s and %s", in, list)
			}

			in, why, given = list, r.Why, r.Given
		}
	}

	return in, why, given
}

// Each of the four reasons a transaction is important, and each kind of
// context, alone, in the list it belongs in; and what is not a reason or
// not context, in neither or still to do.
func TestTheAccountantsLists(t *testing.T) {
	type (
		R = ledgerbus.Reason
		G = ledgerbus.Given
	)

	const (
		needed = ledgerbus.MoreContextNeeded
		added  = ledgerbus.ContextAdded
	)

	cases := []struct {
		name  string
		setup func(a accounted) types.ID
		in    ledgerbus.ForAccountant
		why   []R
		given []G
	}{
		{
			name:  "nothing about it",
			setup: func(a accounted) types.ID { return a.electric.ID },
		},
		{
			name: "a category a person chose is not a reason",
			setup: func(a accounted) types.ID {
				a.split(a.electric.ID, ledgerbus.Part{Amount: a.electric.Amount, CategoryID: a.utilities})
				return a.electric.ID
			},
		},
		{
			name:  "a check",
			setup: func(a accounted) types.ID { a.check(a.electric.ID); return a.electric.ID },
			in:    needed, why: []R{ledgerbus.BecauseCheck},
		},
		{
			name: "a transfer part",
			setup: func(a accounted) types.ID {
				a.split(a.electric.ID, ledgerbus.Part{Amount: a.electric.Amount, CategoryID: a.category("To savings", categorybus.Transfer)})
				return a.electric.ID
			},
			in: needed, why: []R{ledgerbus.BecauseTransfer},
		},
		{
			name: "a pass-through part beside an expense",
			setup: func(a accounted) types.ID {
				a.split(a.electric.ID,
					ledgerbus.Part{Amount: money.MustParse("-100.00"), CategoryID: a.utilities},
					ledgerbus.Part{Amount: money.MustParse("-20.00"), CategoryID: a.category("Collections to pass on", categorybus.PassThrough)})
				return a.electric.ID
			},
			in: needed, why: []R{ledgerbus.BecausePassThrough},
		},
		{
			name: "explained by a line in an account for reference, not adding up",
			setup: func(a accounted) types.ID {
				a.reference()
				a.explain(a.electric.ID, a.cardCoffee.ID, a.card)
				return a.electric.ID
			},
			in: needed, why: []R{ledgerbus.BecauseReference},
		},
		{
			name: "a line explaining a transaction in an account for reference",
			setup: func(a accounted) types.ID {
				a.reference()
				a.explain(a.cardBill.ID, a.electric.ID, a.account)
				return a.electric.ID
			},
			in: needed, why: []R{ledgerbus.BecauseReference},
		},
		{
			name: "explained by a line in an account for reference that adds up",
			setup: func(a accounted) types.ID {
				a.reference()
				a.explain(a.electric.ID, a.cardBill.ID, a.card)
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseReference}, given: []G{ledgerbus.GaveExplanation},
		},
		{
			name: "a reported payee, who is a payee",
			setup: func(a accounted) types.ID {
				tx, err := a.w.ledger.SetPayee(a.w.t.Context(), now, a.owner, a.electric.ID, "Hilltop Electric")
				if err != nil {
					a.w.t.Fatal(err)
				}

				if _, err := a.w.ledger.SetPayeeMarks(a.w.t.Context(), now, a.owner, tx.PayeeID, ledgerbus.PayeeMarks{Reported: true}); err != nil {
					a.w.t.Fatal(err)
				}

				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseReported}, given: []G{ledgerbus.GavePayee},
		},
		{
			name: "a check with its own description",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				if _, err := a.w.ledger.Describe(a.w.t.Context(), now, a.owner, a.electric.ID, "Lights for the hall"); err != nil {
					a.w.t.Fatal(err)
				}
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveDescription},
		},
		{
			name: "a check with a receipt",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				a.w.ledger.OnReceipts(func(context.Context, []types.ID) (map[types.ID]int, error) {
					return map[types.ID]int{a.electric.ID: 1}, nil
				})
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveReceipt},
		},
		{
			name: "a check with its memo",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				if _, err := a.w.db.ExecContext(a.w.t.Context(), `UPDATE transactions SET check_memo = 'hall' WHERE id = ?`, a.electric.ID.String()); err != nil {
					a.w.t.Fatal(err)
				}
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveCheckMemo},
		},
		{
			name: "a check explained by lines that add up",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				a.explain(a.electric.ID, a.cardBill.ID, a.card)
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveExplanation},
		},
		{
			name: "a check whose difference was accepted with a note",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				if err := a.w.ledger.Settle(a.w.t.Context(), now, a.owner, a.electric.ID, "The hall's lights, paid in cash too", true); err != nil {
					a.w.t.Fatal(err)
				}
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveExplanation},
		},
		{
			name: "a check whose difference was accepted without a note",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				if err := a.w.ledger.Settle(a.w.t.Context(), now, a.owner, a.electric.ID, "", true); err != nil {
					a.w.t.Fatal(err)
				}
				return a.electric.ID
			},
			in: needed, why: []R{ledgerbus.BecauseCheck},
		},
		{
			name: "a check in a project",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				a.split(a.electric.ID, ledgerbus.Part{Amount: a.electric.Amount, CategoryID: a.utilities, ProjectID: a.project})
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveProject},
		},
		{
			name: "a check with a part's memo",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				a.split(a.electric.ID, ledgerbus.Part{Amount: a.electric.Amount, CategoryID: a.utilities, Memo: "Hall"})
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GavePartMemo},
		},
		{
			name: "a check with a payee",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				if _, err := a.w.ledger.SetPayee(a.w.t.Context(), now, a.owner, a.electric.ID, "Hilltop Electric"); err != nil {
					a.w.t.Fatal(err)
				}
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GavePayee},
		},
		{
			name: "a check with a question answered",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				a.w.ledger.OnAnswered(func(context.Context, []types.ID) (map[types.ID]bool, error) {
					return map[types.ID]bool{a.electric.ID: true}, nil
				})
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck}, given: []G{ledgerbus.GaveAnswer},
		},
		{
			name: "a check and a transfer, with a receipt and a description",
			setup: func(a accounted) types.ID {
				a.check(a.electric.ID)
				a.split(a.electric.ID, ledgerbus.Part{Amount: a.electric.Amount, CategoryID: a.category("To savings", categorybus.Transfer)})
				if _, err := a.w.ledger.Describe(a.w.t.Context(), now, a.owner, a.electric.ID, "Lights"); err != nil {
					a.w.t.Fatal(err)
				}
				a.w.ledger.OnReceipts(func(context.Context, []types.ID) (map[types.ID]int, error) {
					return map[types.ID]int{a.electric.ID: 2}, nil
				})
				return a.electric.ID
			},
			in: added, why: []R{ledgerbus.BecauseCheck, ledgerbus.BecauseTransfer}, given: []G{ledgerbus.GaveDescription, ledgerbus.GaveReceipt},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAccounted(newWorld(t))
			id := tc.setup(a)

			in, why, given := a.listed(ledgerbus.Criteria{}, id)

			if in != tc.in || !slices.Equal(why, tc.why) || !slices.Equal(given, tc.given) {
				t.Errorf("in %q because %v, given %v; want %q because %v, given %v", in, why, given, tc.in, tc.why, tc.given)
			}
		})
	}
}

// A transaction in an account for reference is never to do, as nothing
// there is ever to sort, and is listed only when its account is chosen.
func TestTheAccountantsListsLeaveAccountsForReferenceOut(t *testing.T) {
	a := newAccounted(newWorld(t))
	a.reference()
	a.check(a.cardBill.ID)

	if in, _, _ := a.listed(ledgerbus.Criteria{Account: a.card}, a.cardBill.ID); in != "" {
		t.Errorf("a check in an account for reference, nothing given: in %q", in)
	}

	if _, err := a.w.ledger.Describe(t.Context(), now, a.owner, a.cardBill.ID, "Lights"); err != nil {
		t.Fatal(err)
	}

	if in, _, _ := a.listed(ledgerbus.Criteria{}, a.cardBill.ID); in != "" {
		t.Errorf("with a description, its account not chosen: in %q", in)
	}

	if in, _, _ := a.listed(ledgerbus.Criteria{Account: a.card}, a.cardBill.ID); in != ledgerbus.ContextAdded {
		t.Errorf("with a description, its account chosen: in %q", in)
	}
}

// On a project's list only the project's parts are judged: a role on the
// project does not learn that the rest of a charge was a transfer.
func TestTheAccountantsListsOnAProjectJudgeOnlyItsParts(t *testing.T) {
	w := newWorld(t)
	a := newAccounted(w)
	ctx := t.Context()

	a.split(a.electric.ID,
		ledgerbus.Part{Amount: money.MustParse("-100.00"), CategoryID: a.utilities, ProjectID: a.project},
		ledgerbus.Part{Amount: money.MustParse("-20.00"), CategoryID: a.category("To savings", categorybus.Transfer)})

	if in, why, given := a.listed(ledgerbus.Criteria{}, a.electric.ID); in != ledgerbus.ContextAdded ||
		!slices.Equal(why, []ledgerbus.Reason{ledgerbus.BecauseTransfer}) || !slices.Equal(given, []ledgerbus.Given{ledgerbus.GaveProject}) {
		t.Errorf("on the organization: in %q because %v, given %v", in, why, given)
	}

	pilgrim := w.user("pilgrim@example.org")
	w.grant(a.owner, types.ProjectScope(a.project), "pilgrim@example.org", tenancybus.Viewer)

	for _, list := range []ledgerbus.ForAccountant{ledgerbus.MoreContextNeeded, ledgerbus.ContextAdded} {
		ex, err := w.ledger.Explore(ctx, pilgrim, types.ProjectScope(a.project), ledgerbus.Criteria{Accountant: list})
		if err != nil || ex.Total != 0 {
			t.Errorf("the project's %s list: %v, %v", list, lines(ex), err)
		}
	}
}

// Find, the API's search, has the lists too, and says each row's reasons.
func TestFindingWhatTheAccountantNeeds(t *testing.T) {
	a := newAccounted(newWorld(t))
	a.check(a.electric.ID)

	got, err := a.w.ledger.Find(t.Context(), a.owner, ledgerbus.Query{Criteria: ledgerbus.Criteria{Accountant: ledgerbus.MoreContextNeeded}})
	if err != nil || len(got.Found) != 1 || got.Found[0].Transaction.ID != a.electric.ID ||
		!slices.Equal(got.Found[0].Why, []ledgerbus.Reason{ledgerbus.BecauseCheck}) {
		t.Errorf("found %+v, %v", got.Found, err)
	}
}
