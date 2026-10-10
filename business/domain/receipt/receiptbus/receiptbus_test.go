package receiptbus_test

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
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/receipt/stores/receiptdb"
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

// july is an invented statement with a running balance.
const july = "Date,Description,Amount,Balance\n" +
	"2026-07-01,OPENING DEPOSIT,1000.00,1000.00\n" +
	"2026-07-03,CORNER GROCERY,-33.99,966.01\n" +
	"2026-07-04,COFFEE CART,-3.50,962.51\n" +
	"2026-07-28,ELECTRIC CO,-120.00,842.51\n"

// photo is the start of a JPEG, which is all the sniffer reads; invented.
const photo = "\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00 an invented receipt"

type world struct {
	t        *testing.T
	ctx      context.Context
	receipts *receiptbus.Business
	ledger   *ledgerbus.Business
	ten      *tenancybus.Business
	files    *filebus.Business
	users    *userdb.Store
	history  *eventbus.Business

	owner, pilgrim, stranger types.ID
	org, account, project    types.ID
	grocery, coffee          ledgerbus.Transaction
	statement                types.ID
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
		userdb.Init, eventdb.Init, tenancydb.Init, filedb.Init, categorydb.Init, ruledb.Init, ledgerdb.Init, receiptdb.Init,
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

	w := &world{
		t: t, ctx: t.Context(), ledger: ledger, ten: ten, files: files, users: users,
		receipts: receiptbus.NewBusiness(log, receiptdb.NewStore(db), ten, ledger, files),
		history:  eventbus.NewBusiness(eventdb.NewStore(db)),
	}

	w.owner = w.user("treasurer@example.org")
	w.pilgrim = w.user("pilgrim@example.org")
	w.stranger = w.user("stranger@example.org")

	org, err := ten.CreateOrg(w.ctx, now, w.owner, "St. Joseph Parish")
	if err != nil {
		t.Fatal(err)
	}

	acct, _ := ten.CreateAccount(w.ctx, now, w.owner, org.ID, tenancybus.AccountFields{Name: "Parish card", Kind: "card"})
	p, _ := ten.CreateProject(w.ctx, now, w.owner, org.ID, tenancybus.ProjectFields{Name: "World Youth Day"})
	w.org, w.account, w.project = org.ID, acct.ID, p.ID

	// The pilgrim is given the project, and nothing else.
	w.grant(types.ProjectScope(p.ID), "pilgrim@example.org", tenancybus.Contributor)

	f := w.upload(w.owner, "july.csv", july, nil)

	d, err := ledger.Prepare(w.ctx, w.owner, acct.ID, f, nil)
	if err != nil {
		t.Fatal(err)
	}

	st, err := ledger.Import(w.ctx, now, w.owner, acct.ID, f, ledgerbus.Options{Mapping: d.Mapping})
	if err != nil {
		t.Fatal(err)
	}

	w.statement = st.ID

	txs, _ := ledger.Transactions(w.ctx, w.owner, acct.ID, "2026-07")
	w.grocery, w.coffee = txs[1], txs[2]

	return w
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

func (w *world) grant(scope types.Scope, addr string, role tenancybus.Role) {
	w.t.Helper()

	e, _ := types.ParseEmail(addr)
	if _, _, err := w.ten.Grant(w.ctx, now, w.owner, scope, e, role); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) upload(actor types.ID, name, content string, accept []string) types.ID {
	w.t.Helper()

	f, err := w.files.Save(w.ctx, now, actor, name, strings.NewReader(content), 1<<20, accept)
	if err != nil {
		w.t.Fatal(err)
	}

	return f.ID
}

func (w *world) photos(actor types.ID, n int) []types.ID {
	ids := make([]types.ID, n)
	for i := range ids {
		ids[i] = w.upload(actor, "IMG_000"+string(rune('1'+i))+".jpg", photo+string(rune('a'+i)), receiptbus.Accept)
	}

	return ids
}

// The design's test: three photographs into the project, by somebody given
// the project and nothing else.
func TestAVolunteerPutsThreeReceiptsIntoTheProject(t *testing.T) {
	w := newWorld(t)

	added, err := w.receipts.Add(w.ctx, now, w.pilgrim, types.ProjectScope(w.project), w.photos(w.pilgrim, 3), false, receiptbus.Details{})
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 3 || len(added[0].Files) != 1 || !added[0].Waiting() {
		t.Fatalf("added: %+v", added)
	}

	// Pages of one receipt are one receipt.
	one, err := w.receipts.Add(w.ctx, now, w.pilgrim, types.ProjectScope(w.project), w.photos(w.pilgrim, 2), true, receiptbus.Details{Merchant: "Hostel"})
	if err != nil || len(one) != 1 || len(one[0].Files) != 2 {
		t.Fatalf("one receipt of two pages: %+v, %v", one, err)
	}

	in, err := w.receipts.Inbox(w.ctx, w.pilgrim, types.ProjectScope(w.project))
	if err != nil || len(in.Waiting) != 4 {
		t.Fatalf("the inbox: %d waiting, %v", len(in.Waiting), err)
	}

	// The treasurer sees them waiting.
	waiting, _ := w.receipts.Waiting(w.ctx, w.owner)
	if len(waiting) != 4 {
		t.Errorf("the treasurer's waiting list: %d", len(waiting))
	}

	events, _ := w.history.Recent(w.ctx, types.ProjectScope(w.project), 1)
	if events[0].Action != receiptbus.Added || events[0].Detail["count"] != "1" {
		t.Errorf("history: %+v", events[0])
	}
}

func TestWhoMayAdd(t *testing.T) {
	w := newWorld(t)

	viewer := w.user("viewer@example.org")
	w.grant(types.ProjectScope(w.project), "viewer@example.org", tenancybus.Viewer)

	if _, err := w.receipts.Add(w.ctx, now, viewer, types.ProjectScope(w.project), w.photos(viewer, 1), false, receiptbus.Details{}); !errors.Is(err, receiptbus.ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}

	if _, err := w.receipts.Add(w.ctx, now, w.stranger, types.ProjectScope(w.project), w.photos(w.stranger, 1), false, receiptbus.Details{}); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	// Somebody else's upload is no way in.
	theirs := w.photos(w.owner, 1)
	if _, err := w.receipts.Add(w.ctx, now, w.pilgrim, types.ProjectScope(w.project), theirs, false, receiptbus.Details{}); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("another person's file: %v", err)
	}

	// A text file is not a receipt, and is not kept.
	if _, err := w.files.Save(w.ctx, now, w.pilgrim, "notes.txt", strings.NewReader("words"), 1<<20, receiptbus.Accept); !errors.Is(err, filebus.ErrType) {
		t.Errorf("a text file: %v", err)
	}
}

