// Package receiptbus is receipts: photos and PDFs of what money was spent
// on, and which transactions they belong to (docs/plan.md, "Receipts and
// files").
//
// A receipt arrives in an inbox -- an account's, or a project's, where a
// volunteer with a role on nothing else can put it -- or straight onto a
// transaction. Until it is attached to one it is waiting. Attaching is a
// person's decision: suggestions are offered (the same amount, within a few
// days), and nothing is attached on its own.
//
// # Who may see one
//
// Whoever may read its inbox, and whoever may see a transaction it is
// attached to: a reader of the transaction's account, or of a project one
// of the transaction's parts is in. A role on a project is the right to see
// its parts with their receipts (docs/plan.md, "Inheritance"), and the
// receipt a pilgrim photographed is exactly what the treasurer needs to see
// once it is matched. Every lookup that misses is ErrNotFound.
//
// # Who may change one
//
// Uploading into an inbox, and attaching, take the Receipts permission:
// a contributor's whole job. Attaching to a transaction takes it on the
// transaction's account, or on a project the transaction is in, so that a
// project's contributor can attach a receipt to a charge in the project's
// book without being given the account. The details are the uploader's to
// correct, and anybody's with Receipts on the inbox. Removing is the
// uploader's or a bookkeeper's, only while the receipt is waiting, and is
// undone with the button beside it.
package receiptbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The errors a page tells apart.
var (
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden

	// ErrAttached is removing a receipt that is attached: take it off its
	// transactions first, which says what is being undone.
	ErrAttached = errors.New("the receipt is attached to a transaction")

	// ErrRemoved is attaching a receipt that was removed.
	ErrRemoved = errors.New("the receipt was removed")
)

// The actions written into an inbox's history. Attaching is not one: the
// link itself says who attached it and when, on the transaction's page.
const (
	Added    eventbus.Action = "receipt.added"
	Removed  eventbus.Action = "receipt.removed"
	Restored eventbus.Action = "receipt.restored"

	// CheckRead is a check's image read through the API (ReadCheck): what
	// Claude says a check is, which the history keeps beside who said it.
	CheckRead eventbus.Action = "receipt.check_read"
)

// Accept is the kinds of file a receipt may be.
var Accept = []string{filebus.JPEG, filebus.PNG, filebus.WebP, filebus.HEIC, filebus.PDF}

// The limits on what is typed about a receipt.
const (
	MaxMerchant = 100
	MaxNote     = 500
)

// MatchDays is how far from a receipt's date a transaction may be posted
// and still be suggested: a card charge posts a day or three after the
// shop, and a weekend makes it five.
const MatchDays = 5

// Receipt is one receipt: one or more files, and what is known about it.
type Receipt struct {
	ID types.ID

	// Home is the inbox it was put in: an account or a project.
	Home types.Scope

	UploadedBy types.ID
	CreatedAt  time.Time
	RemovedAt  time.Time

	Details

	Files []filebus.File
	Links []Link

	// CheckImage is whether it is the image of a check, front and back as
	// its pages, and Check that check's number, "" until somebody has
	// read it (checks.go). Check is "" for any other receipt.
	CheckImage bool
	Check      string
}

// Details is what a person may type about a receipt, all of it optional.
type Details struct {
	SpentOn   types.Date
	Amount    money.Amount // what was paid, never negative
	HasAmount bool
	Merchant  string
	Note      string

	// Memo and WrittenOn are a check image's alone: what its memo line
	// says, and the day written on it. SpentOn is the day it cleared once
	// it is attached (matched), so the day written is kept apart.
	Memo      string
	WrittenOn types.Date
}

// Link is a receipt's attachment to one transaction.
type Link struct {
	TransactionID types.ID
	LinkedBy      types.ID
	LinkedAt      time.Time
}

// Removed reports whether it was taken out of its inbox.
func (r Receipt) Removed() bool { return !r.RemovedAt.IsZero() }

// Waiting reports whether it is in its inbox and on no transaction.
func (r Receipt) Waiting() bool { return len(r.Links) == 0 && !r.Removed() }

// AttachedTo reports whether it is on the transaction.
func (r Receipt) AttachedTo(id types.ID) bool {
	return slices.ContainsFunc(r.Links, func(l Link) bool { return l.TransactionID == id })
}

// Access is how this domain asks who may do what (tenancybus).
type Access interface {
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
	Overview(ctx context.Context, actor types.ID) (tenancybus.Overview, error)
}

