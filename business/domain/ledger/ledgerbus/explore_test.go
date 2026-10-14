package ledgerbus_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// explored is an estate sorted for searching: the parish's checking and
// its card, both with July's statement; in the checking, the groceries
// split between the project's food and a pass-through part, the offertory
// all pass-through, the electric bill a utility with a memo, the first
// coffee in a category whose kind was never said, and the rest unsorted.
type explored struct {
	estate

	card                      types.ID
	passThrough, unsaid       types.ID
	offertory, electric, cafe ledgerbus.Transaction
}

func newExplored(w *world) explored {
	w.t.Helper()

	ctx := w.t.Context()
	x := explored{estate: newEstate(w)}

	card, err := w.ten.CreateAccount(ctx, now, x.owner, x.org, tenancybus.AccountFields{Name: "Parish card", Kind: "card"})
	if err != nil {
		w.t.Fatal(err)
	}

	x.card = card.ID
	w.imports(x.owner, x.card, "checking-july.csv")

	for name, into := range map[string]*types.ID{"Collections to pass on": &x.passThrough, "Odds and ends": &x.unsaid} {
		c, err := w.cats.Create(ctx, now, x.owner, types.OrgScope(x.org), name, categorybus.PassThrough)
		if err != nil {
			w.t.Fatal(err)
		}

		*into = c.ID
	}

	// Only a category made before kinds lacks one; this is one.
	if _, err := w.db.ExecContext(ctx, `UPDATE categories SET kind = '' WHERE id = ?`, x.unsaid.String()); err != nil {
		w.t.Fatal(err)
	}

	txs, err := w.ledger.Transactions(ctx, x.owner, x.account, "2026-07")
	if err != nil {
		w.t.Fatal(err)
	}

	x.offertory, x.electric, x.cafe = txs[4], txs[5], txs[2]

	for id, parts := range map[types.ID][]ledgerbus.Part{
		x.grocery.ID:   {{Amount: amountOf(w.t, "-20.00"), CategoryID: x.groceries, ProjectID: x.project}, {Amount: amountOf(w.t, "-13.99"), CategoryID: x.passThrough}},
		x.offertory.ID: {{Amount: x.offertory.Amount, CategoryID: x.passThrough}},
		x.electric.ID:  {{Amount: x.electric.Amount, CategoryID: x.utilities, Memo: "Hall lights"}},
		x.cafe.ID:      {{Amount: x.cafe.Amount, CategoryID: x.unsaid}},
	} {
		if _, err := w.ledger.SetSplits(ctx, now, x.owner, id, parts); err != nil {
			w.t.Fatal(err)
		}
	}

	return x
}

func amountOf(t *testing.T, s string) money.Amount {
	t.Helper()

	a, err := money.Parse(s)
	if err != nil {
		t.Fatal(err)
	}

	return a
}

// line is how a test names a row: its description, the amount it shows,
// and whether that is part of the charge.
type line struct {
	description string
	amount      string
	part        bool
}

func lines(ex ledgerbus.Explored) []line {
	out := make([]line, len(ex.Rows))
	for i, r := range ex.Rows {
		out[i] = line{r.Transaction.Description, r.Amount.String(), r.Part}
	}

	return out
}

