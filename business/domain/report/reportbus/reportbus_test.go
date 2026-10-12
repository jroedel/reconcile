package reportbus_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/domain/report/stores/reportdb"
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
	reports *reportbus.Business
	ledger  *ledgerbus.Business
	ten     *tenancybus.Business
	cats    *categorybus.Business
	files   *filebus.Business
	users   *userdb.Store
	store   *reportdb.Store
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
		userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ruledb.Init, ledgerdb.Init, reportdb.Init,
	} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
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
	ledger := ledgerbus.NewBusiness(log, ledgerdb.NewStore(db), ten, files, cats, rulebus.NewBusiness(log, ruledb.NewStore(db), ten, cats), nil)

	return &world{
		t: t, ledger: ledger, ten: ten, cats: cats, files: files, users: users,
		reports: reportbus.NewBusiness(ten, ledger, cats, reportdb.NewStore(db)), store: reportdb.NewStore(db),
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

// house is a community's books: its checking and the card it pays from,
// the national office's checking kept for reference, and a card closed in
// 2024 whose history still belongs to the house. July and August are
// imported and sorted, but for one debit nobody has explained.
type house struct {
	owner, org                         types.ID
	checking, card, national, oldCard  types.ID
	statements                         map[string]ledgerbus.Statement
	stipends, donations                types.ID
	groceries, household, travel, fuel types.ID
	cardPayments, personal             types.ID
}

func newHouse(w *world) house {
	w.t.Helper()

	ctx := w.t.Context()
	h := house{owner: w.user("treasurer@example.org"), statements: map[string]ledgerbus.Statement{}}

	org, err := w.ten.CreateOrg(ctx, now, h.owner, "Holy Family House")
	if err != nil {
		w.t.Fatal(err)
	}

	h.org = org.ID

	for name, a := range map[string]struct {
		into *types.ID
		kind string
	}{
		"House checking": {&h.checking, "checking"}, "House card": {&h.card, "card"},
		"National checking": {&h.national, "checking"}, "Old card": {&h.oldCard, "card"},
	} {
		acct, err := w.ten.CreateAccount(ctx, now, h.owner, org.ID, tenancybus.AccountFields{Name: name, Kind: a.kind})
		if err != nil {
			w.t.Fatal(err)
		}

		*a.into = acct.ID
	}

	for name, c := range map[string]struct {
		into *types.ID
		kind categorybus.Kind
	}{
		"Mass stipends": {&h.stipends, categorybus.Income}, "Donations": {&h.donations, categorybus.Income},
		"Groceries": {&h.groceries, categorybus.Expense}, "Household": {&h.household, categorybus.Expense},
		"Travel": {&h.travel, categorybus.Expense}, "Fuel": {&h.fuel, categorybus.Expense},
		"Card payments": {&h.cardPayments, categorybus.Transfer}, "Personal, repaid": {&h.personal, categorybus.PassThrough},
	} {
		cat, err := w.cats.Create(ctx, now, h.owner, org.Scope(), name, c.kind)
		if err != nil {
			w.t.Fatal(err)
		}

		*c.into = cat.ID
	}

	for file, account := range map[string]types.ID{
		"checking-july.csv": h.checking, "checking-august.csv": h.checking, "card-july.csv": h.card,
		"national-july.csv": h.national, "old-card-2024.csv": h.oldCard,
	} {
		h.statements[file] = w.importFile(h.owner, account, file)
	}

	// Travel the national office paid for and took from the house's
	// checking is the house's expense; the national office's own rows are
	// left unsorted, as nobody keeping the house's books would sort them.
	into := map[string]types.ID{
		"MASS STIPENDS": h.stipends, "DONATION": h.donations,
		"GROCERY MART": h.groceries, "GROCERY MART REFUND": h.groceries, "HARDWARE STORE": h.household,
		"TRAVEL REPAID TO NATIONAL": h.travel, "FUEL STATION": h.fuel,
		"CARD PAYMENT": h.cardPayments, "PAYMENT THANK YOU": h.cardPayments, "COFFEE SHOP": h.personal,
	}

	for _, acct := range []types.ID{h.checking, h.card, h.oldCard} {
		for _, month := range []string{"2024-03", "2026-07", "2026-08"} {
			txs, err := w.ledger.Transactions(ctx, h.owner, acct, month)
			if err != nil {
				w.t.Fatal(err)
			}

			for _, tx := range txs {
				cat, ok := into[tx.Description]
				if !ok {
					continue
				}

				if _, err := w.ledger.SetSplits(ctx, now, h.owner, tx.ID, []ledgerbus.Part{{Amount: tx.Amount, CategoryID: cat}}); err != nil {
					w.t.Fatal(err)
				}
			}
		}
	}

	if _, err := w.ten.SetAccountReference(ctx, now, h.owner, h.national, true); err != nil {
		w.t.Fatal(err)
	}

	if _, err := w.ten.SetAccountArchived(ctx, now, h.owner, h.oldCard, true); err != nil {
		w.t.Fatal(err)
	}

	return h
}

func (w *world) importFile(actor, account types.ID, name string) ledgerbus.Statement {
	w.t.Helper()

	ctx := w.t.Context()

	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()

	file, err := w.files.Save(ctx, now, actor, name, f, ledgerbus.MaxFile, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	d, err := w.ledger.Prepare(ctx, actor, account, file.ID, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	st, err := w.ledger.Import(ctx, now, actor, account, file.ID, ledgerbus.Options{Mapping: d.Mapping})
	if err != nil {
		w.t.Fatalf("%s: %v", name, err)
	}

	return st
}

func period(t *testing.T, from, to string) reportbus.Period {
	t.Helper()

	p, err := reportbus.NewPeriod(from, to)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func amounts(s ...string) []money.Amount {
	out := make([]money.Amount, len(s))
	for i, a := range s {
		out[i] = money.MustParse(a)
	}

	return out
}

// July and August of the house: income and expenses by category, the card's
// charges in the months they were made and its payment in neither, the
// national office's money nowhere, a refund lowering groceries, what is not
// sorted yet and how much is, and what should come back to zero.
func TestIncomeAndExpensesByMonth(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()

	r, err := w.reports.IncomeExpenses(ctx, now, h.owner, types.OrgScope(h.org), period(t, "2026-07", "2026-08"), reportbus.IncomeExpensesChoices{})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(r.Months, []string{"2026-07", "2026-08"}) || len(r.Sections) != 1 || r.Partial {
		t.Fatalf("the report: months %v, %d sections, partial %v", r.Months, len(r.Sections), r.Partial)
	}

	if len(r.Accounts) != 3 || slices.ContainsFunc(r.Accounts, func(a tenancybus.Account) bool { return a.ID == h.national }) {
		t.Errorf("the accounts offered: %+v", r.Accounts)
	}

	s := r.Sections[0]

	rows := func(rs []reportbus.Row) map[string][]money.Amount {
		out := map[string][]money.Amount{}
		for _, r := range rs {
			out[r.Name] = r.Months
		}

		return out
	}

	wantRows := func(what string, got map[string][]money.Amount, want map[string][]money.Amount) {
		t.Helper()

		if len(got) != len(want) {
			t.Errorf("%s: %v", what, got)
		}

		for name, months := range want {
			if !slices.Equal(got[name], months) {
				t.Errorf("%s, %s: %v, want %v", what, name, got[name], months)
			}
		}
	}

	wantRows("income", rows(s.Income), map[string][]money.Amount{
		"Mass stipends": amounts("1200.00", "0"),
		"Donations":     amounts("0", "500.00"),
	})

	wantRows("expenses", rows(s.Expenses), map[string][]money.Amount{
		"Groceries": amounts("-150.00", "-180.00"),
		"Household": amounts("-75.00", "0"),
		"Travel":    amounts("-600.00", "0"),
	})

	for name, c := range map[string]struct {
		row  reportbus.Row
		want []money.Amount
	}{
		"income total":   {s.IncomeTotal, amounts("1200.00", "500.00")},
		"expenses total": {s.ExpensesTotal, amounts("-825.00", "-180.00")},
		"net":            {s.Net, amounts("375.00", "320.00")},
		"not sorted yet": {s.Unsorted, amounts("-25.00", "0")},
	} {
		if !slices.Equal(c.row.Months, c.want) {
			t.Errorf("%s: %v, want %v", name, c.row.Months, c.want)
		}
	}

	if s.Net.Total != money.MustParse("695.00") {
		t.Errorf("the net for the period: %s", s.Net.Total)
	}

	// July moved 2,215.00 between the checking and the card, 25.00 of it
	// unsorted: 98.87%, which is not 99 until it is.
	if !slices.Equal(s.Sorted, []int{98, 100}) {
		t.Errorf("sorted: %v", s.Sorted)
	}

	// The coffee is still owed; the card's payment nets to zero, and the
	// old card's 2024 payment, whose checking leg is not here, is still
	// off -- but not in these months.
	if len(s.PassThrough) != 1 || s.PassThroughTotal != money.MustParse("-5.00") ||
		s.Transfers != money.MustParse("40.00") || len(s.TransferMonths) != 0 {
		t.Errorf("what should come back to zero: %+v %s, transfers %s %v", s.PassThrough, s.PassThroughTotal, s.Transfers, s.TransferMonths)
	}

	if !slices.Equal(r.Marks, []reportbus.Mark{reportbus.NotYet, reportbus.NotYet}) {
		t.Errorf("marks before reconciling: %v", r.Marks)
	}

	if r.Before != period(t, "2026-05", "2026-06") || r.After != period(t, "2026-09", "2026-10") {
		t.Errorf("before and after: %+v %+v", r.Before, r.After)
	}
}

// A month is reconciled when every account with money or a statement in it
// is, and not before. The national office's account, and a card closed in
// 2024, hold no month of 2026 back.
func TestAMonthIsReconciledWhenEveryAccountInItIs(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()

	july := func() reportbus.Mark {
		t.Helper()

		r, err := w.reports.IncomeExpenses(ctx, now, h.owner, types.OrgScope(h.org), period(t, "2026-07", "2026-07"), reportbus.IncomeExpensesChoices{})
		if err != nil {
			t.Fatal(err)
		}

		return r.Marks[0]
	}

	from, _ := types.ParseDate("2026-07-01")
	to, _ := types.ParseDate("2026-07-31")

	if _, err := w.ledger.Reconcile(ctx, now, h.owner, h.statements["checking-july.csv"].ID, from, to, ""); err != nil {
		t.Fatal(err)
	}

	if m := july(); m != reportbus.NotYet {
		t.Errorf("with the card's July open: %s", m)
	}

	if _, err := w.ledger.Reconcile(ctx, now, h.owner, h.statements["card-july.csv"].ID, from, to, ""); err != nil {
		t.Fatal(err)
	}

	if m := july(); m != reportbus.Reconciled {
		t.Errorf("with both reconciled: %s", m)
	}
}

// Every month there is reaches back to the card closed in 2024, and shows
// its payment's missing leg in the month it is missing from. Chosen alone,
// the card is the whole report, and the page says only some accounts are
// in it.
func TestEveryMonthAndSomeAccounts(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()

	all, err := w.reports.IncomeExpenses(ctx, now, h.owner, types.OrgScope(h.org), reportbus.Period{All: true}, reportbus.IncomeExpensesChoices{})
	if err != nil {
		t.Fatal(err)
	}

	if len(all.Months) != 30 || all.Months[0] != "2024-03" || all.Months[29] != "2026-08" {
		t.Fatalf("every month: %d, %v", len(all.Months), all.Months)
	}

	s := all.Sections[0]
	if s.ExpensesTotal.Months[0] != money.MustParse("-40.00") || s.Transfers != money.MustParse("40.00") ||
		len(s.TransferMonths) != 1 || s.TransferMonths[0].Month != "2024-03" {
		t.Errorf("the old card's month: expenses %v, transfers %s %v", s.ExpensesTotal.Months[0], s.Transfers, s.TransferMonths)
	}

	if all.Marks[1] != reportbus.Nothing {
		t.Errorf("April 2024, with nothing in it: %s", all.Marks[1])
	}

	card, err := w.reports.IncomeExpenses(ctx, now, h.owner, types.OrgScope(h.org), period(t, "2026-07", "2026-08"),
		reportbus.IncomeExpensesChoices{Accounts: []types.ID{h.card}, PassThrough: reportbus.EachCategory})
	if err != nil {
		t.Fatal(err)
	}

	if !card.Partial || len(card.Chosen) != 1 || len(card.Sections[0].Income) != 0 || len(card.Sections[0].Expenses) != 1 ||
		card.Sections[0].ExpensesTotal.Total != money.MustParse("-75.00") || card.Choices.PassThrough != reportbus.EachCategory {
		t.Errorf("the card alone: %+v", card.Sections[0])
	}

	// Naming only the account for reference names none of the books', so
	// the report is of every one of them rather than of nothing.
	ref, err := w.reports.IncomeExpenses(ctx, now, h.owner, types.OrgScope(h.org), period(t, "2026-07", "2026-07"),
		reportbus.IncomeExpensesChoices{Accounts: []types.ID{h.national}})
	if err != nil || ref.Partial || ref.Sections[0].ExpensesTotal.Total != money.MustParse("-825.00") {
		t.Errorf("the account for reference chosen: %+v %v", ref.Sections, err)
	}
}

// The figure's parts are the ones it adds up, and nobody without a role
// on the organization sees any of it. An account in an organization is not
// books of its own.
func TestTheParts(t *testing.T) {
	w := newWorld(t)
	h := newHouse(w)
	ctx := t.Context()

	q := reportbus.Question{Scope: types.OrgScope(h.org), Period: period(t, "2026-07", "2026-08"), By: []reportbus.Dimension{reportbus.ByKind, reportbus.ByMonth}}

	_, lines, err := w.reports.Parts(ctx, h.owner, q, reportbus.Key{Kind: categorybus.Expense, Month: "2026-07"})
	if err != nil {
		t.Fatal(err)
	}

	var sum money.Amount
	for _, l := range lines {
		sum += l.Split.Amount
	}

	if len(lines) != 3 || sum != money.MustParse("-825.00") {
		t.Errorf("July's expenses: %d parts, %s", len(lines), sum)
	}

	a, err := w.reports.Sum(ctx, h.owner, reportbus.Question{Scope: types.OrgScope(h.org), Period: period(t, "2026-07", "2026-07"), By: []reportbus.Dimension{reportbus.ByAccount}})
	if err != nil || len(a.Cells) != 2 {
		t.Errorf("July by account: %+v %v", a.Cells, err)
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.reports.IncomeExpenses(ctx, now, stranger, types.OrgScope(h.org), reportbus.Period{}, reportbus.IncomeExpensesChoices{}); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.reports.Books(ctx, h.owner, types.AccountScope(h.checking)); !errors.Is(err, reportbus.ErrNotFound) {
		t.Errorf("an organization's account as books of its own: %v", err)
	}
}

func TestPeriods(t *testing.T) {
	day := func(s string) types.Date {
		d, _ := types.ParseDate(s)

		return d
	}

	for _, c := range []struct {
		today    string
		fiscal   int
		key      string
		from, to string
	}{
		{"2026-08-15", 1, reportbus.YearToDate, "2026-01", "2026-07"},
		{"2026-08-15", 7, reportbus.YearToDate, "2026-07", "2026-07"},
		{"2026-07-02", 7, reportbus.YearToDate, "2025-07", "2026-06"},
		{"2026-01-10", 1, reportbus.YearToDate, "2025-01", "2025-12"},
		{"2026-01-10", 1, reportbus.LastMonth, "2025-12", "2025-12"},
		{"2026-03-31", 1, reportbus.LastTwelve, "2025-03", "2026-02"},
	} {
		var got reportbus.Period

		for _, p := range reportbus.Presets(day(c.today), c.fiscal) {
			if p.Key == c.key {
				got = p.Period
			}
		}

		if got.From != c.from || got.To != c.to {
			t.Errorf("%s from %s, starting in %d: %+v", c.key, c.today, c.fiscal, got)
		}
	}

	for _, bad := range [][2]string{{"2026-13", "2026-12"}, {"2026-08", "2026-07"}, {"July", "2026-07"}, {"1900-01", "2026-07"}} {
		if _, err := reportbus.NewPeriod(bad[0], bad[1]); !errors.Is(err, reportbus.ErrPeriod) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}