// Ledger is the transactions receipts are attached to (ledgerbus).
type Ledger interface {
	Lookup(ctx context.Context, id types.ID) (ledgerbus.Transaction, error)
	LookupAll(ctx context.Context, ids []types.ID) ([]ledgerbus.Transaction, error)
	Matching(ctx context.Context, accounts []types.ID, amount money.Amount, on types.Date, days int) ([]ledgerbus.Transaction, error)
	WithCheck(ctx context.Context, account types.ID, number string) ([]ledgerbus.Transaction, error)
	SetPayee(ctx context.Context, now time.Time, actor, id types.ID, payee string) (ledgerbus.Transaction, error)
	SetWritten(ctx context.Context, now time.Time, actor, id types.ID, memo string, on types.Date) (ledgerbus.Transaction, error)
}

// Files is where the receipts' bytes are (filebus).
type Files interface {
	ByID(ctx context.Context, id types.ID) (filebus.File, error)
}

// Storer keeps receipts.
type Storer interface {
	// Create stores new receipts with their files and links, and the
	// history, in one transaction.
	Create(ctx context.Context, receipts []Receipt, ev eventbus.Event) error

	ByID(ctx context.Context, id types.ID) (Receipt, error)

	// InHomes is the receipts in any of the inboxes, newest first: those
	// waiting only, or all of them.
	InHomes(ctx context.Context, homes []types.Scope, waitingOnly bool) ([]Receipt, error)

	// OnTransactions is the receipts attached to any of the transactions.
	OnTransactions(ctx context.Context, ids []types.ID) ([]Receipt, error)

	// HoldsContent reports whether a receipt in the inbox, not removed,
	// has a file with these bytes.
	HoldsContent(ctx context.Context, home types.Scope, sha256 string) (bool, error)

	// Link attaches; attaching twice is attaching once.
	Link(ctx context.Context, receiptID types.ID, l Link) error
	Unlink(ctx context.Context, receiptID, transactionID types.ID) error

	// Update writes the details and the removed time, and the history
	// when there is a line to write.
	Update(ctx context.Context, r Receipt, ev *eventbus.Event) error

	// SaveCheck writes a check image's number and details, and its links,
	// and the history when there is a line to write, in one transaction
	// (checks.go).
	SaveCheck(ctx context.Context, r Receipt, ev *eventbus.Event) error
}

// Business is the set of operations on receipts.
type Business struct {
	log    *slog.Logger
	store  Storer
	access Access
	ledger Ledger
	files  Files
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, access Access, ledger Ledger, files Files) *Business {
	return &Business{log: log, store: store, access: access, ledger: ledger, files: files}
}

// --- who may ------------------------------------------------------------------

func (b *Business) can(ctx context.Context, actor types.ID, scope types.Scope, p tenancybus.Permission) (bool, error) {
	access, err := b.access.AccessTo(ctx, actor, scope)

	return access.Can(p), err
}

// onTransaction reports whether the actor holds p on a transaction: on its
// account, or on a project one of its parts is in.
func (b *Business) onTransaction(ctx context.Context, actor types.ID, t ledgerbus.Transaction, p tenancybus.Permission) (bool, error) {
	if ok, err := b.can(ctx, actor, types.AccountScope(t.AccountID), p); ok || err != nil {
		return ok, err
	}

	for _, s := range t.Splits {
		if s.ProjectID.Zero() {
			continue
		}

		if ok, err := b.can(ctx, actor, types.ProjectScope(s.ProjectID), p); ok || err != nil {
			return ok, err
		}
	}

	return false, nil
}

// canRead reports whether the actor may see a receipt (the package comment).
func (b *Business) canRead(ctx context.Context, actor types.ID, r Receipt) (bool, error) {
	if ok, err := b.can(ctx, actor, r.Home, tenancybus.Read); ok || err != nil {
		return ok, err
	}

	txs, err := b.ledger.LookupAll(ctx, linked(r))
	if err != nil {
		return false, err
	}

	for _, t := range txs {
		if ok, err := b.onTransaction(ctx, actor, t, tenancybus.Read); ok || err != nil {
			return ok, err
		}
	}

	return false, nil
}

func linked(r Receipt) []types.ID {
	ids := make([]types.ID, len(r.Links))
	for i, l := range r.Links {
		ids[i] = l.TransactionID
	}

	return ids
}

