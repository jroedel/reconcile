package receiptbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A person's own check inbox (docs/phone.md, 7). At a month's end a
// treasurer has a stack of checks from several accounts -- fifty
// screenshots from a bank's app, or a pile photographed at a desk -- and
// choosing an account for each before sharing them is the work the stack
// was meant to save. So they can share the lot to an inbox that is
// theirs, and each check is filed under its account afterwards: by their
// Claude, which reads the account number printed on the check's face, or
// by them, choosing the account on the inbox's page.
//
// It holds the images of checks and nothing else. An inbox only its
// uploader can see is a place where a receipt can be stranded: forgotten,
// or left behind by somebody who loses their role, where nobody else will
// ever find it. Ordinary receipts therefore keep going into an account's
// or a project's inbox, where the treasurer sees what a volunteer put
// there; a check image is the one thing whose account is printed on it,
// and that its uploader, or their Claude, files at once. Checks may still
// be added straight to an account's check images (checks.go) by somebody
// who would rather choose there.
//
// It is the statement inbox's shape (ledgerbus, inbox.go): a person's list
// of files, not a third kind of receipt home. Filed, an item becomes a
// check image in the account, as though it had been added there, and the
// item keeps which receipt it became. Until then it has no history,
// because the history is an organization's, an account's or a project's,
// and this is none of them; filing it writes the line in the account's.

// InboxCheck is one image in a person's own check inbox.
type InboxCheck struct {
	ID      types.ID
	UserID  types.ID
	File    filebus.File
	AddedAt time.Time

	// RemovedAt is when its person took it out, zero while it is not.
	RemovedAt time.Time

	// FiledAt is when it was filed into an account, as ReceiptID; zero
	// while it waits.
	FiledAt   time.Time
	ReceiptID types.ID
}

// Waiting reports whether it is still to be filed.
func (c InboxCheck) Waiting() bool { return c.FiledAt.IsZero() && c.RemovedAt.IsZero() }

// Removed reports whether its person took it out.
func (c InboxCheck) Removed() bool { return !c.RemovedAt.IsZero() }

// CheckInbox is a person's own check inbox, as its page lists it.
type CheckInbox struct {
	Waiting []InboxCheck
	Filed   []InboxCheck
	Removed []InboxCheck
}

// AddToCheckInbox puts uploaded files into the actor's own check inbox,
// each the image of a check of its own. The files must be the actor's.
func (b *Business) AddToCheckInbox(ctx context.Context, now time.Time, actor types.ID, fileIDs []types.ID) ([]InboxCheck, error) {
	if len(fileIDs) == 0 {
		return nil, Invalid{Field: "files", Err: errors.New("there are no files")}
	}

	checks := make([]InboxCheck, len(fileIDs))

	for i, id := range fileIDs {
		f, err := b.ownFile(ctx, actor, id)
		if err != nil {
			return nil, err
		}

		checks[i] = InboxCheck{ID: types.NewID(), UserID: actor, File: f, AddedAt: now}
	}

	return checks, b.store.AddInboxChecks(ctx, checks)
}

// ownFile is an uploaded file of the actor's, or ErrNotFound.
func (b *Business) ownFile(ctx context.Context, actor, id types.ID) (filebus.File, error) {
	f, err := b.files.ByID(ctx, id)

	switch {
	case errors.Is(err, filebus.ErrNotFound) || err == nil && f.UploadedBy != actor:
		return filebus.File{}, ErrNotFound
	case err != nil:
		return filebus.File{}, err
	}

	return f, nil
}

// CheckInboxHolds reports whether the actor's check inbox already has an
// image, not removed, with the same bytes as an uploaded file: the share
// page's question before it adds one, as Holds is for an inbox's
// receipts, so that sending again is always safe. A check filed already
// counts: its image is in an account, and adding it again would file it
// twice.
func (b *Business) CheckInboxHolds(ctx context.Context, actor, fileID types.ID) (bool, error) {
	f, err := b.ownFile(ctx, actor, fileID)
	if err != nil {
		return false, err
	}

	return b.store.InboxHolds(ctx, actor, f.SHA256)
}

// CheckInbox is the actor's own check inbox.
func (b *Business) CheckInbox(ctx context.Context, actor types.ID) (CheckInbox, error) {
	all, err := b.store.InboxChecks(ctx, actor)
	if err != nil {
		return CheckInbox{}, err
	}

	var in CheckInbox

	for _, c := range all {
		switch {
		case c.Removed():
			in.Removed = append(in.Removed, c)
		case c.Waiting():
			in.Waiting = append(in.Waiting, c)
		default:
			in.Filed = append(in.Filed, c)
		}
	}

	return in, nil
}

