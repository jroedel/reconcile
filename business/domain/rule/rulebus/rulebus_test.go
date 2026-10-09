package rulebus_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/rule/stores/ruledb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

var now = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

func TestMatching(t *testing.T) {
	fuel, travel := types.NewID(), types.NewID()

	amazon := rulebus.Rule{ID: types.NewID(), Match: "amazon", Direction: rulebus.Either, CategoryID: fuel}
	aws := rulebus.Rule{ID: types.NewID(), Match: "AMAZON  WEB SERVICES", Direction: rulebus.Out, CategoryID: travel}
	shell := rulebus.Rule{ID: types.NewID(), Match: "shell oil", Direction: rulebus.Out, CategoryID: fuel}
	shellToo := rulebus.Rule{ID: types.NewID(), Match: "SHELL OIL", Direction: rulebus.Either, CategoryID: fuel}
	refund := rulebus.Rule{ID: types.NewID(), Match: "oil change", Direction: rulebus.In, CategoryID: travel}
	oilAgain := rulebus.Rule{ID: types.NewID(), Match: "oil change", Direction: rulebus.Out, CategoryID: fuel}
	wash := rulebus.Rule{ID: types.NewID(), Match: "car wash", Direction: rulebus.Out, CategoryID: fuel}
	bay := rulebus.Rule{ID: types.NewID(), Match: "wash bay", Direction: rulebus.Out, CategoryID: travel}
	rules := []rulebus.Rule{amazon, aws, shell, shellToo, refund, oilAgain, wash, bay}

	out, in := money.MustParse("-12.00"), money.MustParse("12.00")

	for _, c := range []struct {
		description string
		amount      money.Amount
		want        types.ID // the rule's; zero for none
		torn        int
	}{
		{"AMAZON WEB  SERVICES AWS.AMAZON.CO", out, aws.ID, 0}, // the longest wins
		{"Amazon Mktpl*2K4", in, amazon.ID, 0},                 // either way round
		{"amazon web services", in, amazon.ID, 0},              // the longer is money out only
		{"SHELL OIL 57444", out, shell.ID, 0},                  // two that agree
		{"JIFFY OIL CHANGE", in, refund.ID, 0},                 // by direction
		{"PARISH OFFERTORY", in, types.ID{}, 0},                // none
		{"SHELL OIL CHANGE", out, oilAgain.ID, 0},              // ten characters over nine
		{"CAR WASH BAY 3", out, types.ID{}, 2},                 // eight each, disagreeing
	} {
		got, ok, torn := rulebus.Pick(rules, c.description, c.amount)

		switch {
		case c.want.Zero() && ok, !c.want.Zero() && (!ok || got.ID != c.want), len(torn) != c.torn:
			t.Errorf("%s %v: picked %q (%v), torn %d", c.description, c.amount, got.Match, ok, len(torn))
		}
	}
}

func TestPayee(t *testing.T) {
	for in, want := range map[string]string{
		"SHELL OIL 57444 SPRINGFIELD": "SHELL OIL",
		"AMAZON MKTPL*2K4 AMZN.COM":   "AMAZON",
		"SQ *COFFEE CART":             "COFFEE CART",
		"ELECTRIC  CO":                "ELECTRIC CO",
		"POS 0712 CORNER GROCERY":     "CORNER GROCERY",
		"07/12 CORNER GROCERY":        "CORNER GROCERY",
		"POS 4411":                    "POS 4411",
	} {
		if got := rulebus.Payee(in); got != want {
			t.Errorf("Payee(%q) = %q, want %q", in, got, want)
		}
	}

	if got := rulebus.Payee(strings.Repeat("LONG ", 40)); len([]rune(got)) > rulebus.MaxMatch {
		t.Errorf("a payee of %d characters", len([]rune(got)))
	}
}

type world struct {
	t     *testing.T
	rules *rulebus.Business
	ten   *tenancybus.Business
	cats  *categorybus.Business
	users *userdb.Store
	hist  *eventbus.Business
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, categorydb.Init, ruledb.Init} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, ruledb.Expected); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := userdb.NewStore(db)
	ten := tenancybus.NewBusiness(log, tenancydb.NewStore(db), userbus.NewBusiness(log, users))
	cats := categorybus.NewBusiness(log, categorydb.NewStore(db), ten)

	return &world{
		t: t, ten: ten, cats: cats, users: users,
		rules: rulebus.NewBusiness(log, ruledb.NewStore(db), ten, cats),
		hist:  eventbus.NewBusiness(eventdb.NewStore(db)),
	}
}