// readable is a receipt the actor may see, or ErrNotFound.
func (b *Business) readable(ctx context.Context, actor, id types.ID) (Receipt, error) {
	r, err := b.store.ByID(ctx, id)
	if err != nil {
		return Receipt{}, err
	}

	ok, err := b.canRead(ctx, actor, r)

	switch {
	case err != nil:
		return Receipt{}, err
	case !ok:
		return Receipt{}, ErrNotFound
	}

	return r, nil
}

// transaction is a transaction the actor may see, and whether they may
// attach receipts to it.
func (b *Business) transaction(ctx context.Context, actor, id types.ID) (ledgerbus.Transaction, bool, error) {
	t, err := b.ledger.Lookup(ctx, id)
	if err != nil {
		return ledgerbus.Transaction{}, false, err
	}

	if ok, err := b.onTransaction(ctx, actor, t, tenancybus.Read); err != nil {
		return ledgerbus.Transaction{}, false, err
	} else if !ok {
		return ledgerbus.Transaction{}, false, ErrNotFound
	}

	ok, err := b.onTransaction(ctx, actor, t, tenancybus.Receipts)

	return t, ok, err
}

// --- adding -------------------------------------------------------------------

// Invalid is a detail that will not do.
type Invalid struct {
	Field string
	Err   error
}

func (e Invalid) Error() string { return e.Field + ": " + e.Err.Error() }
func (e Invalid) Unwrap() error { return e.Err }

func (d Details) check() error {
	switch {
	case d.Amount < 0:
		return Invalid{Field: "amount", Err: errors.New("an amount paid is not negative")}
	case utf8.RuneCountInString(d.Merchant) > MaxMerchant:
		return Invalid{Field: "merchant", Err: fmt.Errorf("at most %d characters", MaxMerchant)}
	case utf8.RuneCountInString(d.Note) > MaxNote:
		return Invalid{Field: "note", Err: fmt.Errorf("at most %d characters", MaxNote)}
	case utf8.RuneCountInString(d.Memo) > ledgerbus.MaxCheckMemo:
		return Invalid{Field: "memo", Err: fmt.Errorf("at most %d characters", ledgerbus.MaxCheckMemo)}
	}

	return nil
}

// newReceipts makes the receipts for an upload: one per file, or one
// holding them all as its pages. The files must be the actor's own.
func (b *Business) newReceipts(ctx context.Context, now time.Time, actor types.ID, home types.Scope, fileIDs []types.ID, together bool, d Details) ([]Receipt, error) {
	if err := d.check(); err != nil {
		return nil, err
	}

	if len(fileIDs) == 0 {
		return nil, Invalid{Field: "files", Err: errors.New("there are no files")}
	}

	files := make([]filebus.File, len(fileIDs))

	for i, id := range fileIDs {
		f, err := b.files.ByID(ctx, id)
		if errors.Is(err, filebus.ErrNotFound) || err == nil && f.UploadedBy != actor {
			return nil, ErrNotFound
		}

		if err != nil {
			return nil, err
		}

		files[i] = f
	}

	receipt := func(files ...filebus.File) Receipt {
		return Receipt{ID: types.NewID(), Home: home, UploadedBy: actor, CreatedAt: now, Details: d, Files: files}
	}

	if together {
		return []Receipt{receipt(files...)}, nil
	}

	out := make([]Receipt, len(files))
	for i, f := range files {
		out[i] = receipt(f)
	}

	return out, nil
}

// Add puts uploaded files into an inbox as receipts.
func (b *Business) Add(ctx context.Context, now time.Time, actor types.ID, home types.Scope, fileIDs []types.ID, together bool, d Details) ([]Receipt, error) {
	if home.Kind != types.ScopeAccount && home.Kind != types.ScopeProject {
		return nil, ErrNotFound
	}

	access, err := b.access.AccessTo(ctx, actor, home)

	switch {
	case err != nil:
		return nil, err
	case !access.Can(tenancybus.Read):
		return nil, ErrNotFound
	case !access.Can(tenancybus.Receipts):
		return nil, ErrForbidden
	}

	receipts, err := b.newReceipts(ctx, now, actor, home, fileIDs, together, d)
	if err != nil {
		return nil, err
	}

	ev := eventbus.New(now, actor, home, Added, map[string]string{"count": strconv.Itoa(len(receipts))})

	return receipts, b.store.Create(ctx, receipts, ev)
}

