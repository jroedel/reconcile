package ledgerbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// ruled is an organization's account with rules made before its first
// statement arrives, and a project that ends in the middle of July.
type ruled struct {
	owner, account, project types.ID
	groceries, utilities    types.ID
	coffee, electric, gift  rulebus.Rule
}

func newRuled(w *world) ruled {
	w.t.Helper()

	ctx := w.t.Context()
	r := ruled{owner: w.user("treasurer@example.org")}

	org, err := w.ten.CreateOrg(ctx, now, r.owner, "St. Joseph Parish")
	if err != nil {
		w.t.Fatal(err)
	}

	acct, err := w.ten.CreateAccount(ctx, now, r.owner, org.ID, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		w.t.Fatal(err)
	}

	p, err := w.ten.CreateProject(ctx, now, r.owner, org.ID, tenancybus.ProjectFields{Name: "Roof", StartsOn: "2026-07-01", EndsOn: "2026-07-15"})
	if err != nil {
		w.t.Fatal(err)
	}

	r.account, r.project = acct.ID, p.ID

	for name, into := range map[string]*types.ID{"Utilities": &r.utilities, "Groceries": &r.groceries} {
		c, err := w.cats.Create(ctx, now, r.owner, org.Scope(), name, categorybus.Expense)
		if err != nil {
			w.t.Fatal(err)
		}

		*into = c.ID
	}

	save := func(f rulebus.Fields) rulebus.Rule {
		rule, err := w.rules.Save(ctx, now, r.owner, r.account, f)
		if err != nil {
			w.t.Fatalf("saving %q: %v", f.Match, err)
		}

		return rule
	}

	r.coffee = save(rulebus.Fields{Match: "coffee  cart", Direction: rulebus.Out, CategoryID: r.groceries})
	r.electric = save(rulebus.Fields{Match: "Electric", Direction: rulebus.Out, CategoryID: r.utilities})
	r.gift = save(rulebus.Fields{Match: "PARISH OFFERTORY", Direction: rulebus.In, ProjectID: r.project})

	// Two of the same length that disagree about the grocery, and one
	// that meets the deposit only the wrong way round.
	save(rulebus.Fields{Match: "corner", Direction: rulebus.Either, CategoryID: r.utilities})
	save(rulebus.Fields{Match: "grocer", Direction: rulebus.Out, CategoryID: r.groceries})
	save(rulebus.Fields{Match: "opening deposit", Direction: rulebus.Out, CategoryID: r.groceries})

	return r
}

func byDescription(w *world, actor, account types.ID, month string) map[string][]ledgerbus.Transaction {
	w.t.Helper()

	txs, err := w.ledger.Transactions(w.t.Context(), actor, account, month)
	if err != nil {
		w.t.Fatal(err)
	}

	out := map[string][]ledgerbus.Transaction{}
	for _, t := range txs {
		out[t.Description] = append(out[t.Description], t)
	}

	return out
}