// inboxCheck is an item of the actor's own check inbox, or ErrNotFound:
// nobody else's, whoever they are.
func (b *Business) inboxCheck(ctx context.Context, actor, id types.ID) (InboxCheck, error) {
	c, err := b.store.InboxCheckByID(ctx, id)
	if err != nil {
		return InboxCheck{}, err
	}

	if c.UserID != actor {
		return InboxCheck{}, ErrNotFound
	}

	return c, nil
}

// InboxCheckFile is the image of an item of the actor's check inbox. It
// has one page, n zero; any other is ErrNotFound.
func (b *Business) InboxCheckFile(ctx context.Context, actor, id types.ID, n int) (filebus.File, error) {
	c, err := b.inboxCheck(ctx, actor, id)
	if err != nil {
		return filebus.File{}, err
	}

	if n != 0 {
		return filebus.File{}, ErrNotFound
	}

	return c.File, nil
}

// SetInboxCheckRemoved takes a waiting check out of the actor's inbox,
// or brings it back. One filed is ErrFiled: it is a receipt in its
// account now, and removed there if it is a mistake.
func (b *Business) SetInboxCheckRemoved(ctx context.Context, now time.Time, actor, id types.ID, removed bool) (InboxCheck, error) {
	c, err := b.inboxCheck(ctx, actor, id)
	if err != nil {
		return InboxCheck{}, err
	}

	if !c.FiledAt.IsZero() {
		return c, ErrFiled
	}

	c.RemovedAt = time.Time{}
	if removed {
		c.RemovedAt = now
	}

	return c, b.store.SetInboxCheckRemoved(ctx, c.ID, c.RemovedAt)
}

// FileInboxCheck files a check from the actor's inbox into an account,
// as a person does on the inbox's page: it becomes a check image there,
// as though it had been added to the account's check images (AddChecks),
// numbered as typed, or by its file's name, and attached to the account's
// one transaction with that number. Filing takes the Receipts permission
// on the account.
func (b *Business) FileInboxCheck(ctx context.Context, now time.Time, actor, id, accountID types.ID, number string) (Receipt, error) {
	c, err := b.waitingInboxCheck(ctx, actor, id)
	if err != nil {
		return Receipt{}, err
	}

	given := importbus.CheckNumber(number)
	if strings.TrimSpace(number) != "" && given == "" {
		return Receipt{}, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	}

	return b.file(ctx, now, actor, c, accountID, cmp.Or(given, NumberFromName(c.File.Name)))
}

// waitingInboxCheck is an item of the actor's check inbox still to file.
func (b *Business) waitingInboxCheck(ctx context.Context, actor, id types.ID) (InboxCheck, error) {
	c, err := b.inboxCheck(ctx, actor, id)
	if err != nil {
		return InboxCheck{}, err
	}

	if !c.Waiting() {
		return c, ErrFiled
	}

	return c, nil
}

// file makes an item of the actor's check inbox a check image in the
// account, numbered and attached when a number is given and the account
// has one transaction with it, and writes the line in the account's
// history that says it came from the actor's own checks.
func (b *Business) file(ctx context.Context, now time.Time, actor types.ID, c InboxCheck, accountID types.ID, number string) (Receipt, error) {
	home := types.AccountScope(accountID)

	access, err := b.access.AccessTo(ctx, actor, home)

	switch {
	case err != nil:
		return Receipt{}, err
	case !access.Can(tenancybus.Read):
		return Receipt{}, ErrNotFound
	case !access.Can(tenancybus.Receipts):
		return Receipt{}, ErrForbidden
	}

	r := Receipt{ID: types.NewID(), Home: home, UploadedBy: actor, CreatedAt: now, CheckImage: true, Check: number, Files: []filebus.File{c.File}}

	t, ok, err := b.paidBy(ctx, accountID, number)
	if err != nil {
		return Receipt{}, err
	}

	if ok {
		r = matched(r, t, actor, now)
	}

	ev := eventbus.New(now, actor, home, Filed, map[string]string{"number": number})

	return r, b.store.FileInboxCheck(ctx, c.ID, r, ev)
}

// OnFace is what a check's face says of the account it is drawn on: the
// account number in the line of digits at its foot, as printed, spaces,
// dashes and the line's symbols allowed.
//
// It is used to find the account and then dropped. The site stores the
// last four digits of an account's number and never the rest
// (tenancybus.Account), so the rest is compared with nothing, kept
// nowhere and said back to nobody.
type OnFace struct {
	Account string
}

// AccountUnknown is an account number read off a check that names no one
// account the actor may file it in: no account of theirs ends in its last
// four digits, or several do, or those that do are ones they may only
// read. Nothing is changed.
type AccountUnknown struct {
	Last4    string
	Matches  int  // how many of the accounts they may file in end so: 0, or more than 1
	ReadOnly bool // some they may read end so, and they may file in none of them
}