// Holds reports whether an inbox already has a receipt, not removed, with
// the same bytes as an uploaded file: a photo sent again, because the
// phone sending it lost its signal before it heard that it had arrived
// (receiptapp's share page). Asked before adding it, so that sending again
// is always safe. A receipt that was removed does not count, so that a
// photo removed by mistake can be added again as well as brought back.
func (b *Business) Holds(ctx context.Context, actor types.ID, home types.Scope, fileID types.ID) (bool, error) {
	if ok, err := b.can(ctx, actor, home, tenancybus.Read); err != nil {
		return false, err
	} else if !ok {
		return false, ErrNotFound
	}

	f, err := b.files.ByID(ctx, fileID)
	if errors.Is(err, filebus.ErrNotFound) || err == nil && f.UploadedBy != actor {
		return false, ErrNotFound
	}

	if err != nil {
		return false, err
	}

	return b.store.HoldsContent(ctx, home, f.SHA256)
}

// AddToTransaction puts uploaded files onto a transaction as receipts,
// attached already. Their inbox is the transaction's account's.
func (b *Business) AddToTransaction(ctx context.Context, now time.Time, actor, transactionID types.ID, fileIDs []types.ID, together bool, d Details) ([]Receipt, error) {
	t, may, err := b.transaction(ctx, actor, transactionID)
	if err != nil {
		return nil, err
	}

	if !may {
		return nil, ErrForbidden
	}

	home := types.AccountScope(t.AccountID)

	receipts, err := b.newReceipts(ctx, now, actor, home, fileIDs, together, d)
	if err != nil {
		return nil, err
	}

	for i := range receipts {
		receipts[i].Links = []Link{{TransactionID: t.ID, LinkedBy: actor, LinkedAt: now}}
	}

	ev := eventbus.New(now, actor, home, Added, map[string]string{"count": strconv.Itoa(len(receipts))})

	return receipts, b.store.Create(ctx, receipts, ev)
}

// --- reading ------------------------------------------------------------------

// View is one receipt as its page shows it.
type View struct {
	Receipt Receipt

	// Transactions is those it is attached to, and Readable which of them
	// the actor may open (the others they see through a project).
	Transactions []ledgerbus.Transaction
	Readable     map[types.ID]bool

	CanEdit   bool
	CanRemove bool
}

// Receipt is one receipt and what the actor may do with it.
func (b *Business) Receipt(ctx context.Context, actor, id types.ID) (View, error) {
	r, err := b.readable(ctx, actor, id)
	if err != nil {
		return View{}, err
	}

	v := View{Receipt: r, Readable: map[types.ID]bool{}}

	if v.Transactions, err = b.ledger.LookupAll(ctx, linked(r)); err != nil {
		return View{}, err
	}

	for _, t := range v.Transactions {
		if v.Readable[t.ID], err = b.can(ctx, actor, types.AccountScope(t.AccountID), tenancybus.Read); err != nil {
			return View{}, err
		}
	}

	if v.CanEdit, err = b.mayEdit(ctx, actor, r); err != nil {
		return View{}, err
	}

	if v.CanRemove, err = b.mayRemove(ctx, actor, r); err != nil {
		return View{}, err
	}

	return v, nil
}

func (b *Business) mayEdit(ctx context.Context, actor types.ID, r Receipt) (bool, error) {
	if r.UploadedBy == actor {
		return true, nil
	}

	return b.can(ctx, actor, r.Home, tenancybus.Receipts)
}

func (b *Business) mayRemove(ctx context.Context, actor types.ID, r Receipt) (bool, error) {
	if r.UploadedBy == actor {
		return true, nil
	}

	return b.can(ctx, actor, r.Home, tenancybus.Bookkeep)
}

// File is one of a receipt's files, counting from zero.
func (b *Business) File(ctx context.Context, actor, id types.ID, n int) (filebus.File, error) {
	r, err := b.readable(ctx, actor, id)
	if err != nil {
		return filebus.File{}, err
	}

	if n < 0 || n >= len(r.Files) {
		return filebus.File{}, ErrNotFound
	}

	return r.Files[n], nil
}

// Inbox is an account's or a project's receipts.
type Inbox struct {
	Access  tenancybus.Access
	Waiting []Receipt
	Matched []Receipt
	Removed []Receipt
}