// An organization's list: each choice alone and together, a part's
// category and project judged on one part, and the amount and direction
// judged on the part that was chosen.
func TestExploringAnOrganization(t *testing.T) {
	w := newWorld(t)
	x := newExplored(w)
	ctx := t.Context()
	org := types.OrgScope(x.org)

	cases := []struct {
		name string
		c    ledgerbus.Criteria
		want []line
	}{
		{"all pass-through, part of the groceries", ledgerbus.Criteria{Kind: categorybus.PassThrough}, []line{
			{"PARISH OFFERTORY", "250.00", false}, {"CORNER GROCERY", "-13.99", true},
		}},
		{"pass-through coming in", ledgerbus.Criteria{Kind: categorybus.PassThrough, Direction: rulebus.In}, []line{
			{"PARISH OFFERTORY", "250.00", false},
		}},
		{"expenses", ledgerbus.Criteria{Kind: categorybus.Expense}, []line{
			{"ELECTRIC CO", "-120.00", false}, {"CORNER GROCERY", "-20.00", true},
		}},
		{"a kind not said", ledgerbus.Criteria{KindUnsaid: true}, []line{{"COFFEE CART", "-3.50", false}}},
		{"one category", ledgerbus.Criteria{Category: x.utilities}, []line{{"ELECTRIC CO", "-120.00", false}}},
		{"the project", ledgerbus.Criteria{Project: x.project}, []line{{"CORNER GROCERY", "-20.00", true}}},
		{"groceries in no project: the one part is in it", ledgerbus.Criteria{Category: x.groceries, NoProject: true}, nil},
		{"pass-through in no project", ledgerbus.Criteria{Kind: categorybus.PassThrough, NoProject: true, Max: amountOf(t, "100"), HasMax: true}, []line{
			{"CORNER GROCERY", "-13.99", true},
		}},
		{"a part's memo, in any case", ledgerbus.Criteria{Text: "hall LIGHTS"}, []line{{"ELECTRIC CO", "-120.00", false}}},
		{"one account", ledgerbus.Criteria{Account: x.card, Min: amountOf(t, "100"), HasMin: true}, []line{
			{"ELECTRIC CO", "-120.00", false}, {"PARISH OFFERTORY", "250.00", false}, {"OPENING DEPOSIT", "1000.00", false},
		}},
	}

	for _, c := range cases {
		ex, err := w.ledger.Explore(ctx, x.owner, org, c.c)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		if got := lines(ex); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}

	// Not sorted yet: the checking's deposit and second coffee, and all
	// of the card.
	unsorted, err := w.ledger.Explore(ctx, x.owner, org, ledgerbus.Criteria{Unsorted: true})
	if err != nil || unsorted.Total != 8 {
		t.Errorf("not sorted yet: %d, %v", unsorted.Total, err)
	}

	// Words across both accounts, each row in its own.
	coffee, err := w.ledger.Explore(ctx, x.owner, org, ledgerbus.Criteria{Text: "coffee  cart"})
	if err != nil || coffee.Total != 4 {
		t.Fatalf("the coffee: %d, %v", coffee.Total, err)
	}

	in := map[types.ID]int{}
	for _, r := range coffee.Rows {
		in[r.Account.ID]++
	}

	if in[x.account] != 2 || in[x.card] != 2 {
		t.Errorf("the coffee by account: %v", in)
	}
}