func (e AccountUnknown) Error() string {
	switch {
	case e.ReadOnly:
		return fmt.Sprintf("the account ending %s is not one you may add receipts to", e.Last4)
	case e.Matches > 1:
		return fmt.Sprintf("%d accounts end %s", e.Matches, e.Last4)
	}

	return fmt.Sprintf("no account of yours ends %s", e.Last4)
}

// accountOnFace is the one account, among those the actor may add
// receipts to, whose last four digits are the account number's on a
// check.
func (b *Business) accountOnFace(ctx context.Context, actor types.ID, face OnFace) (tenancybus.Account, error) {
	digits := strings.Map(func(r rune) rune {
		if '0' <= r && r <= '9' {
			return r
		}

		return -1
	}, face.Account)

	if len(digits) < 4 {
		return tenancybus.Account{}, Invalid{Field: "account_number", Err: errors.New("the account number is the digits printed after the routing number at the check's foot")}
	}

	last4 := digits[len(digits)-4:]

	ov, err := b.access.Overview(ctx, actor)
	if err != nil {
		return tenancybus.Account{}, err
	}

	var (
		mayFile  []tenancybus.Account
		readable int
	)

	for _, a := range accountsOf(ov) {
		if a.Last4 != last4 {
			continue
		}

		readable++

		if ok, err := b.can(ctx, actor, a.Scope(), tenancybus.Receipts); err != nil {
			return tenancybus.Account{}, err
		} else if ok {
			mayFile = append(mayFile, a)
		}
	}

	if len(mayFile) != 1 {
		return tenancybus.Account{}, AccountUnknown{Last4: last4, Matches: len(mayFile), ReadOnly: len(mayFile) == 0 && readable > 0}
	}

	return mayFile[0], nil
}

// accountsOf is every account on an overview.
func accountsOf(ov tenancybus.Overview) []tenancybus.Account {
	out := append([]tenancybus.Account(nil), ov.Accounts...)

	for _, o := range ov.Orgs {
		out = append(out, o.Accounts...)
	}

	return out
}

// ReadInboxCheck is ReadCheck for a check in the actor's own inbox, as
// their Claude reads it through the API: the account number on its face
// says which account it is filed in, and then it is read there, held to
// the bank's amount, exactly as a check image added to that account
// would be.
//
// Everything that can refuse is asked before anything moves: the reading
// (a number, an amount, a payee and a memo that fit), the account (one,
// and one the actor may add receipts to: AccountUnknown otherwise), and
// the bank's amount for the check with that number in it (AmountDiffers).
// So a misread digit, of the account's number or the check's, leaves the
// image where it was, for Claude to look at again or the person to file.
// The account's history gets two lines: filed from the person's checks,
// and read.
func (b *Business) ReadInboxCheck(ctx context.Context, now time.Time, actor, id types.ID, rd CheckReading, face OnFace) (Receipt, error) {
	c, err := b.waitingInboxCheck(ctx, actor, id)
	if err != nil {
		return Receipt{}, err
	}

	n := importbus.CheckNumber(rd.Number)

	switch {
	case n == "":
		return Receipt{}, Invalid{Field: "number", Err: errors.New("a check number is digits")}
	case rd.Amount <= 0:
		return Receipt{}, Invalid{Field: "amount", Err: errors.New("a check's amount is more than nothing")}
	case utf8.RuneCountInString(strings.Join(strings.Fields(rd.Payee), " ")) > ledgerbus.MaxPayee:
		return Receipt{}, Invalid{Field: "payee", Err: fmt.Errorf("at most %d characters", ledgerbus.MaxPayee)}
	case utf8.RuneCountInString(strings.Join(strings.Fields(rd.Memo), " ")) > ledgerbus.MaxCheckMemo:
		return Receipt{}, Invalid{Field: "memo", Err: fmt.Errorf("at most %d characters", ledgerbus.MaxCheckMemo)}
	}

	acct, err := b.accountOnFace(ctx, actor, face)
	if err != nil {
		return Receipt{}, err
	}

	txs, err := b.ledger.WithCheck(ctx, acct.ID, n)
	if err != nil {
		return Receipt{}, err
	}

	if len(txs) == 1 && txs[0].Amount.Abs() != rd.Amount {
		return Receipt{}, AmountDiffers{Number: n, Bank: txs[0].Amount.Abs(), Read: rd.Amount}
	}

	// Filed with no number, so that nothing is attached on the file's
	// name alone; the reading numbers it and attaches it.
	r, err := b.file(ctx, now, actor, c, acct.ID, "")
	if err != nil {
		return Receipt{}, err
	}

	return b.ReadCheck(ctx, now, actor, r.ID, rd)
}