// Suggestions are the same amount within a few days; attaching is a click,
// and then whoever may see the transaction may see the receipt.
func TestMatchingAReceipt(t *testing.T) {
	w := newWorld(t)

	spent, _ := types.ParseDate("2026-07-05")
	added, err := w.receipts.Add(w.ctx, now, w.pilgrim, types.ProjectScope(w.project), w.photos(w.pilgrim, 1), false,
		receiptbus.Details{SpentOn: spent, Amount: money.MustParse("33.99"), HasAmount: true, Merchant: "Corner Grocery"})
	if err != nil {
		t.Fatal(err)
	}

	r := added[0]

	// The pilgrim cannot see the card, so is offered nothing from it.
	if s, _ := w.receipts.Suggestions(w.ctx, w.pilgrim, added); len(s[r.ID]) != 0 {
		t.Errorf("the pilgrim was offered the card's transactions: %v", s)
	}

	s, err := w.receipts.Suggestions(w.ctx, w.owner, added)
	if err != nil || len(s[r.ID]) != 1 || s[r.ID][0].ID != w.grocery.ID {
		t.Fatalf("suggestions: %+v, %v", s, err)
	}

	// The pilgrim may not attach to a transaction they cannot see.
	if _, err := w.receipts.Attach(w.ctx, now, w.pilgrim, r.ID, w.grocery.ID); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("the pilgrim attached to the card: %v", err)
	}

	if _, err := w.receipts.Attach(w.ctx, now, w.owner, r.ID, w.grocery.ID); err != nil {
		t.Fatal(err)
	}

	if waiting, _ := w.receipts.Waiting(w.ctx, w.owner); len(waiting) != 0 {
		t.Errorf("still waiting: %d", len(waiting))
	}

	on, _ := w.receipts.OnTransactions(w.ctx, []types.ID{w.grocery.ID})
	if len(on[w.grocery.ID]) != 1 {
		t.Errorf("the grocery's receipts: %+v", on)
	}

	// A viewer of the card alone sees the receipt through its transaction.
	clerk := w.user("clerk@example.org")
	w.grant(types.AccountScope(w.account), "clerk@example.org", tenancybus.Viewer)

	v, err := w.receipts.Receipt(w.ctx, clerk, r.ID)
	if err != nil || len(v.Transactions) != 1 || !v.Readable[w.grocery.ID] || v.CanEdit || v.CanRemove {
		t.Errorf("the clerk's view: %+v, %v", v, err)
	}

	if _, err := w.receipts.File(w.ctx, clerk, r.ID, 0); err != nil {
		t.Errorf("the clerk's file: %v", err)
	}

	if _, err := w.receipts.Receipt(w.ctx, w.stranger, r.ID); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	// Attached, it cannot be removed; detached, it can, and it comes back.
	if _, err := w.receipts.SetRemoved(w.ctx, now, w.pilgrim, r.ID, true); !errors.Is(err, receiptbus.ErrAttached) {
		t.Errorf("removing an attached receipt: %v", err)
	}

	if _, err := w.receipts.Detach(w.ctx, w.owner, r.ID, w.grocery.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := w.receipts.SetRemoved(w.ctx, now, w.pilgrim, r.ID, true); err != nil {
		t.Fatal(err)
	}

	in, _ := w.receipts.Inbox(w.ctx, w.owner, types.ProjectScope(w.project))
	if len(in.Removed) != 1 || len(in.Waiting) != 0 {
		t.Errorf("after removing: %+v", in)
	}

	if _, err := w.receipts.SetRemoved(w.ctx, now, w.pilgrim, r.ID, false); err != nil {
		t.Fatal(err)
	}

	if in, _ := w.receipts.Inbox(w.ctx, w.owner, types.ProjectScope(w.project)); len(in.Waiting) != 1 {
		t.Errorf("after bringing it back: %+v", in)
	}
}