func TestRulesSortAStatementAsItArrives(t *testing.T) {
	w := newWorld(t)
	r := newRuled(w)
	ctx := t.Context()

	if d, _ := w.prepare(r.owner, r.account, "checking-july.csv"); d.Statement.ByRule != 4 {
		t.Errorf("the preview says %d will be sorted by rule", d.Statement.ByRule)
	}

	if st := w.imports(r.owner, r.account, "checking-july.csv"); st.ByRule != 4 {
		t.Errorf("%d sorted by rule, want both coffees, the electric bill and the offertory", st.ByRule)
	}

	july := byDescription(w, r.owner, r.account, "2026-07")

	for _, c := range july["COFFEE CART"] {
		if sp := c.Splits[0]; sp.CategoryID != r.groceries || sp.RuleID != r.coffee.ID || !c.ByRule() {
			t.Errorf("a coffee: %+v", sp)
		}
	}

	if sp := july["PARISH OFFERTORY"][0].Splits[0]; sp.ProjectID != r.project || !sp.CategoryID.Zero() || sp.RuleID != r.gift.ID {
		t.Errorf("the offertory: %+v", sp)
	}

	for _, name := range []string{"CORNER GROCERY", "OPENING DEPOSIT"} {
		if tx := july[name][0]; tx.ByRule() || tx.Sorted() {
			t.Errorf("%s was sorted: %+v", name, tx.Splits)
		}
	}

	// The history says so: the account's import line, and the project's.
	events, _ := w.history.Recent(ctx, types.AccountScope(r.account), 1)
	if events[0].Action != ledgerbus.StatementImported || events[0].Detail["byrule"] != "4" {
		t.Errorf("the account's history: %+v", events[0])
	}

	events, _ = w.history.Recent(ctx, types.ProjectScope(r.project), 1)
	if len(events) == 0 || events[0].Action != ledgerbus.SplitAdded || events[0].Detail["amount"] != "250.00" {
		t.Errorf("the project's history: %+v", events)
	}

	months, _ := w.ledger.Months(ctx, r.owner, r.account)
	if months[0].ByRule != 4 {
		t.Errorf("the month counts %d by rule", months[0].ByRule)
	}

	// The rules page: the grocery is torn between two rules.
	book, err := w.ledger.Rulebook(ctx, now, r.owner, r.account)
	if err != nil {
		t.Fatal(err)
	}

	if len(book.Torn) != 1 || book.Torn[0].Transaction.Description != "CORNER GROCERY" || len(book.Torn[0].Rules) != 2 || book.Waiting != 0 {
		t.Errorf("torn: %+v, waiting %d", book.Torn, book.Waiting)
	}

	uses := map[types.ID]ledgerbus.RuleUse{}
	for _, u := range book.Rules {
		uses[u.Rule.ID] = u
	}

	// The roof project ended on the 15th, and today is in August.
	if uses[r.coffee.ID].Parts != 2 || uses[r.gift.ID].Problem != ledgerbus.RuleEnded {
		t.Errorf("uses: %+v", book.Rules)
	}

	// A person who saves a coffee has said: the mark goes.
	coffee := july["COFFEE CART"][0]
	if _, err := w.ledger.SetSplits(ctx, now, r.owner, coffee.ID, []ledgerbus.Part{{Amount: coffee.Amount, CategoryID: r.groceries}}); err != nil {
		t.Fatal(err)
	}

	if ed, _ := w.ledger.Transaction(ctx, r.owner, coffee.ID); ed.Transaction.ByRule() {
		t.Error("a person's sorting still says a rule did it")
	}

	if months, _ := w.ledger.Months(ctx, r.owner, r.account); months[0].ByRule != 3 {
		t.Errorf("the month counts %d by rule", months[0].ByRule)
	}
}

