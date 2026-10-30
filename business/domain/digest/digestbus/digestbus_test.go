package digestbus_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/digest/digestbus"
	"github.com/jroedel/reconcile/business/domain/digest/stores/digestdb"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
	"github.com/jroedel/reconcile/business/domain/question/stores/questiondb"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/rule/stores/ruledb"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/domain/shape/stores/shapedb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Every statement in testdata/ is invented.
var start = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

// world is a parish whose checking account holds July, its owner, and the
// pieces a week is built from.
type world struct {
	t         *testing.T
	db        *sql.DB
	digest    *digestbus.Business
	questions *questionbus.Business
	ledger    *ledgerbus.Business
	ten       *tenancybus.Business
	users     *userdb.Store
	files     *filebus.Business

	owner          types.ID
	org, account   types.ID
	coffee, bill   ledgerbus.Transaction
	grocery, offer ledgerbus.Transaction
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
		userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ruledb.Init, ledgerdb.Init, shapedb.Init, questiondb.Init, digestdb.Init,
	} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, digestdb.Expected); err != nil {
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
	rules := rulebus.NewBusiness(log, ruledb.NewStore(db), ten, cats)
	ledger := ledgerbus.NewBusiness(log, ledgerdb.NewStore(db), ten, files, cats, rules, shapebus.NewBusiness(log, shapedb.NewStore(db)))
	questions := questionbus.NewBusiness(log, questiondb.NewStore(db), ten, ledger)
	ledger.OnAnswered(questions.Answered)

	w := &world{
		t: t, db: db, ledger: ledger, ten: ten, users: users, files: files, questions: questions,
		digest: digestbus.NewBusiness(digestdb.NewStore(db), ten, ledger, questions),
	}

	w.owner = w.user("treasurer@example.org")
	w.org, w.account = w.parish(w.owner, "St. Joseph Parish")

	txs, err := ledger.Transactions(t.Context(), w.owner, w.account, "2026-07")
	if err != nil {
		t.Fatal(err)
	}

	w.grocery, w.coffee, w.offer, w.bill = txs[1], txs[2], txs[4], txs[5]

	return w
}