func (w *world) user(addr string) types.ID {
	w.t.Helper()

	e, _ := types.ParseEmail(addr)
	u := userbus.User{ID: types.NewID(), Email: e, Enabled: true, CreatedAt: now, UpdatedAt: now}

	if err := w.users.CreateUser(w.t.Context(), u); err != nil {
		w.t.Fatal(err)
	}

	return u.ID
}

func (w *world) grant(by types.ID, scope types.Scope, addr string, role tenancybus.Role) {
	w.t.Helper()

	e, _ := types.ParseEmail(addr)
	if _, _, err := w.ten.Grant(w.t.Context(), now, by, scope, e, role); err != nil {
		w.t.Fatal(err)
	}
}

// Saving the same text twice corrects the rule; the fields are checked;
// changing and removing are written in the account's history; and rules
// are a bookkeeper's.
func TestSavingRules(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")

	org, err := w.ten.CreateOrg(ctx, now, me, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	acct, err := w.ten.CreateAccount(ctx, now, me, org.ID, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		t.Fatal(err)
	}

	fuel, _ := w.cats.Create(ctx, now, me, org.Scope(), "Fuel", categorybus.Expense)
	travel, _ := w.cats.Create(ctx, now, me, org.Scope(), "Travel", categorybus.Expense)

	first, err := w.rules.Save(ctx, now, me, acct.ID, rulebus.Fields{Match: " Shell   Oil ", Direction: rulebus.Out, CategoryID: fuel.ID})
	if err != nil || first.Match != "Shell Oil" {
		t.Fatalf("%+v %v", first, err)
	}

	again, err := w.rules.Save(ctx, now.Add(time.Minute), me, acct.ID, rulebus.Fields{Match: "SHELL OIL", Direction: rulebus.Either, CategoryID: travel.ID})
	if err != nil || again.ID != first.ID || again.CategoryID != travel.ID || again.Match != "SHELL OIL" {
		t.Errorf("saving the same text again: %+v %v", again, err)
	}

	rules, _, _ := w.rules.Rules(ctx, me, acct.ID)
	if len(rules) != 1 || rules[0].Direction != rulebus.Either {
		t.Errorf("rules: %+v", rules)
	}

	otherOrg, _ := w.ten.CreateOrg(ctx, now, me, "St. Ann Parish")
	elsewhere, _ := w.cats.Create(ctx, now, me, otherOrg.Scope(), "Fuel", categorybus.Expense)

	for field, f := range map[string]rulebus.Fields{
		"match":     {Match: "ab", Direction: rulebus.Out, CategoryID: fuel.ID},
		"direction": {Match: "abc", Direction: "sideways", CategoryID: fuel.ID},
		"choice":    {Match: "abc", Direction: rulebus.Out},
		"category":  {Match: "abc", Direction: rulebus.Out, CategoryID: elsewhere.ID},
		"project":   {Match: "abc", Direction: rulebus.Out, ProjectID: types.NewID()},
	} {
		_, err := w.rules.Save(ctx, now, me, acct.ID, f)
		if inv, ok := errors.AsType[rulebus.Invalid](err); !ok || inv.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}

	if _, err := w.cats.SetArchived(ctx, now, me, fuel.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := w.rules.Change(ctx, now, me, first.ID, rulebus.Fields{Match: "SHELL", Direction: rulebus.Out, CategoryID: fuel.ID}); err == nil {
		t.Error("a rule into an archived category")
	}

	// Who: a viewer is refused, a stranger finds nothing.
	viewer := w.user("viewer@example.org")
	w.grant(me, org.Scope(), "viewer@example.org", tenancybus.Viewer)
	stranger := w.user("stranger@example.org")

	for who, want := range map[types.ID]error{viewer: rulebus.ErrForbidden, stranger: rulebus.ErrNotFound} {
		if _, _, err := w.rules.Rules(ctx, who, acct.ID); !errors.Is(err, want) {
			t.Errorf("Rules: %v", err)
		}

		if _, err := w.rules.Remove(ctx, now, who, first.ID); !errors.Is(err, want) {
			t.Errorf("Remove: %v", err)
		}
	}

	if _, err := w.rules.Remove(ctx, now.Add(time.Hour), me, first.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := w.rules.Remove(ctx, now, me, first.ID); !errors.Is(err, rulebus.ErrNotFound) {
		t.Errorf("removing it twice: %v", err)
	}

	events, _ := w.hist.Recent(ctx, acct.Scope(), 5)

	var actions []string
	for _, e := range events {
		actions = append(actions, string(e.Action))
	}

	if got := strings.Join(actions, " "); !strings.HasPrefix(got, "rule.removed rule.changed rule.made") {
		t.Errorf("history: %s", got)
	}
}