// Everything, a page at a time, newest first; the totals are over every
// row and not the page's; one whole month reads as its statement, all of it.
func TestExploringPagesAndTotals(t *testing.T) {
	w := newWorld(t)
	x := newExplored(w)
	ctx := t.Context()
	org := types.OrgScope(x.org)

	first, err := w.ledger.Explore(ctx, x.owner, org, ledgerbus.Criteria{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}

	if first.Total != 12 || len(first.Rows) != 5 || first.Next != 5 || first.Rows[0].Transaction.Description != "ELECTRIC CO" {
		t.Errorf("the first page: %d of %d, next %d, %v", len(first.Rows), first.Total, first.Next, lines(first))
	}

	want := []ledgerbus.Sum{{Currency: "USD", In: amountOf(t, "2500.00"), Out: amountOf(t, "-321.98")}}
	if !slices.Equal(first.Sums, want) {
		t.Errorf("the totals: %+v, want %+v", first.Sums, want)
	}

	last, err := w.ledger.Explore(ctx, x.owner, org, ledgerbus.Criteria{Limit: 5, Skip: 10})
	if err != nil || len(last.Rows) != 2 || last.Next != 0 || last.Rows[1].Transaction.Description != "OPENING DEPOSIT" {
		t.Errorf("the last page: %v, next %d, %v", lines(last), last.Next, err)
	}

	july, err := w.ledger.Explore(ctx, x.owner, types.AccountScope(x.account), ledgerbus.Criteria{From: date(t, "2026-07-01"), To: date(t, "2026-07-31"), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}

	if len(july.Rows) != 6 || july.Rows[0].Transaction.Description != "OPENING DEPOSIT" || july.Next != 0 {
		t.Errorf("July as its statement reads: %v, next %d", lines(july), july.Next)
	}

	// Part of a month is a search like any other.
	part, err := w.ledger.Explore(ctx, x.owner, types.AccountScope(x.account), ledgerbus.Criteria{From: date(t, "2026-07-01"), To: date(t, "2026-07-10")})
	if err != nil || len(part.Rows) != 5 || part.Rows[0].Transaction.Description != "PARISH OFFERTORY" {
		t.Errorf("July's first ten days: %v, %v", lines(part), err)
	}
}

// An organization's accounts for reference are left out, and said to be,
// until one is chosen.
func TestExploringLeavesAccountsForReferenceOut(t *testing.T) {
	w := newWorld(t)
	x := newExplored(w)
	ctx := t.Context()
	org := types.OrgScope(x.org)

	if _, err := w.ten.SetAccountReference(ctx, now, x.owner, x.card, true); err != nil {
		t.Fatal(err)
	}

	all, err := w.ledger.Explore(ctx, x.owner, org, ledgerbus.Criteria{})
	if err != nil {
		t.Fatal(err)
	}

	if all.Total != 6 || len(all.LeftOut) != 1 || all.LeftOut[0].ID != x.card || len(all.Accounts) != 2 {
		t.Errorf("everything: %d rows, left out %+v, accounts %d", all.Total, all.LeftOut, len(all.Accounts))
	}

	chosen, err := w.ledger.Explore(ctx, x.owner, org, ledgerbus.Criteria{Account: x.card})
	if err != nil || chosen.Total != 6 || chosen.Rows[0].Account.ID != x.card || len(chosen.LeftOut) != 0 {
		t.Errorf("the card, chosen: %d rows, left out %+v, %v", chosen.Total, chosen.LeftOut, err)
	}
}

// A project's list is its parts and only those, from whichever accounts;
// a role on the project sees them and opens none it is given nothing on.
// Nobody without a role finds any scope at all.
func TestExploringAProject(t *testing.T) {
	w := newWorld(t)
	x := newExplored(w)
	ctx := t.Context()
	project := types.ProjectScope(x.project)

	pilgrim := w.user("pilgrim@example.org")
	w.grant(x.owner, project, "pilgrim@example.org", tenancybus.Viewer)

	for _, who := range []types.ID{x.owner, pilgrim} {
		// The project's parts, whatever else is said of projects.
		ex, err := w.ledger.Explore(ctx, who, project, ledgerbus.Criteria{NoProject: true})
		if err != nil {
			t.Fatal(err)
		}

		if got, want := lines(ex), []line{{"CORNER GROCERY", "-20.00", true}}; !slices.Equal(got, want) {
			t.Errorf("the project's parts: %v, want %v", got, want)
		}

		if ex.Rows[0].Readable != (who == x.owner) || len(ex.Accounts) != 1 {
			t.Errorf("readable %v for the owner %v; accounts %+v", ex.Rows[0].Readable, who == x.owner, ex.Accounts)
		}
	}

	if ex, err := w.ledger.Explore(ctx, pilgrim, project, ledgerbus.Criteria{Kind: categorybus.PassThrough}); err != nil || ex.Total != 0 {
		t.Errorf("the rest of the groceries, through the project: %v, %v", lines(ex), err)
	}

	for _, s := range []types.Scope{types.OrgScope(x.org), types.AccountScope(x.account)} {
		if _, err := w.ledger.Explore(ctx, pilgrim, s, ledgerbus.Criteria{}); !errors.Is(err, ledgerbus.ErrNotFound) {
			t.Errorf("the pilgrim, %s: %v", s.Kind, err)
		}
	}

	stranger := w.user("stranger@example.org")
	for _, s := range []types.Scope{types.OrgScope(x.org), types.AccountScope(x.account), project, {Kind: "nothing", ID: x.org}} {
		if _, err := w.ledger.Explore(ctx, stranger, s, ledgerbus.Criteria{}); !errors.Is(err, ledgerbus.ErrNotFound) {
			t.Errorf("a stranger, %s: %v", s.Kind, err)
		}
	}
}

// The marks to check are the month list's, and receipts are counted by
// whatever main hands the ledger.
func TestExploringMarksAndReceipts(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	r := newRuled(w)
	w.imports(r.owner, r.account, "checking-july.csv")

	all, err := w.ledger.Transactions(ctx, r.owner, r.account, "2026-07")
	if err != nil {
		t.Fatal(err)
	}

	byRule := slices.DeleteFunc(slices.Clone(all), func(t ledgerbus.Transaction) bool { return !t.ByRule() })
	if len(byRule) == 0 || len(byRule) == len(all) {
		t.Fatalf("the rules sorted %d of %d", len(byRule), len(all))
	}

	account := types.AccountScope(r.account)

	ex, err := w.ledger.Explore(ctx, r.owner, account, ledgerbus.Criteria{Mark: ledgerbus.MarkRule})
	if err != nil || ex.Total != len(byRule) {
		t.Errorf("sorted by rule: %d, want %d, %v", ex.Total, len(byRule), err)
	}

	// Without a way to count them, nothing has receipts.
	if ex, err := w.ledger.Explore(ctx, r.owner, account, ledgerbus.Criteria{Receipts: ledgerbus.WithReceipts}); err != nil || ex.Total != 0 {
		t.Errorf("with receipts, uncounted: %d, %v", ex.Total, err)
	}

	w.ledger.OnReceipts(func(_ context.Context, ids []types.ID) (map[types.ID]int, error) {
		return map[types.ID]int{all[1].ID: 2}, nil
	})

	with, err := w.ledger.Explore(ctx, r.owner, account, ledgerbus.Criteria{Receipts: ledgerbus.WithReceipts})
	if err != nil || len(with.Rows) != 1 || with.Rows[0].Transaction.ID != all[1].ID {
		t.Errorf("with receipts: %v, %v", lines(with), err)
	}

	without, err := w.ledger.Explore(ctx, r.owner, account, ledgerbus.Criteria{Receipts: ledgerbus.WithoutReceipts})
	if err != nil || without.Total != len(all)-1 {
		t.Errorf("without receipts: %d, %v", without.Total, err)
	}
}

// A pending charge that has since posted is listed, as on the month list,
// and is not in the totals: it is the posted charge that counts.
func TestExploringCountsAPostedChargeOnce(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card, hold := held(t, w, me)

	w.add(me, card, "october.csv", withStatus("2026-10-01,CITY PARKING 0412 SPRINGFIELD,-7.25,Posted"))

	ex, err := w.ledger.Explore(ctx, me, types.AccountScope(card), ledgerbus.Criteria{Text: "city parking"})
	if err != nil {
		t.Fatal(err)
	}

	if ex.Total != 2 || !slices.ContainsFunc(ex.Rows, func(r ledgerbus.Row) bool { return r.Transaction.ID == hold.ID && r.Transaction.Cleared() }) {
		t.Fatalf("the hold and its charge: %v", lines(ex))
	}

	if want := []ledgerbus.Sum{{Currency: "USD", Out: -725}}; !slices.Equal(ex.Sums, want) {
		t.Errorf("the totals: %+v, want %+v", ex.Sums, want)
	}
}