// parish makes an organization with a checking account holding July.
func (w *world) parish(owner types.ID, name string) (types.ID, types.ID) {
	w.t.Helper()

	ctx := w.t.Context()

	org, err := w.ten.CreateOrg(ctx, start, owner, name)
	if err != nil {
		w.t.Fatal(err)
	}

	acct, err := w.ten.CreateAccount(ctx, start, owner, org.ID, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		w.t.Fatal(err)
	}

	f, err := os.Open(filepath.Join("testdata", "checking-july.csv"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()

	saved, err := w.files.Save(ctx, start, owner, "checking-july.csv", f, ledgerbus.MaxFile, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	d, err := w.ledger.Prepare(ctx, owner, acct.ID, saved.ID, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	if _, err := w.ledger.Import(ctx, start, owner, acct.ID, saved.ID, ledgerbus.Options{Mapping: d.Mapping, Opening: d.Opening, Closing: d.Closing}); err != nil {
		w.t.Fatal(err)
	}

	return org.ID, acct.ID
}

func (w *world) user(addr string) types.ID {
	w.t.Helper()

	e, err := types.ParseEmail(addr)
	if err != nil {
		w.t.Fatal(err)
	}

	u := userbus.User{ID: types.NewID(), Email: e, Enabled: true, CreatedAt: start, UpdatedAt: start}
	if err := w.users.CreateUser(w.t.Context(), u); err != nil {
		w.t.Fatal(err)
	}

	return u.ID
}

func (w *world) given(addr string, role tenancybus.Role) types.ID {
	w.t.Helper()

	id := w.user(addr)
	e, _ := types.ParseEmail(addr)

	if _, _, err := w.ten.Grant(w.t.Context(), start, w.owner, types.OrgScope(w.org), e, role); err != nil {
		w.t.Fatal(err)
	}

	return id
}

func (w *world) check(id types.ID, number string) {
	w.t.Helper()

	if _, err := w.db.ExecContext(w.t.Context(), `UPDATE transactions SET check_number = ? WHERE id = ?`, number, id.String()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) ask(at time.Time, actor types.ID, t ledgerbus.Transaction, text string) questionbus.Question {
	w.t.Helper()

	q, err := w.questions.Ask(w.t.Context(), at, actor, questionbus.About{Kind: questionbus.OnTransaction, ID: t.ID}, text)
	if err != nil {
		w.t.Fatal(err)
	}

	return q
}

// week builds a person's week, at a time, since another, and keeps what it
// saw as a sent email would.
func (w *world) week(at time.Time, who types.ID, since time.Time) digestbus.Week {
	w.t.Helper()

	wk, err := w.digest.Build(w.t.Context(), at, who, since)
	if err != nil {
		w.t.Fatal(err)
	}

	if err := w.digest.Sent(w.t.Context(), who, wk); err != nil {
		w.t.Fatal(err)
	}

	return wk
}

// One week in a parish: the first email only looks; then the bookkeeper
// hears of a new check to explain and the question waiting for them, the
// accountant of the check now explained and the answer to theirs; nobody
// hears of what they did themselves, a viewer hears nothing, and nobody
// hears of another organization.
func TestWhatTheWeekSays(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	accountant := w.given("accountant@example.org", tenancybus.Accountant)
	viewer := w.given("viewer@example.org", tenancybus.Viewer)
	stranger := w.user("stranger@example.org")

	// Somebody else's parish, with a check of its own to explain.
	elsewhere := w.user("elsewhere@example.org")
	otherOrg, otherAccount := w.parish(elsewhere, "Holy Cross")
	theirs, err := w.ledger.Transactions(ctx, elsewhere, otherAccount, "2026-07")
	if err != nil {
		t.Fatal(err)
	}

	w.check(theirs[5].ID, "77")

	w.check(w.bill.ID, "1322")

	// The first email sees the check to explain, and says nothing of it.
	first := start.Add(time.Hour)
	for _, who := range []types.ID{w.owner, accountant, viewer, stranger} {
		if wk := w.week(first, who, time.Time{}); !wk.Empty() {
			t.Errorf("the first week of %v: %+v", who, wk.Orgs)
		}
	}

	// The week: the bill is explained, a new check arrives, the
	// accountant asks twice and the owner answers once and asks once.
	during := first.Add(time.Hour)

	if _, err := w.ledger.Describe(ctx, during, w.owner, w.bill.ID, "Lights for the hall"); err != nil {
		t.Fatal(err)
	}

	w.check(w.coffee.ID, "1323")

	whose := w.ask(during, accountant, w.offer, "Whose offering was this?")
	w.ask(during, accountant, w.grocery, "Which part was the retreat's?")

	if _, err := w.questions.Reply(ctx, during, w.owner, whose.ID, "The second collection's."); err != nil {
		t.Fatal(err)
	}

	w.ask(during, w.owner, w.grocery, "Does anybody have this receipt?")

	next := during.Add(time.Hour)

	owner := w.week(next, w.owner, first)
	if len(owner.Orgs) != 1 {
		t.Fatalf("the owner's week: %+v", owner.Orgs)
	}

	o := owner.Orgs[0]
	if o.Org.ID != w.org || !o.Keeps || o.Reads || o.NewlyNeeded != 1 || o.Needed != 1 || o.NewlyReady != 0 ||
		len(o.Asked) != 1 || o.Asked[0].Body != "Which part was the retreat's?" || len(o.Answered) != 0 {
		t.Errorf("the owner's week: %+v", o)
	}

	acc := w.week(next, accountant, first)
	if len(acc.Orgs) != 1 {
		t.Fatalf("the accountant's week: %+v", acc.Orgs)
	}

	a := acc.Orgs[0]
	if a.Keeps || !a.Reads || a.NewlyReady != 1 || a.Ready != 1 || a.NewlyNeeded != 0 ||
		len(a.Asked) != 1 || a.Asked[0].Body != "Does anybody have this receipt?" ||
		len(a.Answered) != 1 || a.Answered[0].ID != whose.ID {
		t.Errorf("the accountant's week: %+v", a)
	}

	for _, who := range []types.ID{viewer, stranger} {
		if wk := w.week(next, who, first); !wk.Empty() {
			t.Errorf("a viewer's or a stranger's week: %+v", wk.Orgs)
		}
	}

	for _, wk := range []digestbus.Week{owner, acc} {
		for _, o := range wk.Orgs {
			if o.Org.ID == otherOrg {
				t.Error("another organization in the week")
			}
		}
	}

	// A week later with nothing done, nothing is new, and nobody is sent
	// anything.
	later := next.Add(7 * 24 * time.Hour)
	for _, who := range []types.ID{w.owner, accountant} {
		if wk := w.week(later, who, next); !wk.Empty() {
			t.Errorf("a quiet week: %+v", wk.Orgs)
		}
	}
}

// Somebody's first email looks back a week for questions, no further.
func TestTheFirstWeekLooksBackAWeek(t *testing.T) {
	w := newWorld(t)

	w.ask(start, w.owner, w.coffee, "Long ago?")
	w.ask(start.Add(10*24*time.Hour), w.owner, w.offer, "This week?")

	accountant := w.given("accountant@example.org", tenancybus.Accountant)

	wk := w.week(start.Add(12*24*time.Hour), accountant, time.Time{})
	if len(wk.Orgs) != 1 || len(wk.Orgs[0].Asked) != 1 || wk.Orgs[0].Asked[0].Body != "This week?" {
		t.Errorf("the first week: %+v", wk.Orgs)
	}
}
