package questionbus_test

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
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Every statement in testdata/ is invented.
var now = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

// world is a parish with a checking account holding July, a project with a
// part of the groceries in it, and the people the tests ask as.
type world struct {
	t         *testing.T
	db        *sql.DB
	questions *questionbus.Business
	ledger    *ledgerbus.Business
	ten       *tenancybus.Business
	history   *eventbus.Business
	users     *userdb.Store

	owner                     types.ID
	org, account, project     types.ID
	statement                 ledgerbus.Statement
	grocery, electric, coffee ledgerbus.Transaction
}

func newWorld(t *testing.T) *world {
	t.Helper()

	dir := t.TempDir()

	db, err := sqldb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	for _, init := range []func(context.Context, *sql.DB) error{userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ruledb.Init, ledgerdb.Init, shapedb.Init, questiondb.Init} {
		if err := init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, questiondb.Expected); err != nil {
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

	w := &world{t: t, db: db, questions: questions, ledger: ledger, ten: ten, history: eventbus.NewBusiness(eventdb.NewStore(db)), users: users}
	ctx := t.Context()

	w.owner = w.user("treasurer@example.org")

	org, err := ten.CreateOrg(ctx, now, w.owner, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	acct, err := ten.CreateAccount(ctx, now, w.owner, org.ID, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		t.Fatal(err)
	}

	p, err := ten.CreateProject(ctx, now, w.owner, org.ID, tenancybus.ProjectFields{Name: "World Youth Day"})
	if err != nil {
		t.Fatal(err)
	}

	w.org, w.account, w.project = org.ID, acct.ID, p.ID

	f, err := os.Open(filepath.Join("testdata", "checking-july.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	saved, err := files.Save(ctx, now, w.owner, "checking-july.csv", f, ledgerbus.MaxFile, nil)
	if err != nil {
		t.Fatal(err)
	}

	d, err := ledger.Prepare(ctx, w.owner, w.account, saved.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	if w.statement, err = ledger.Import(ctx, now, w.owner, w.account, saved.ID, ledgerbus.Options{Mapping: d.Mapping, Opening: d.Opening, Closing: d.Closing}); err != nil {
		t.Fatal(err)
	}

	txs, err := ledger.Transactions(ctx, w.owner, w.account, "2026-07")
	if err != nil {
		t.Fatal(err)
	}

	w.grocery, w.coffee, w.electric = txs[1], txs[2], txs[5]

	food, err := cats.Create(ctx, now, w.owner, types.OrgScope(w.org), "Groceries", categorybus.Expense)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ledger.SetSplits(ctx, now, w.owner, w.grocery.ID, []ledgerbus.Part{
		{Amount: money.MustParse("-20.00"), CategoryID: food.ID, ProjectID: w.project},
		{Amount: money.MustParse("-13.99"), CategoryID: food.ID},
	}); err != nil {
		t.Fatal(err)
	}

	return w
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

// given makes a person with a role on a scope.
func (w *world) given(addr string, scope types.Scope, role tenancybus.Role) types.ID {
	w.t.Helper()

	id := w.user(addr)
	e, _ := types.ParseEmail(addr)

	if _, _, err := w.ten.Grant(w.t.Context(), now, w.owner, scope, e, role); err != nil {
		w.t.Fatal(err)
	}

	return id
}

func (w *world) ask(actor types.ID, about questionbus.About, text string) questionbus.Question {
	w.t.Helper()

	q, err := w.questions.Ask(w.t.Context(), now, actor, about, text)
	if err != nil {
		w.t.Fatal(err)
	}

	return q
}

func (w *world) state(id types.ID) questionbus.Question {
	w.t.Helper()

	th, err := w.questions.Question(w.t.Context(), w.owner, id)
	if err != nil {
		w.t.Fatal(err)
	}

	return th.Question
}

func onTx(t ledgerbus.Transaction) questionbus.About {
	return questionbus.About{Kind: questionbus.OnTransaction, ID: t.ID}
}

// The accountant asks; the bookkeeper's reply answers it, and the
// accountant's own reply would not have; the thread reads oldest first;
// and an answered question is context the accountant's lists count.
func TestAskingAndAnswering(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	accountant := w.given("accountant@example.org", types.OrgScope(w.org), tenancybus.Accountant)
	bookkeeper := w.given("bookkeeper@example.org", types.OrgScope(w.org), tenancybus.Bookkeeper)

	if _, err := w.db.ExecContext(ctx, `UPDATE transactions SET check_number = '1322' WHERE id = ?`, w.electric.ID.String()); err != nil {
		t.Fatal(err)
	}

	q := w.ask(accountant, onTx(w.electric), "  What was check 1322 for?  ")
	if q.Body != "What was check 1322 for?" || q.State != questionbus.Open || q.Home != types.AccountScope(w.account) {
		t.Errorf("asked: %+v", q)
	}

	if _, err := w.questions.Reply(ctx, now.Add(time.Minute), accountant, q.ID, "It may be the plumber."); err != nil {
		t.Fatal(err)
	}

	if got := w.state(q.ID); got.State != questionbus.Open {
		t.Errorf("the asker's own reply answered it: %+v", got)
	}

	if ex, err := w.ledger.Explore(ctx, w.owner, types.OrgScope(w.org), ledgerbus.Criteria{Accountant: ledgerbus.MoreContextNeeded}); err != nil || ex.Total != 1 {
		t.Errorf("the check, open: %d to do, %v", ex.Total, err)
	}

	if _, err := w.questions.Reply(ctx, now.Add(2*time.Minute), bookkeeper, q.ID, "The plumber, for the leak in the hall."); err != nil {
		t.Fatal(err)
	}

	got := w.state(q.ID)
	if got.State != questionbus.Answered || got.AnsweredBy != bookkeeper || len(got.Replies) != 2 ||
		got.Replies[0].AuthorID != accountant || got.Replies[1].AuthorID != bookkeeper {
		t.Errorf("answered: %+v", got)
	}

	// Another reply after the answer is a reply, and changes no answer.
	if _, err := w.questions.Reply(ctx, now.Add(3*time.Minute), w.owner, q.ID, "Thank you."); err != nil {
		t.Fatal(err)
	}

	if again := w.state(q.ID); again.AnsweredBy != bookkeeper || len(again.Replies) != 3 {
		t.Errorf("after the answer: %+v", again)
	}

	ex, err := w.ledger.Explore(ctx, w.owner, types.OrgScope(w.org), ledgerbus.Criteria{Accountant: ledgerbus.ContextAdded})
	if err != nil || ex.Total != 1 || ex.Rows[0].Transaction.ID != w.electric.ID {
		t.Fatalf("the check, answered: %d ready, %v", ex.Total, err)
	}

	if g := ex.Rows[0].Given; len(g) != 1 || g[0] != ledgerbus.GaveAnswer {
		t.Errorf("given: %v", g)
	}

	sum, err := w.questions.Summary(ctx, accountant, onTx(w.electric))
	if err != nil || len(sum.Open) != 0 || len(sum.Answered) != 1 || !sum.CanAsk {
		t.Errorf("the questions about the check: %+v, %v", sum, err)
	}
}

// Only the asker marks their question answered, once; anybody else answers
// it by replying.
func TestMarkingAnswered(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	accountant := w.given("accountant@example.org", types.OrgScope(w.org), tenancybus.Accountant)
	q := w.ask(accountant, onTx(w.coffee), "Whose coffee?")

	if err := w.questions.MarkAnswered(ctx, now, w.owner, q.ID); !errors.Is(err, questionbus.ErrForbidden) {
		t.Errorf("the owner marking the accountant's question: %v", err)
	}

	for range 2 {
		if err := w.questions.MarkAnswered(ctx, now, accountant, q.ID); err != nil {
			t.Fatal(err)
		}
	}

	if got := w.state(q.ID); got.State != questionbus.Answered || got.AnsweredBy != accountant {
		t.Errorf("marked: %+v", got)
	}

	lines, err := w.history.Recent(ctx, types.AccountScope(w.account), 50)
	if err != nil {
		t.Fatal(err)
	}

	asked, answered := 0, 0
	for _, l := range lines {
		switch l.Action {
		case questionbus.Asked:
			asked++
		case questionbus.MarkedAnswered:
			answered++
		}
	}

	if asked != 1 || answered != 1 {
		t.Errorf("the history: %d asked, %d answered", asked, answered)
	}
}

// Note asks and replies; Read reads; a role on a project reaches its
// parts' transactions and nothing else of the account; a stranger finds
// nothing at all.
func TestWhoMayAsk(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	viewer := w.given("viewer@example.org", types.OrgScope(w.org), tenancybus.Viewer)
	pilgrim := w.given("pilgrim@example.org", types.ProjectScope(w.project), tenancybus.Contributor)
	stranger := w.user("stranger@example.org")

	q := w.ask(w.owner, onTx(w.grocery), "Which part was the retreat's?")

	if th, err := w.questions.Question(ctx, viewer, q.ID); err != nil || th.CanReply || th.CanMark || !th.Whole {
		t.Errorf("a viewer reading: %+v, %v", th, err)
	}

	if _, err := w.questions.Ask(ctx, now, viewer, onTx(w.grocery), "Me too?"); !errors.Is(err, questionbus.ErrForbidden) {
		t.Errorf("a viewer asking: %v", err)
	}

	if _, err := w.questions.Reply(ctx, now, viewer, q.ID, "Me too?"); !errors.Is(err, questionbus.ErrForbidden) {
		t.Errorf("a viewer replying: %v", err)
	}

	// The pilgrim reaches the groceries through the project's part, and
	// sees that part and none of the rest.
	if _, err := w.questions.Reply(ctx, now, pilgrim, q.ID, "The twenty dollars."); err != nil {
		t.Errorf("the pilgrim replying about the groceries: %v", err)
	}

	th, err := w.questions.Question(ctx, pilgrim, q.ID)
	if err != nil || th.Whole || len(th.Transaction.Splits) != 1 || th.Transaction.Splits[0].ProjectID != w.project ||
		th.Account.Name != "Parish checking" || th.Account.Currency == "" {
		t.Errorf("the groceries, to the pilgrim: %+v, %v", th, err)
	}

	if whole, err := w.questions.Question(ctx, w.owner, q.ID); err != nil || !whole.Whole || len(whole.Transaction.Splits) != 2 {
		t.Errorf("the groceries, to the owner: %+v, %v", whole, err)
	}

	for _, about := range []questionbus.About{
		onTx(w.coffee),
		{Kind: questionbus.OnStatement, ID: w.statement.ID},
	} {
		if _, err := w.questions.Ask(ctx, now, pilgrim, about, "And this?"); !errors.Is(err, questionbus.ErrNotFound) {
			t.Errorf("the pilgrim asking about a %s outside the project: %v", about.Kind, err)
		}
	}

	if _, err := w.questions.Ask(ctx, now, pilgrim, questionbus.About{Kind: questionbus.OnProject, ID: w.project}, "When do we leave?"); err != nil {
		t.Errorf("the pilgrim asking about the project: %v", err)
	}

	for name, call := range map[string]func() error{
		"ask":      func() error { _, err := w.questions.Ask(ctx, now, stranger, onTx(w.grocery), "?"); return err },
		"read":     func() error { _, err := w.questions.Question(ctx, stranger, q.ID); return err },
		"list":     func() error { _, err := w.questions.Summary(ctx, stranger, onTx(w.grocery)); return err },
		"org":      func() error { _, err := w.questions.InOrg(ctx, stranger, w.org, "", types.ID{}); return err },
		"reply":    func() error { _, err := w.questions.Reply(ctx, now, stranger, q.ID, "?"); return err },
		"answered": func() error { return w.questions.MarkAnswered(ctx, now, stranger, q.ID) },
		"nothing": func() error {
			_, err := w.questions.Ask(ctx, now, w.owner, questionbus.About{Kind: "account", ID: w.account}, "?")
			return err
		},
		"missing": func() error { _, err := w.questions.Question(ctx, w.owner, types.NewID()); return err },
	} {
		if err := call(); !errors.Is(err, questionbus.ErrNotFound) {
			t.Errorf("%s: %v, want not found", name, err)
		}
	}
}

// Each of the four things takes a question, and its history goes where the
// thing's does; through a key, the key is said.
func TestAskingAboutEachThing(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	paid, err := w.ledger.SetPayee(ctx, now, w.owner, w.electric.ID, "Hilltop Electric")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		about questionbus.About
		home  types.Scope
	}{
		{onTx(w.electric), types.AccountScope(w.account)},
		{questionbus.About{Kind: questionbus.OnStatement, ID: w.statement.ID}, types.AccountScope(w.account)},
		{questionbus.About{Kind: questionbus.OnProject, ID: w.project}, types.ProjectScope(w.project)},
		{questionbus.About{Kind: questionbus.OnPayee, ID: paid.PayeeID}, types.OrgScope(w.org)},
	} {
		q, err := w.questions.Ask(eventbus.WithVia(ctx, "Claude"), now, w.owner, c.about, "What is this?")
		if err != nil || q.Home != c.home || q.Via != "Claude" {
			t.Errorf("about a %s: %+v, %v", c.about.Kind, q, err)
		}
	}
}

// What is written must be something, and not too much.
func TestABodyIsWritten(t *testing.T) {
	w := newWorld(t)

	for _, text := range []string{"", "   ", strings.Repeat("x", questionbus.MaxBody+1)} {
		if _, err := w.questions.Ask(t.Context(), now, w.owner, onTx(w.coffee), text); !errors.Is(err, questionbus.ErrBody) {
			t.Errorf("%d characters: %v", len(text), err)
		}
	}
}

// Removing a statement takes the questions about its transactions, and
// about it, with them.
func TestQuestionsGoWithTheirStatement(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	onCoffee := w.ask(w.owner, onTx(w.coffee), "Whose coffee?")
	onStatement := w.ask(w.owner, questionbus.About{Kind: questionbus.OnStatement, ID: w.statement.ID}, "Is July all here?")
	onProject := w.ask(w.owner, questionbus.About{Kind: questionbus.OnProject, ID: w.project}, "When do we leave?")

	if _, err := w.questions.Reply(ctx, now, w.owner, onCoffee.ID, "Mine."); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.RemoveStatement(ctx, now, w.owner, w.statement.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []types.ID{onCoffee.ID, onStatement.ID} {
		if _, err := w.questions.Question(ctx, w.owner, id); !errors.Is(err, questionbus.ErrNotFound) {
			t.Errorf("a question about what was removed: %v", err)
		}
	}

	var replies int
	if err := w.db.QueryRowContext(ctx, `SELECT count(*) FROM question_replies`).Scan(&replies); err != nil || replies != 0 {
		t.Errorf("replies left: %d, %v", replies, err)
	}

	if _, err := w.questions.Question(ctx, w.owner, onProject.ID); err != nil {
		t.Errorf("the project's question: %v", err)
	}
}

// An organization's list is every question about anything in it, newest
// first, open or answered, and those one person asked.
func TestAnOrganizationsQuestions(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	accountant := w.given("accountant@example.org", types.OrgScope(w.org), tenancybus.Accountant)

	coffee := w.ask(accountant, onTx(w.coffee), "Whose coffee?")
	w.ask(w.owner, questionbus.About{Kind: questionbus.OnProject, ID: w.project}, "When do we leave?")
	statement := w.ask(accountant, questionbus.About{Kind: questionbus.OnStatement, ID: w.statement.ID}, "Is July all here?")

	if err := w.questions.MarkAnswered(ctx, now, accountant, statement.ID); err != nil {
		t.Fatal(err)
	}

	count := func(state questionbus.State, by types.ID) int {
		t.Helper()

		got, err := w.questions.InOrg(ctx, w.owner, w.org, state, by)
		if err != nil {
			t.Fatal(err)
		}

		for _, l := range got {
			if l.What == "" {
				t.Errorf("a question about nothing named: %+v", l)
			}
		}

		return len(got)
	}

	all, open, answered, theirs := count("", types.ID{}), count(questionbus.Open, types.ID{}), count(questionbus.Answered, types.ID{}), count(questionbus.Open, accountant)
	if all != 3 || open != 2 || answered != 1 || theirs != 1 {
		t.Errorf("all %d, open %d, answered %d, the accountant's open %d", all, open, answered, theirs)
	}

	got, err := w.questions.InOrg(ctx, accountant, w.org, questionbus.Open, accountant)
	if err != nil || len(got) != 1 || got[0].ID != coffee.ID || !strings.Contains(got[0].What, "COFFEE CART") {
		t.Errorf("the accountant's open questions: %+v, %v", got, err)
	}
}

// A list shows a question's first line, shortened.
func TestAQuestionsOpening(t *testing.T) {
	for body, want := range map[string]string{
		"What was check 1322 for?":                      "What was check 1322 for?",
		"What was it for?\nIt is not on the statement.": "What was it for?",
		strings.Repeat("a", 200):                        strings.Repeat("a", 119) + "…",
	} {
		if got := (questionbus.Question{Body: body}).Opening(); got != want {
			t.Errorf("Opening(%.20q) = %q", body, got)
		}
	}
}
