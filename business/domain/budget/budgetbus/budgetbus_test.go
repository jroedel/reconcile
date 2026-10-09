package budgetbus_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/budget/stores/budgetdb"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
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

type world struct {
	t       *testing.T
	budgets *budgetbus.Business
	ledger  *ledgerbus.Business
	ten     *tenancybus.Business
	cats    *categorybus.Business
	files   *filebus.Business
	users   *userdb.Store
	history *eventbus.Business
}

func newWorld(t *testing.T) *world {
	t.Helper()

	dir := t.TempDir()

	db, err := sqldb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	for _, init := range []func(context.Context, *sql.DB) error{
		userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ruledb.Init, ledgerdb.Init, budgetdb.Init,
	} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, budgetdb.Expected); err != nil {
		t.Fatal(err)
	}

	bytes, err := filefs.NewStore(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := userdb.NewStore(db)
	ten := tenancybus.NewBusiness(log, tenancydb.NewStore(db), userbus.NewBusiness(log, users))
	files := filebus.NewBusiness(log, filedb.NewStore(db), bytes)
	cats := categorybus.NewBusiness(log, categorydb.NewStore(db), ten)
	ledger := ledgerbus.NewBusiness(log, ledgerdb.NewStore(db), ten, files, cats, rulebus.NewBusiness(log, ruledb.NewStore(db), ten, cats))

	return &world{
		t: t, ledger: ledger, ten: ten, cats: cats, files: files, users: users,
		budgets: budgetbus.NewBusiness(log, budgetdb.NewStore(db), ten, ledger, cats),
		history: eventbus.NewBusiness(eventdb.NewStore(db)),
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

// pilgrimage is a parish with a pilgrimage, July imported and sorted into
// it: fees, a bus deposit and its refund, a hostel, a card payment (a
// transfer), and cash nobody has said anything about.
type pilgrimage struct {
	owner, org, project                    types.ID
	fees, travel, lodging, food, transfers types.ID
}

func newPilgrimage(w *world) pilgrimage {
	w.t.Helper()

	ctx := w.t.Context()
	p := pilgrimage{owner: w.user("treasurer@example.org")}

	org, err := w.ten.CreateOrg(ctx, now, p.owner, "St. Joseph Parish")
	if err != nil {
		w.t.Fatal(err)
	}

	acct, err := w.ten.CreateAccount(ctx, now, p.owner, org.ID, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		w.t.Fatal(err)
	}

	proj, err := w.ten.CreateProject(ctx, now, p.owner, org.ID, tenancybus.ProjectFields{Name: "Fatima pilgrimage"})
	if err != nil {
		w.t.Fatal(err)
	}

	p.org, p.project = org.ID, proj.ID

	for name, c := range map[string]struct {
		into *types.ID
		kind categorybus.Kind
	}{
		"Fees": {&p.fees, categorybus.Income}, "Travel": {&p.travel, categorybus.Expense},
		"Lodging": {&p.lodging, categorybus.Expense}, "Food": {&p.food, categorybus.Expense},
		"Card payments": {&p.transfers, categorybus.Transfer},
	} {
		cat, err := w.cats.Create(ctx, now, p.owner, org.Scope(), name, c.kind)
		if err != nil {
			w.t.Fatal(err)
		}

		*c.into = cat.ID
	}

	f, err := os.Open(filepath.Join("testdata", "pilgrimage.csv"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()

	file, err := w.files.Save(ctx, now, p.owner, "pilgrimage.csv", f, ledgerbus.MaxFile, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	d, err := w.ledger.Prepare(ctx, p.owner, acct.ID, file.ID, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	if _, err := w.ledger.Import(ctx, now, p.owner, acct.ID, file.ID, ledgerbus.Options{Mapping: d.Mapping}); err != nil {
		w.t.Fatal(err)
	}

	txs, err := w.ledger.Transactions(ctx, p.owner, acct.ID, "2026-07")
	if err != nil {
		w.t.Fatal(err)
	}

	into := map[string]types.ID{
		"PILGRIM FEES": p.fees, "BUS DEPOSIT": p.travel, "BUS REFUND": p.travel,
		"CARD PAYMENT": p.transfers, "HOSTEL": p.lodging, "CASH": {},
	}

	for _, tx := range txs {
		if _, err := w.ledger.SetSplits(ctx, now, p.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount, CategoryID: into[tx.Description], ProjectID: p.project}}); err != nil {
			w.t.Fatal(err)
		}
	}

	return p
}

func TestAProjectsBudgetBesideItsBook(t *testing.T) {
	w := newWorld(t)
	p := newPilgrimage(w)
	ctx := t.Context()

	// Nothing set yet: the currency is the money's.
	c, err := w.budgets.Project(ctx, p.owner, p.project)
	if err != nil || c.Set || c.Currency != "USD" || !c.CanSet() {
		t.Fatalf("before: %+v %v", c, err)
	}

	if err := w.budgets.SetProject(ctx, now, p.owner, p.project, "usd", []budgetbus.Entry{
		{CategoryID: p.fees, Kind: categorybus.Income, Amount: money.MustParse("1000.00")},
		{CategoryID: p.travel, Kind: categorybus.Expense, Amount: money.MustParse("500.00")},
		{CategoryID: p.food, Kind: categorybus.Expense, Amount: money.MustParse("200.00")},
		{CategoryID: p.lodging, Kind: categorybus.Expense},
		{Kind: categorybus.Expense, Amount: money.MustParse("1000.00")},
	}); err != nil {
		t.Fatal(err)
	}

	c, err = w.budgets.Project(ctx, p.owner, p.project)
	if err != nil {
		t.Fatal(err)
	}

	m := money.MustParse

	// Fees: 900 of 1,000. Travel: the deposit less the refund, 350 of 500.
	// The hostel is spent with no line; the card payment is a transfer and
	// the cash is not sorted.
	if in := c.Income; len(in.Rows) != 1 || in.Rows[0].Actual != m("900.00") || in.Total.Budget != m("1000.00") || in.Total.Actual != m("900.00") {
		t.Errorf("income: %+v", in)
	}

	ex := c.Expenses
	if len(ex.Rows) != 2 || ex.Rows[0].Name != "Food" || ex.Rows[1].Name != "Travel" || ex.Rows[1].Actual != m("350.00") || ex.Rows[1].Left() != m("150.00") {
		t.Errorf("expense lines: %+v", ex.Rows)
	}

	if ex.Total.Budget != m("1000.00") || ex.Lines != m("700.00") || !ex.Mismatch() || ex.Total.Actual != m("650.00") {
		t.Errorf("expense total: %+v", ex)
	}

	if len(ex.NotInBudget) != 1 || ex.NotInBudget[0].Name != "Lodging" || ex.NotInBudget[0].Actual != m("300.00") {
		t.Errorf("not in the budget: %+v", ex.NotInBudget)
	}

	if c.ExpectedNet() != 0 || c.ActualNet() != m("250.00") || c.Unsorted != m("-20.00") || c.Currency != "USD" {
		t.Errorf("nets %v %v, unsorted %v, %s", c.ExpectedNet(), c.ActualNet(), c.Unsorted, c.Currency)
	}

	events, _ := w.history.Recent(ctx, types.ProjectScope(p.project), 10)

	set := 0
	for _, e := range events {
		if e.Action == budgetbus.Set {
			set++
		}
	}

	if set != 4 {
		t.Errorf("%d lines set in the history", set)
	}

	// Travel up, food taken out: two lines of history.
	if err := w.budgets.SetProject(ctx, now.Add(time.Hour), p.owner, p.project, "USD", []budgetbus.Entry{
		{CategoryID: p.fees, Kind: categorybus.Income, Amount: m("1000.00")},
		{CategoryID: p.travel, Kind: categorybus.Expense, Amount: m("600.00")},
		{Kind: categorybus.Expense, Amount: m("1000.00")},
	}); err != nil {
		t.Fatal(err)
	}

	events, _ = w.history.Recent(ctx, types.ProjectScope(p.project), 2)
	if len(events) != 2 || events[0].Action == events[1].Action {
		t.Errorf("the change's history: %+v", events)
	}

	for _, e := range events {
		if e.Action == budgetbus.Set && (e.Detail["category"] != "Travel" || e.Detail["before"] != "500.00" || e.Detail["amount"] != "600.00") {
			t.Errorf("travel's line: %+v", e.Detail)
		}

		if e.Action == budgetbus.Removed && e.Detail["category"] != "Food" {
			t.Errorf("food's line: %+v", e.Detail)
		}
	}
}

func TestWhatABudgetMayBeAndWhoSetsIt(t *testing.T) {
	w := newWorld(t)
	p := newPilgrimage(w)
	ctx := t.Context()

	if _, err := w.cats.SetArchived(ctx, now, p.owner, p.food, true); err != nil {
		t.Fatal(err)
	}

	for field, c := range map[string]struct {
		currency string
		e        budgetbus.Entry
	}{
		"currency": {"dollars", budgetbus.Entry{Kind: categorybus.Expense, Amount: 100}},
		"amount":   {"USD", budgetbus.Entry{CategoryID: p.travel, Kind: categorybus.Expense, Amount: -100}},
		"category": {"USD", budgetbus.Entry{CategoryID: p.transfers, Kind: categorybus.Transfer, Amount: 100}},
	} {
		err := w.budgets.SetProject(ctx, now, p.owner, p.project, c.currency, []budgetbus.Entry{c.e})
		if inv, ok := errors.AsType[budgetbus.Invalid](err); !ok || inv.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}

	for name, e := range map[string]budgetbus.Entry{
		"an archived category":     {CategoryID: p.food, Kind: categorybus.Expense, Amount: 100},
		"income into an expense":   {CategoryID: p.travel, Kind: categorybus.Income, Amount: 100},
		"somebody else's category": {CategoryID: types.NewID(), Kind: categorybus.Expense, Amount: 100},
	} {
		if err := w.budgets.SetProject(ctx, now, p.owner, p.project, "USD", []budgetbus.Entry{e}); err == nil {
			t.Errorf("%s was budgeted", name)
		}
	}

	f, err := w.budgets.ProjectForm(ctx, p.owner, p.project)
	if err != nil || len(f.IncomeCategories) != 1 || len(f.ExpenseCategories) != 2 {
		t.Errorf("the form: %d income, %d expense categories: %v", len(f.IncomeCategories), len(f.ExpenseCategories), err)
	}

	// A bookkeeper of the project reads it and cannot set it; a stranger
	// finds nothing.
	clerk := w.user("clerk@example.org")
	w.grant(p.owner, types.ProjectScope(p.project), "clerk@example.org", tenancybus.Bookkeeper)
	stranger := w.user("stranger@example.org")

	if c, err := w.budgets.Project(ctx, clerk, p.project); err != nil || c.CanSet() {
		t.Errorf("the clerk reading: %v", err)
	}

	for who, want := range map[types.ID]error{clerk: budgetbus.ErrForbidden, stranger: budgetbus.ErrNotFound} {
		if err := w.budgets.SetProject(ctx, now, who, p.project, "USD", nil); !errors.Is(err, want) {
			t.Errorf("SetProject: %v, want %v", err, want)
		}

		if _, err := w.budgets.ProjectForm(ctx, who, p.project); !errors.Is(err, want) {
			t.Errorf("ProjectForm: %v, want %v", err, want)
		}
	}

	if _, err := w.budgets.Project(ctx, stranger, p.project); !errors.Is(err, budgetbus.ErrNotFound) {
		t.Errorf("a stranger reading: %v", err)
	}
}
