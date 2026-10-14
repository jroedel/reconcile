package ledgerbus_test

import (
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

// Hunting an amount: by words, by amount without its sign, by direction
// and by dates, newest first, across the accounts the treasurer may read
// and never one they may not, even with the same statement in it.
func TestFindingTransactions(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	other := w.user("other@example.org")

	acct := func(owner types.ID, name string) types.ID {
		a, err := w.ten.CreateAccount(ctx, now, owner, types.ID{}, tenancybus.AccountFields{Name: name, Kind: "checking"})
		if err != nil {
			t.Fatal(err)
		}

		return a.ID
	}

	mine, theirs := acct(me, "Checking"), acct(other, "Theirs")
	w.imports(me, mine, "checking-july.csv")
	w.imports(other, theirs, "checking-july.csv")

	amount := func(s string) money.Amount {
		a, err := money.Parse(s)
		if err != nil {
			t.Fatal(err)
		}

		return a
	}

	date := func(s string) types.Date {
		d, err := types.ParseDate(s)
		if err != nil {
			t.Fatal(err)
		}

		return d
	}

	cases := []struct {
		name  string
		q     ledgerbus.Query
		want  []string
		more  int
		actor types.ID
	}{
		{"every one", ledgerbus.Query{}, []string{"ELECTRIC CO", "PARISH OFFERTORY", "CORNER GROCERY", "COFFEE CART", "COFFEE CART", "OPENING DEPOSIT"}, 0, me},
		{"words", ledgerbus.Query{Criteria: ledgerbus.Criteria{Text: "coffee  cart"}}, []string{"COFFEE CART", "COFFEE CART"}, 0, me},
		{"an amount, either way round", ledgerbus.Query{Criteria: ledgerbus.Criteria{Min: amount("120"), Max: amount("120"), HasMin: true, HasMax: true}}, []string{"ELECTRIC CO"}, 0, me},
		{"at least", ledgerbus.Query{Criteria: ledgerbus.Criteria{Min: amount("250"), HasMin: true}}, []string{"PARISH OFFERTORY", "OPENING DEPOSIT"}, 0, me},
		{"money in", ledgerbus.Query{Criteria: ledgerbus.Criteria{Direction: rulebus.In}}, []string{"PARISH OFFERTORY", "OPENING DEPOSIT"}, 0, me},
		{"one day", ledgerbus.Query{Criteria: ledgerbus.Criteria{From: date("2026-07-03"), To: date("2026-07-03")}}, []string{"CORNER GROCERY", "COFFEE CART", "COFFEE CART"}, 0, me},
		{"a few", ledgerbus.Query{Criteria: ledgerbus.Criteria{Limit: 2}}, []string{"ELECTRIC CO", "PARISH OFFERTORY"}, 4, me},
		{"somebody else's account", ledgerbus.Query{Accounts: []types.ID{theirs}}, nil, 0, me},
		{"a stranger", ledgerbus.Query{Accounts: []types.ID{mine}}, nil, 0, w.user("stranger@example.org")},
	}

	for _, c := range cases {
		got, err := w.ledger.Find(ctx, c.actor, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		var names []string
		for _, f := range got.Found {
			names = append(names, f.Transaction.Description)

			if f.Account.ID != mine {
				t.Errorf("%s: found in %s", c.name, f.Account.Name)
			}
		}

		if len(names) != len(c.want) || got.More != c.more {
			t.Errorf("%s: %q and %d more, want %q and %d", c.name, names, got.More, c.want, c.more)

			continue
		}

		for i := range names {
			if names[i] != c.want[i] {
				t.Errorf("%s: %q, want %q", c.name, names, c.want)

				break
			}
		}
	}
}

// Trying a rule says what it would sort, what a longer rule sorts instead,
// where it would disagree with a rule as long, and what is sorted already,
// and saves nothing. Trying a rule's own text tries a correction of it.
func TestTryingARule(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	r := newRuled(w)

	// At import the rules sort the coffee and the electric bill, put the
	// offertory in the project, and leave the deposit and the grocery.
	w.imports(r.owner, r.account, "checking-july.csv")

	try := func(f rulebus.Fields) ledgerbus.Trial {
		t.Helper()

		trial, err := w.ledger.TryRule(ctx, now, r.owner, r.account, f)
		if err != nil {
			t.Fatalf("trying %q: %v", f.Match, err)
		}

		return trial
	}

	if got := try(rulebus.Fields{Match: "deposit", Direction: rulebus.In, CategoryID: r.groceries}); got.WouldCount != 1 || got.Would[0].Description != "OPENING DEPOSIT" || got.AlreadyCount != 0 {
		t.Errorf("the deposit: %+v", got)
	}

	if got := try(rulebus.Fields{Match: "coffee", Direction: rulebus.Out}); got.WouldCount != 0 || got.AlreadyCount != 2 {
		t.Errorf("the coffee, sorted already: would %d, already %d", got.WouldCount, got.AlreadyCount)
	}

	// Longer than both rules that disagree about the grocery, so it wins.
	if got := try(rulebus.Fields{Match: "corner grocery", Direction: rulebus.Out, CategoryID: r.groceries}); got.WouldCount != 1 || got.TornCount != 0 {
		t.Errorf("a longer rule: would %d, torn %d", got.WouldCount, got.TornCount)
	}

	// "corner" again, for groceries, is a correction of the rule there:
	// it then agrees with "grocer", and the two sort the grocery.
	if got := try(rulebus.Fields{Match: "Corner", Direction: rulebus.Either, CategoryID: r.groceries}); got.WouldCount != 1 {
		t.Errorf("a corrected rule: would %d, torn %d", got.WouldCount, got.TornCount)
	}

	// With nothing chosen it agrees with nobody, and says with whom not.
	got := try(rulebus.Fields{Match: "grocer", Direction: rulebus.Out})
	if got.TornCount != 1 || len(got.With) != 1 || got.With[0].Match != "corner" {
		t.Errorf("a rule that disagrees: torn %d, with %+v", got.TornCount, got.With)
	}

	if _, err := w.ledger.TryRule(ctx, now, r.owner, r.account, rulebus.Fields{Match: "co"}); !isInvalid(err) {
		t.Errorf("text too short: %v", err)
	}

	// Nothing was saved.
	if book, err := w.ledger.Rulebook(ctx, now, r.owner, r.account); err != nil || len(book.Rules) != 6 {
		t.Errorf("the rules after trying: %d, %v", len(book.Rules), err)
	}

	// Who may try: who may make rules.
	viewer := w.user("viewer@example.org")
	w.grant(r.owner, types.AccountScope(r.account), "viewer@example.org", tenancybus.Viewer)

	if _, err := w.ledger.TryRule(ctx, now, viewer, r.account, rulebus.Fields{Match: "deposit"}); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer trying: %v", err)
	}

	if _, err := w.ledger.TryRule(ctx, now, w.user("stranger@example.org"), r.account, rulebus.Fields{Match: "deposit"}); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger trying: %v", err)
	}
}