// Inbox is one inbox, for a reader of its account or project.
func (b *Business) Inbox(ctx context.Context, actor types.ID, home types.Scope) (Inbox, error) {
	access, err := b.access.AccessTo(ctx, actor, home)

	switch {
	case err != nil:
		return Inbox{}, err
	case !access.Can(tenancybus.Read) || home.Kind != types.ScopeAccount && home.Kind != types.ScopeProject:
		return Inbox{}, ErrNotFound
	}

	all, err := b.store.InHomes(ctx, []types.Scope{home}, false)
	if err != nil {
		return Inbox{}, err
	}

	in := Inbox{Access: access}

	for _, r := range all {
		switch {
		case r.Removed():
			in.Removed = append(in.Removed, r)
		case r.Waiting():
			in.Waiting = append(in.Waiting, r)
		default:
			in.Matched = append(in.Matched, r)
		}
	}

	return in, nil
}

// Waiting is every waiting receipt in an inbox the actor may read: the
// treasurer's list of what is still to be matched.
func (b *Business) Waiting(ctx context.Context, actor types.ID) ([]Receipt, error) {
	ov, err := b.access.Overview(ctx, actor)
	if err != nil {
		return nil, err
	}

	return b.store.InHomes(ctx, homes(ov), true)
}

// homes is every account and project on an overview, as inboxes.
func homes(ov tenancybus.Overview) []types.Scope {
	var out []types.Scope

	add := func(accounts []tenancybus.Account, projects []tenancybus.Project) {
		for _, a := range accounts {
			out = append(out, a.Scope())
		}

		for _, p := range projects {
			out = append(out, p.Scope())
		}
	}

	add(ov.Accounts, ov.Projects)

	for _, o := range ov.Orgs {
		add(o.Accounts, o.Projects)
	}

	return out
}