// A project's contributor may attach to a charge in the project's book
// without being given the account.
func TestAttachingToAChargeInTheProject(t *testing.T) {
	w := newWorld(t)

	if _, err := w.ledger.SetSplits(w.ctx, now, w.owner, w.coffee.ID, []ledgerbus.Part{{Amount: w.coffee.Amount, ProjectID: w.project}}); err != nil {
		t.Fatal(err)
	}

	added, _ := w.receipts.Add(w.ctx, now, w.pilgrim, types.ProjectScope(w.project), w.photos(w.pilgrim, 1), false, receiptbus.Details{})

	if _, err := w.receipts.Attach(w.ctx, now, w.pilgrim, added[0].ID, w.coffee.ID); err != nil {
		t.Errorf("the coffee in the project: %v", err)
	}

	v, _ := w.receipts.Receipt(w.ctx, w.pilgrim, added[0].ID)
	if len(v.Transactions) != 1 || v.Readable[w.coffee.ID] {
		t.Errorf("the pilgrim sees the coffee, and cannot open the card: %+v", v.Readable)
	}

	// Straight onto a transaction, from its page.
	on, err := w.receipts.AddToTransaction(w.ctx, now, w.pilgrim, w.coffee.ID, w.photos(w.pilgrim, 1), false, receiptbus.Details{})
	if err != nil || !on[0].AttachedTo(w.coffee.ID) || on[0].Home != types.AccountScope(w.account) {
		t.Errorf("onto the coffee: %+v, %v", on, err)
	}
}

// Removing a statement puts its receipts back, waiting, rather than losing
// them.
func TestRemovingAStatementKeepsItsReceipts(t *testing.T) {
	w := newWorld(t)

	added, _ := w.receipts.Add(w.ctx, now, w.owner, types.AccountScope(w.account), w.photos(w.owner, 1), false, receiptbus.Details{})
	if _, err := w.receipts.Attach(w.ctx, now, w.owner, added[0].ID, w.grocery.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.RemoveStatement(w.ctx, now, w.owner, w.statement); err != nil {
		t.Fatal(err)
	}

	if waiting, _ := w.receipts.Waiting(w.ctx, w.owner); len(waiting) != 1 {
		t.Errorf("%d waiting after the statement went", len(waiting))
	}
}

func TestDetailsAreChecked(t *testing.T) {
	w := newWorld(t)

	added, _ := w.receipts.Add(w.ctx, now, w.pilgrim, types.ProjectScope(w.project), w.photos(w.pilgrim, 1), false, receiptbus.Details{})

	_, err := w.receipts.SetDetails(w.ctx, w.pilgrim, added[0].ID, receiptbus.Details{Merchant: strings.Repeat("x", 101)})
	if invalid, ok := errors.AsType[receiptbus.Invalid](err); !ok || invalid.Field != "merchant" {
		t.Errorf("a long merchant: %v", err)
	}

	r, err := w.receipts.SetDetails(w.ctx, w.pilgrim, added[0].ID, receiptbus.Details{Merchant: "Hostel", Amount: money.MustParse("80.00"), HasAmount: true})
	if err != nil || r.Merchant != "Hostel" {
		t.Errorf("SetDetails: %+v, %v", r, err)
	}

}