func isInvalid(err error) bool {
	_, ok := errors.AsType[rulebus.Invalid](err)

	return ok
}

// Find takes the web's search's choices: a kind, a category or none, and a
// project, judged on one part, with the parts and their amount; one
// organization's accounts or all; a page at a time with the totals of
// every page; and accounts for reference only when named.
func TestFindingWithTheSearchsChoices(t *testing.T) {
	w := newWorld(t)
	x := newExplored(w)
	ctx := t.Context()

	// An account of the treasurer's own, outside the parish.
	own, err := w.ten.CreateAccount(ctx, now, x.owner, types.ID{}, tenancybus.AccountFields{Name: "Own checking", Kind: "checking"})
	if err != nil {
		t.Fatal(err)
	}

	w.imports(x.owner, own.ID, "checking-july.csv")

	find := func(q ledgerbus.Query) ledgerbus.Finding {
		t.Helper()

		f, err := w.ledger.Find(ctx, x.owner, q)
		if err != nil {
			t.Fatal(err)
		}

		return f
	}

	pass := find(ledgerbus.Query{Criteria: ledgerbus.Criteria{Kind: categorybus.PassThrough}})
	if pass.Total != 2 || pass.Found[0].Transaction.Description != "PARISH OFFERTORY" || pass.Found[0].Part {
		t.Fatalf("all pass-through: %+v", pass)
	}

	if g := pass.Found[1]; g.Transaction.ID != x.grocery.ID || !g.Part || g.Amount != amountOf(t, "-13.99") || len(g.Parts) != 1 || g.Parts[0].CategoryID != x.passThrough {
		t.Errorf("the grocery's pass-through part: %+v", g)
	}

	want := []ledgerbus.Sum{{Currency: "USD", In: amountOf(t, "250.00"), Out: amountOf(t, "-13.99")}}
	if !slices.Equal(pass.Sums, want) {
		t.Errorf("all pass-through's totals: %+v, want %+v", pass.Sums, want)
	}

	if got := find(ledgerbus.Query{Criteria: ledgerbus.Criteria{Project: x.project}}); got.Total != 1 || got.Found[0].Amount != amountOf(t, "-20.00") {
		t.Errorf("the project: %+v", got)
	}

	// Not sorted yet: two of the parish's checking, all of its card's and
	// all of the treasurer's own.
	if got := find(ledgerbus.Query{Criteria: ledgerbus.Criteria{Unsorted: true}}); got.Total != 14 {
		t.Errorf("not sorted yet: %d", got.Total)
	}

	parish := find(ledgerbus.Query{Org: x.org, Criteria: ledgerbus.Criteria{Unsorted: true}})
	if parish.Total != 8 || slices.ContainsFunc(parish.Found, func(f ledgerbus.Found) bool { return f.Account.ID == own.ID }) {
		t.Errorf("the parish's not sorted yet: %d", parish.Total)
	}

	page := find(ledgerbus.Query{Org: x.org, Criteria: ledgerbus.Criteria{Limit: 5, Skip: 5}})
	if page.Total != 12 || len(page.Found) != 5 || page.Next != 10 || page.More != 2 || len(page.Sums) != 1 {
		t.Errorf("the second page: %d of %d, next %d, %d more", len(page.Found), page.Total, page.Next, page.More)
	}

	if last := find(ledgerbus.Query{Org: x.org, Criteria: ledgerbus.Criteria{Limit: 5, Skip: 10}}); len(last.Found) != 2 || last.Next != 0 || last.More != 0 {
		t.Errorf("the last page: %d, next %d, %d more", len(last.Found), last.Next, last.More)
	}

	if _, err := w.ledger.Find(ctx, w.user("stranger@example.org"), ledgerbus.Query{Org: x.org}); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger's organization: %v", err)
	}

	if _, err := w.ten.SetAccountReference(ctx, now, x.owner, x.card, true); err != nil {
		t.Fatal(err)
	}

	for name, q := range map[string]ledgerbus.Query{"everything": {}, "the parish": {Org: x.org}} {
		if got := find(q); slices.ContainsFunc(got.Found, func(f ledgerbus.Found) bool { return f.Account.ID == x.card }) || len(got.LeftOut) != 1 || got.LeftOut[0].ID != x.card {
			t.Errorf("%s, the card for reference: left out %+v", name, got.LeftOut)
		}
	}

	if got := find(ledgerbus.Query{Criteria: ledgerbus.Criteria{Account: x.card}}); got.Total != 6 || len(got.LeftOut) != 0 {
		t.Errorf("the card, named: %d, left out %+v", got.Total, got.LeftOut)
	}
}