// OnTransactions is the receipts on transactions, by transaction, asking
// nobody's permission: for a page that has already shown the reader those
// transactions, whose receipts they may therefore see.
func (b *Business) OnTransactions(ctx context.Context, ids []types.ID) (map[types.ID][]Receipt, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	receipts, err := b.store.OnTransactions(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := map[types.ID][]Receipt{}

	for _, r := range receipts {
		for _, l := range r.Links {
			if slices.Contains(ids, l.TransactionID) {
				out[l.TransactionID] = append(out[l.TransactionID], r)
			}
		}
	}

	return out, nil
}

// Suggestions is, for each receipt with an amount and a date, the
// transactions it might be: in an account the actor may attach receipts
// to, for the same amount, within MatchDays, nearest first. A check image
// with a number its account has several transactions for is waiting for
// a person to say which (checks.go), and those are its suggestions.
func (b *Business) Suggestions(ctx context.Context, actor types.ID, receipts []Receipt) (map[types.ID][]ledgerbus.Transaction, error) {
	ov, err := b.access.Overview(ctx, actor)
	if err != nil {
		return nil, err
	}

	var accounts []types.ID

	for _, h := range homes(ov) {
		if h.Kind != types.ScopeAccount {
			continue
		}

		if ok, err := b.can(ctx, actor, h, tenancybus.Receipts); err != nil {
			return nil, err
		} else if ok {
			accounts = append(accounts, h.ID)
		}
	}

	out := map[types.ID][]ledgerbus.Transaction{}

	for _, r := range receipts {
		if r.CheckImage && r.Check != "" && slices.Contains(accounts, r.Home.ID) && r.Waiting() {
			txs, err := b.ledger.WithCheck(ctx, r.Home.ID, r.Check)
			if err != nil {
				return nil, err
			}

			if len(txs) > 0 {
				out[r.ID] = txs
			}

			continue
		}

		if !r.HasAmount || r.SpentOn.Zero() || r.Removed() {
			continue
		}

		txs, err := b.ledger.Matching(ctx, accounts, r.Amount, r.SpentOn, MatchDays)
		if err != nil {
			return nil, err
		}

		txs = slices.DeleteFunc(txs, func(t ledgerbus.Transaction) bool { return r.AttachedTo(t.ID) })

		slices.SortStableFunc(txs, func(a, c ledgerbus.Transaction) int {
			return cmp.Compare(days(a.PostedOn, r.SpentOn), days(c.PostedOn, r.SpentOn))
		})

		if len(txs) > 0 {
			out[r.ID] = txs
		}
	}

	return out, nil
}

// days is how many days apart two dates are.
func days(a, b types.Date) int {
	ta, _ := time.Parse("2006-01-02", a.String())
	tb, _ := time.Parse("2006-01-02", b.String())

	d := int(ta.Sub(tb).Hours() / 24)
	if d < 0 {
		return -d
	}

	return d
}

// --- changing -----------------------------------------------------------------

// Attach puts a receipt on a transaction.
func (b *Business) Attach(ctx context.Context, now time.Time, actor, receiptID, transactionID types.ID) (Receipt, error) {
	r, err := b.readable(ctx, actor, receiptID)
	if err != nil {
		return Receipt{}, err
	}

	if r.Removed() {
		return Receipt{}, ErrRemoved
	}

	_, may, err := b.transaction(ctx, actor, transactionID)
	if err != nil {
		return Receipt{}, err
	}

	if !may {
		return Receipt{}, ErrForbidden
	}

	return r, b.store.Link(ctx, r.ID, Link{TransactionID: transactionID, LinkedBy: actor, LinkedAt: now})
}

// Detach takes a receipt off a transaction. Back in its inbox, waiting,
// when it was on no other.
func (b *Business) Detach(ctx context.Context, actor, receiptID, transactionID types.ID) (Receipt, error) {
	r, err := b.readable(ctx, actor, receiptID)
	if err != nil {
		return Receipt{}, err
	}

	_, may, err := b.transaction(ctx, actor, transactionID)
	if err != nil {
		return Receipt{}, err
	}

	if !may {
		return Receipt{}, ErrForbidden
	}

	return r, b.store.Unlink(ctx, r.ID, transactionID)
}

// SetDetails corrects what is known about a receipt. A check's image's
// shop is whom the check was paid to, and goes to its transaction as well
// (checks.go).
func (b *Business) SetDetails(ctx context.Context, now time.Time, actor, id types.ID, d Details) (Receipt, error) {
	r, err := b.readable(ctx, actor, id)
	if err != nil {
		return Receipt{}, err
	}

	if ok, err := b.mayEdit(ctx, actor, r); err != nil {
		return Receipt{}, err
	} else if !ok {
		return Receipt{}, ErrForbidden
	}

	if err := d.check(); err != nil {
		return r, err
	}

	d.Memo = strings.Join(strings.Fields(d.Memo), " ")
	if !r.CheckImage {
		d.Memo, d.WrittenOn = "", types.Date{}
	}

	before := r
	r.Details = d

	if err := b.store.Update(ctx, r, nil); err != nil {
		return r, err
	}

	return r, b.paidTo(ctx, now, actor, before, r)
}

// SetRemoved takes a waiting receipt out of its inbox, or puts it back.
func (b *Business) SetRemoved(ctx context.Context, now time.Time, actor, id types.ID, removed bool) (Receipt, error) {
	r, err := b.readable(ctx, actor, id)
	if err != nil {
		return Receipt{}, err
	}

	if ok, err := b.mayRemove(ctx, actor, r); err != nil {
		return Receipt{}, err
	} else if !ok {
		return Receipt{}, ErrForbidden
	}

	if r.Removed() == removed {
		return r, nil
	}

	if removed && len(r.Links) > 0 {
		return r, ErrAttached
	}

	action := Restored

	r.RemovedAt = time.Time{}
	if removed {
		r.RemovedAt, action = now, Removed
	}

	ev := eventbus.New(now, actor, r.Home, action, map[string]string{"what": r.Label()})

	return r, b.store.Update(ctx, r, &ev)
}

// labelNote is how much of a note can name a receipt: a heading's worth,
// not the paragraph a note may be.
const labelNote = 60

// Label is how a receipt is named in a sentence and at the top of its page:
// the shop, or else the start of the note the uploader typed, or else its
// first file's name. The note comes before the file because a phone names
// every photo IMG_something, and "hostel, two nights" is what lets a
// treasurer tell a month later which receipt the history means.
func (r Receipt) Label() string {
	if r.Merchant != "" {
		return r.Merchant
	}

	if note, _, _ := strings.Cut(strings.TrimSpace(r.Note), "\n"); note != "" {
		if runes := []rune(note); len(runes) > labelNote {
			note = strings.TrimSpace(string(runes[:labelNote])) + "…"
		}

		return note
	}

	if len(r.Files) > 0 {
		return r.Files[0].Name
	}

	return ""
}
