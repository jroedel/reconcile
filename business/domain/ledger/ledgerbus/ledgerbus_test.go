package ledgerbus_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
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

// Every statement in testdata/ is invented.

var now = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

type world struct {
	t       *testing.T
	ledger  *ledgerbus.Business
	ten     *tenancybus.Business
	files   *filebus.Business
	users   *userdb.Store
	history *eventbus.Business
	cats    *categorybus.Business
	rules   *rulebus.Business
	db      *sql.DB
	tick    int
}

func newWorld(t *testing.T) *world {
	t.Helper()

	dir := t.TempDir()

	db, err := sqldb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ruledb.Init, ledgerdb.Init} {
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
	rules := rulebus.NewBusiness(log, ruledb.NewStore(db), ten, cats)

	return &world{
		t:       t,
		ledger:  ledgerbus.NewBusiness(log, ledgerdb.NewStore(db), ten, files, cats, rules),
		rules:   rules,
		ten:     ten,
		files:   files,
		users:   users,
		history: eventbus.NewBusiness(eventdb.NewStore(db)),
		cats:    cats,
		db:      db,
	}
}

func (w *world) user(addr string) types.ID {
	w.t.Helper()

	e, err := types.ParseEmail(addr)
	if err != nil {
		w.t.Fatal(err)
	}

	u := userbus.User{ID: types.NewID(), Email: e, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := w.users.CreateUser(w.t.Context(), u); err != nil {
		w.t.Fatal(err)
	}

	return u.ID
}

func (w *world) account(owner types.ID, kind string) types.ID {
	w.t.Helper()

	a, err := w.ten.CreateAccount(w.t.Context(), now, owner, types.ID{}, tenancybus.AccountFields{Name: "Parish checking", Kind: kind})
	if err != nil {
		w.t.Fatal(err)
	}

	return a.ID
}

// upload saves a fixture as the actor's upload.
func (w *world) upload(actor types.ID, name string) types.ID {
	w.t.Helper()

	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()

	saved, err := w.files.Save(w.t.Context(), now, actor, name, f, ledgerbus.MaxFile, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	return saved.ID
}

// prepare reads a fixture with what the page would choose by itself.
func (w *world) prepare(actor, account types.ID, name string) (ledgerbus.Draft, types.ID) {
	w.t.Helper()

	file := w.upload(actor, name)

	d, err := w.ledger.Prepare(w.t.Context(), actor, account, file, nil)
	if err != nil {
		w.t.Fatalf("Prepare %s: %v", name, err)
	}

	return d, file
}

// imports prepares a fixture and imports it as prepared.
func (w *world) imports(actor, account types.ID, name string) ledgerbus.Statement {
	w.t.Helper()

	d, file := w.prepare(actor, account, name)

	// A minute later each time, so that the history has an order.
	w.tick++

	st, err := w.ledger.Import(w.t.Context(), now.Add(time.Duration(w.tick)*time.Minute), actor, account, file,
		ledgerbus.Options{Mapping: d.Mapping, Opening: d.Opening, Closing: d.Closing})
	if err != nil {
		w.t.Fatalf("Import %s: %v (check %+v)", name, err, d.Check)
	}

	return st
}

func (w *world) count(actor, account types.ID) int {
	w.t.Helper()

	months, err := w.ledger.Months(w.t.Context(), actor, account)
	if err != nil {
		w.t.Fatal(err)
	}

	n := 0
	for _, m := range months {
		n += m.Count
	}

	return n
}

func TestACSVWithBalancesIsCheckedAndImported(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	d, _ := w.prepare(me, acct, "checking-july.csv")

	if d.Format != ledgerbus.CSV || d.Remembered || d.Mapping.Balance != "Balance" {
		t.Errorf("draft: format %s, remembered %v, mapping %+v", d.Format, d.Remembered, d.Mapping)
	}

	if d.Check.Method != ledgerbus.ByBalances || !d.Check.OK || !d.Ready() {
		t.Fatalf("check = %+v", d.Check)
	}

	// The preview counts, and stores nothing.
	if d.Statement.Added != 6 || d.Statement.Already != 0 {
		t.Errorf("preview: %d new, %d already", d.Statement.Added, d.Statement.Already)
	}

	if n := w.count(me, acct); n != 0 {
		t.Fatalf("the preview stored %d transactions", n)
	}

	st := w.imports(me, acct, "checking-july.csv")

	// The two identical coffees on one day are two transactions.
	if st.Added != 6 || st.Start.String() != "2026-07-01" || st.End.String() != "2026-07-28" || st.Checked != ledgerbus.ByBalances {
		t.Errorf("statement = %+v", st)
	}

	txs, err := w.ledger.Transactions(t.Context(), me, acct, "2026-07")
	if err != nil || len(txs) != 6 {
		t.Fatalf("July: %d, %v", len(txs), err)
	}

	if txs[0].Description != "OPENING DEPOSIT" || txs[5].Amount != money.MustParse("-120.00") || !txs[5].HasBalance {
		t.Errorf("transactions: %+v", txs)
	}

	// The history says it.
	events, _ := w.history.Recent(t.Context(), types.AccountScope(acct), 10)
	if events[0].Action != ledgerbus.StatementImported || events[0].Detail["added"] != "6" || events[0].Detail["name"] != "checking-july.csv" {
		t.Errorf("history: %+v", events[0])
	}
}

// An overlapping export adds only what is new: the overlap is recognised
// even where the bank changed the case and spacing of a description.
func TestAnOverlappingExportAddsOnlyWhatIsNew(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.imports(me, acct, "checking-july.csv")

	d, _ := w.prepare(me, acct, "checking-july-august.csv")
	if !d.Remembered {
		t.Error("the columns were not remembered for a file with the same header")
	}

	st := w.imports(me, acct, "checking-july-august.csv")
	if st.Added != 2 || st.Already != 2 {
		t.Errorf("%d new, %d already; want 2 and 2", st.Added, st.Already)
	}

	if n := w.count(me, acct); n != 8 {
		t.Errorf("%d transactions, want 8", n)
	}
}

func TestTheSameFileTwiceIsRefused(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	first := w.imports(me, acct, "checking-july.csv")

	d, file := w.prepare(me, acct, "checking-july.csv")
	if !d.HasEarlier || d.Earlier.ID != first.ID || d.Ready() {
		t.Errorf("draft: earlier %v, ready %v", d.HasEarlier, d.Ready())
	}

	if _, err := w.ledger.Import(t.Context(), now, me, acct, file, ledgerbus.Options{Mapping: d.Mapping}); !errors.Is(err, ledgerbus.ErrSameFile) {
		t.Errorf("err = %v", err)
	}
}

// A statement missing a row does not balance, says where, and imports
// nothing.
func TestAStatementThatDoesNotBalanceImportsNothing(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	d, file := w.prepare(me, acct, "checking-dropped-row.csv")

	want := ledgerbus.Check{Method: ledgerbus.ByBalances, Line: 3, Expected: money.MustParse("996.50"), Stated: money.MustParse("962.51")}
	if d.Check != want || d.Ready() {
		t.Errorf("check = %+v, want %+v", d.Check, want)
	}

	if _, err := w.ledger.Import(t.Context(), now, me, acct, file, ledgerbus.Options{Mapping: d.Mapping}); !errors.Is(err, ledgerbus.ErrUnbalanced) {
		t.Errorf("err = %v", err)
	}

	if n := w.count(me, acct); n != 0 {
		t.Errorf("%d transactions imported from a statement that did not balance", n)
	}
}

// A card lists newest first, and its balance is what is owed.
func TestACardNewestFirstBalances(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "card")

	d, _ := w.prepare(me, acct, "card-newest-first.csv")
	if d.Check.Method != ledgerbus.ByBalances || !d.Check.OK {
		t.Errorf("check = %+v", d.Check)
	}
}

// With no balances printed, typed ones are checked; with none at all, the
// statement imports unchecked.
func TestTotalsAndUnchecked(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	d, file := w.prepare(me, acct, "no-balance.csv")
	if d.Check.Method != ledgerbus.Unchecked || !d.Ready() {
		t.Errorf("unchecked: %+v", d.Check)
	}

	opts := ledgerbus.Options{
		Mapping: d.Mapping,
		Opening: importbus.Balance{Amount: money.MustParse("100.00"), Known: true},
		Closing: importbus.Balance{Amount: money.MustParse("168.00"), Known: true},
	}

	d, err := w.ledger.Prepare(t.Context(), me, acct, file, &opts)
	if err != nil || d.Check != (ledgerbus.Check{Method: ledgerbus.ByTotals, OK: true}) {
		t.Errorf("typed balances: %+v, %v", d.Check, err)
	}

	opts.Closing.Amount = money.MustParse("186.00")

	d, _ = w.ledger.Prepare(t.Context(), me, acct, file, &opts)
	if want := (ledgerbus.Check{Method: ledgerbus.ByTotals, Expected: money.MustParse("168.00"), Stated: money.MustParse("186.00")}); d.Check != want {
		t.Errorf("a typo in the closing balance: %+v", d.Check)
	}

	opts.Closing.Amount = money.MustParse("168.00")

	st, err := w.ledger.Import(t.Context(), now, me, acct, file, opts)
	if err != nil || st.Checked != ledgerbus.ByTotals || !st.HasOpening || st.Closing != money.MustParse("168.00") {
		t.Errorf("Import = %+v, %v", st, err)
	}
}

// OFX after a CSV of the same month: the rows the CSV brought are matched
// and given the bank's identifiers, and only the new one is added.
func TestOFXAfterCSVAdoptsTheRows(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.imports(me, acct, "checking-july.csv")

	d, file := w.prepare(me, acct, "checking-july.ofx")
	if d.Format != ledgerbus.OFX || !d.Closing.Known || d.Closing.Amount != money.MustParse("1080.01") {
		t.Fatalf("draft: %s, closing %+v", d.Format, d.Closing)
	}

	// A closing balance alone checks nothing.
	if d.Check.Method != ledgerbus.Unchecked {
		t.Errorf("check = %+v", d.Check)
	}

	st, err := w.ledger.Import(t.Context(), now, me, acct, file, ledgerbus.Options{
		Opening: importbus.Balance{Known: true},
		Closing: d.Closing,
	})
	if err != nil || st.Added != 1 || st.Already != 6 || st.Checked != ledgerbus.ByTotals {
		t.Fatalf("Import = %+v, %v", st, err)
	}

	if st.Start.String() != "2026-07-01" || st.End.String() != "2026-07-31" {
		t.Errorf("the period is the file's: %s to %s", st.Start, st.End)
	}

	txs, _ := w.ledger.Transactions(t.Context(), me, acct, "2026-07")

	ids := map[string]bool{}
	for _, tx := range txs {
		ids[tx.ExternalID] = true
	}

	for _, want := range []string{"F-0703-2", "F-0703-3", "F-0730-1"} {
		if !ids[want] {
			t.Errorf("no transaction carries %s: %v", want, ids)
		}
	}
}

func TestRemovingAStatementRemovesWhatItBrought(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	july := w.imports(me, acct, "checking-july.csv")
	w.imports(me, acct, "checking-july-august.csv")

	if _, err := w.ledger.RemoveStatement(t.Context(), now.Add(time.Hour), me, july.ID); err != nil {
		t.Fatal(err)
	}

	if n := w.count(me, acct); n != 2 {
		t.Errorf("%d transactions left, want the second statement's 2", n)
	}

	sts, _ := w.ledger.Statements(t.Context(), me, acct)
	if len(sts) != 1 {
		t.Errorf("%d statements", len(sts))
	}

	events, _ := w.history.Recent(t.Context(), types.AccountScope(acct), 1)
	if events[0].Action != ledgerbus.StatementRemoved {
		t.Errorf("history: %+v", events[0])
	}

	// Including the two July rows the second statement had found already
	// here: they were July's. Importing it again brings all six back.
	if st := w.imports(me, acct, "checking-july.csv"); st.Added != 6 || st.Already != 0 {
		t.Errorf("again: %d new, %d already", st.Added, st.Already)
	}
}

// Who may do what: a viewer reads and imports nothing, a stranger finds
// nothing, and nobody reads a file somebody else uploaded.
func TestWhoMayImport(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	viewer := w.user("viewer@example.org")
	stranger := w.user("stranger@example.org")
	acct := w.account(me, "checking")

	e, _ := types.ParseEmail("viewer@example.org")
	if _, _, err := w.ten.Grant(t.Context(), now, me, types.AccountScope(acct), e, tenancybus.Viewer); err != nil {
		t.Fatal(err)
	}

	st := w.imports(me, acct, "checking-july.csv")

	file := w.upload(viewer, "checking-july-august.csv")
	if _, err := w.ledger.Prepare(t.Context(), viewer, acct, file, nil); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer prepared an import: %v", err)
	}

	if _, err := w.ledger.RemoveStatement(t.Context(), now, viewer, st.ID); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer removed a statement: %v", err)
	}

	if txs, err := w.ledger.Transactions(t.Context(), viewer, acct, "2026-07"); err != nil || len(txs) != 6 {
		t.Errorf("a viewer reads: %d, %v", len(txs), err)
	}

	for what, err := range map[string]error{
		"transactions": func() error { _, err := w.ledger.Transactions(t.Context(), stranger, acct, "2026-07"); return err }(),
		"statement":    func() error { _, _, err := w.ledger.Statement(t.Context(), stranger, st.ID); return err }(),
		"file":         func() error { _, err := w.ledger.StatementFile(t.Context(), stranger, st.ID); return err }(),
		"remove":       func() error { _, err := w.ledger.RemoveStatement(t.Context(), now, stranger, st.ID); return err }(),
	} {
		if !errors.Is(err, ledgerbus.ErrNotFound) {
			t.Errorf("a stranger's %s: %v", what, err)
		}
	}

	// The owner with somebody else's upload.
	if _, err := w.ledger.Prepare(t.Context(), me, acct, file, nil); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("somebody else's file: %v", err)
	}
}

func TestAPDFIsSaidToComeLater(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	f, err := w.files.Save(t.Context(), now, me, "statement.pdf", strings.NewReader("%PDF-1.7\n%invented\n"), ledgerbus.MaxFile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Prepare(t.Context(), me, acct, f.ID, nil); !errors.Is(err, ledgerbus.ErrPDF) {
		t.Errorf("err = %v", err)
	}
}

func (w *world) grant(by types.ID, scope types.Scope, addr string, role tenancybus.Role) {
	w.t.Helper()

	e, _ := types.ParseEmail(addr)
	if _, _, err := w.ten.Grant(w.t.Context(), now, by, scope, e, role); err != nil {
		w.t.Fatal(err)
	}
}