// "Sort what is not sorted yet" leaves alone a reconciled period, a
// transaction a project's dates are past, and one an archived category
// would sort.
func TestSortingWhatIsNotSortedYet(t *testing.T) {
	w := newWorld(t)
	r := newRuled(w)
	ctx := t.Context()

	julySt := w.imports(r.owner, r.account, "checking-july.csv")

	if _, err := w.ledger.Reconcile(ctx, now, r.owner, julySt.ID, date(t, "2026-07-01"), date(t, "2026-07-31"), ""); err != nil {
		t.Fatal(err)
	}

	// The deposit's rule, put right, would sort it -- but July is
	// reconciled.
	if _, err := w.rules.Save(ctx, now, r.owner, r.account, rulebus.Fields{Match: "Opening Deposit", Direction: rulebus.In, CategoryID: r.groceries}); err != nil {
		t.Fatal(err)
	}

	if n, err := w.ledger.SortUnsorted(ctx, now, r.owner, r.account); err != nil || n != 0 {
		t.Errorf("sorted %d in a reconciled month: %v", n, err)
	}

	// August's offertory is after the roof project ended; the flowers have
	// no rule yet.
	if st := w.imports(r.owner, r.account, "checking-july-august.csv"); st.ByRule != 0 || st.Added != 2 {
		t.Errorf("August: %+v", st)
	}

	if _, err := w.rules.Save(ctx, now, r.owner, r.account, rulebus.Fields{Match: "flowers", Direction: rulebus.Out, CategoryID: r.utilities}); err != nil {
		t.Fatal(err)
	}

	if book, _ := w.ledger.Rulebook(ctx, now, r.owner, r.account); book.Waiting != 1 {
		t.Errorf("%d waiting", book.Waiting)
	}

	// An hour on, after the imports, so that it is the newest line of the
	// history.
	if n, err := w.ledger.SortUnsorted(ctx, now.Add(time.Hour), r.owner, r.account); err != nil || n != 1 {
		t.Errorf("sorted %d: %v", n, err)
	}

	aug := byDescription(w, r.owner, r.account, "2026-08")
	if sp := aug["FLOWERS"][0].Splits[0]; sp.CategoryID != r.utilities || sp.RuleID.Zero() {
		t.Errorf("the flowers: %+v", sp)
	}

	if aug["PARISH OFFERTORY"][0].Sorted() || aug["PARISH OFFERTORY"][0].ByRule() {
		t.Error("an ended project took the August offertory")
	}

	events, _ := w.history.Recent(ctx, types.AccountScope(r.account), 1)
	if events[0].Action != ledgerbus.RulesApplied || events[0].Detail["count"] != "1" {
		t.Errorf("history: %+v", events[0])
	}

	// An archived category sorts nothing, and its rule says why.
	if _, err := w.cats.SetArchived(ctx, now, r.owner, r.utilities, true); err != nil {
		t.Fatal(err)
	}

	book, _ := w.ledger.Rulebook(ctx, now, r.owner, r.account)
	for _, u := range book.Rules {
		if u.Rule.ID == r.electric.ID && u.Problem != ledgerbus.RuleCategory {
			t.Errorf("the electric rule's problem: %q", u.Problem)
		}
	}
}

// Rules are a bookkeeper's, and a bookkeeper who cannot use a project
// cannot have a rule put money into it on their behalf.
func TestWhoMayApplyRules(t *testing.T) {
	w := newWorld(t)
	r := newRuled(w)
	ctx := t.Context()

	viewer := w.user("viewer@example.org")
	w.grant(r.owner, types.AccountScope(r.account), "viewer@example.org", tenancybus.Viewer)

	stranger := w.user("stranger@example.org")

	for who, want := range map[types.ID]error{viewer: ledgerbus.ErrForbidden, stranger: ledgerbus.ErrNotFound} {
		if _, err := w.ledger.SortUnsorted(ctx, now, who, r.account); !errors.Is(err, want) {
			t.Errorf("SortUnsorted: %v, want %v", err, want)
		}

		if _, err := w.ledger.Rulebook(ctx, now, who, r.account); !errors.Is(err, want) {
			t.Errorf("Rulebook: %v, want %v", err, want)
		}
	}

	clerk := w.user("clerk@example.org")
	w.grant(r.owner, types.AccountScope(r.account), "clerk@example.org", tenancybus.Bookkeeper)

	w.imports(clerk, r.account, "checking-july.csv")

	if july := byDescription(w, r.owner, r.account, "2026-07"); july["PARISH OFFERTORY"][0].Sorted() || july["PARISH OFFERTORY"][0].ByRule() {
		t.Error("the clerk's import put the offertory into a project they cannot use")
	}

	book, err := w.ledger.Rulebook(ctx, now, clerk, r.account)
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range book.Rules {
		if u.Rule.ID == r.gift.ID && u.Problem != ledgerbus.RuleProject {
			t.Errorf("the clerk sees the offertory rule's problem as %q", u.Problem)
		}
	}
}
