package receiptbus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// shots are screenshots of checks, uploaded by the actor.
func (w *world) shots(actor types.ID, names ...string) []types.ID {
	w.t.Helper()

	ids := make([]types.ID, len(names))
	for i, name := range names {
		ids[i] = w.upload(actor, name, photo+name, receiptbus.Accept)
	}

	return ids
}

// A treasurer's own checks: shared to an inbox that is theirs, seen by
// nobody else, and filed under an account by choosing it, which numbers
// and attaches the check as adding it there would have. Filed once only;
// removed and brought back while it waits, and not once it is filed.
func TestACheckFiledFromTheInbox(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	in, err := w.receipts.AddToCheckInbox(w.ctx, now, w.owner, w.shots(w.owner, "Screenshot_1.png", "Screenshot_2.png", "1180.png"))
	if err != nil || len(in) != 3 {
		t.Fatalf("added: %+v, %v", in, err)
	}

	box, err := w.receipts.CheckInbox(w.ctx, w.owner)
	if err != nil || len(box.Waiting) != 3 || len(box.Filed)+len(box.Removed) != 0 {
		t.Fatalf("the inbox: %+v, %v", box, err)
	}

	// Nobody else's: not the stranger's, not the pilgrim's.
	for _, who := range []types.ID{w.stranger, w.pilgrim} {
		if box, _ := w.receipts.CheckInbox(w.ctx, who); len(box.Waiting) != 0 {
			t.Errorf("somebody else sees the treasurer's checks: %+v", box)
		}

		if _, err := w.receipts.InboxCheckFile(w.ctx, who, in[0].ID, 0); !errors.Is(err, receiptbus.ErrNotFound) {
			t.Errorf("somebody else's image: %v", err)
		}

		if _, err := w.receipts.FileInboxCheck(w.ctx, now, who, in[0].ID, acct, "1176"); !errors.Is(err, receiptbus.ErrNotFound) {
			t.Errorf("somebody else filed it: %v", err)
		}
	}

	if f, err := w.receipts.InboxCheckFile(w.ctx, w.owner, in[0].ID, 0); err != nil || f.Name != "Screenshot_1.png" {
		t.Errorf("the image: %+v, %v", f, err)
	}

	if _, err := w.receipts.InboxCheckFile(w.ctx, w.owner, in[0].ID, 1); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a second page: %v", err)
	}

	// The same bytes again are there already.
	if held, err := w.receipts.CheckInboxHolds(w.ctx, w.owner, w.shots(w.owner, "Screenshot_1.png")[0]); err != nil || !held {
		t.Errorf("sent again: %v, %v", held, err)
	}

	// Filed with its number typed: attached, with the bank's date and
	// amount, and a line in the account's history.
	r, err := w.receipts.FileInboxCheck(w.ctx, now, w.owner, in[0].ID, acct, "1176")
	if err != nil || r.Home != types.AccountScope(acct) || !r.CheckImage || r.Check != "1176" || r.Waiting() || r.Amount.String() != "120.00" {
		t.Fatalf("filed: %+v, %v", r, err)
	}

	if _, err := w.receipts.FileInboxCheck(w.ctx, now, w.owner, in[0].ID, acct, "1176"); !errors.Is(err, receiptbus.ErrFiled) {
		t.Errorf("filed twice: %v", err)
	}

	events, _ := w.history.Recent(w.ctx, types.AccountScope(acct), 10)
	if len(events) == 0 || events[0].Action != receiptbus.Filed || events[0].Detail["number"] != "1176" {
		t.Errorf("the account's history: %+v", events)
	}

	// Numbered by its file's name; and with no number, it waits in the
	// account as a check image like any other.
	if r, err := w.receipts.FileInboxCheck(w.ctx, now, w.owner, in[2].ID, acct, ""); err != nil || r.Check != "1180" || !r.Waiting() {
		t.Errorf("1180.png: %+v, %v", r, err)
	}

	if _, err := w.receipts.FileInboxCheck(w.ctx, now, w.owner, in[1].ID, acct, "12a"); err == nil {
		t.Error("a number that is no number was taken")
	}

	// Removed and brought back while it waits.
	if c, err := w.receipts.SetInboxCheckRemoved(w.ctx, now, w.owner, in[1].ID, true); err != nil || !c.Removed() {
		t.Fatalf("removed: %+v, %v", c, err)
	}

	if _, err := w.receipts.FileInboxCheck(w.ctx, now, w.owner, in[1].ID, acct, ""); !errors.Is(err, receiptbus.ErrFiled) {
		t.Errorf("a removed check was filed: %v", err)
	}

	if held, _ := w.receipts.CheckInboxHolds(w.ctx, w.owner, w.shots(w.owner, "Screenshot_2.png")[0]); held {
		t.Error("a removed check still counts as there")
	}

	if _, err := w.receipts.SetInboxCheckRemoved(w.ctx, now, w.owner, in[1].ID, false); err != nil {
		t.Fatal(err)
	}

	if _, err := w.receipts.SetInboxCheckRemoved(w.ctx, now, w.owner, in[0].ID, true); !errors.Is(err, receiptbus.ErrFiled) {
		t.Errorf("a filed check was removed: %v", err)
	}

	box, _ = w.receipts.CheckInbox(w.ctx, w.owner)
	if len(box.Waiting) != 1 || len(box.Filed) != 2 || box.Filed[0].ReceiptID.Zero() {
		t.Errorf("the inbox afterwards: %+v", box)
	}

	// Into an account somebody cannot see, or may only read.
	theirs, _ := w.receipts.AddToCheckInbox(w.ctx, now, w.pilgrim, w.shots(w.pilgrim, "Screenshot_9.png"))

	if _, err := w.receipts.FileInboxCheck(w.ctx, now, w.pilgrim, theirs[0].ID, acct, ""); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("into an account the pilgrim cannot see: %v", err)
	}

	w.grant(types.AccountScope(acct), "pilgrim@example.org", tenancybus.Viewer)

	if _, err := w.receipts.FileInboxCheck(w.ctx, now, w.pilgrim, theirs[0].ID, acct, ""); !errors.Is(err, receiptbus.ErrForbidden) {
		t.Errorf("into an account the pilgrim may only read: %v", err)
	}
}

// Claude files a stack from the treasurer's own checks by the account
// number on each: the account whose last four digits it ends in, then
// read there, held to the bank's amount. An account number that names no
// account, or two, or a misread amount, moves nothing.
func TestClaudeFilesChecksByTheirAccount(t *testing.T) {
	w := newWorld(t)
	main := w.checkingEnding("Parish checking", "4321")
	school := w.checkingEnding("School checking", "8765")

	in, err := w.receipts.AddToCheckInbox(w.ctx, now, w.owner, w.shots(w.owner, "Screenshot_1.png", "Screenshot_2.png", "Screenshot_3.png", "Screenshot_4.png"))
	if err != nil {
		t.Fatal(err)
	}

	read := func(i int, number, amount, account string) (receiptbus.Receipt, error) {
		t.Helper()

		a, err := money.Parse(amount)
		if err != nil {
			t.Fatal(err)
		}

		return w.receipts.ReadInboxCheck(w.ctx, now, w.owner, in[i].ID, receiptbus.CheckReading{Number: number, Amount: a, Payee: "Hilltop Plumbing"}, receiptbus.OnFace{Account: account})
	}

	// From the school's account: its number as the line at a check's foot
	// prints it, symbol and all.
	r, err := read(0, "1176", "120.00", "9999 0000 8765⑈")
	if err != nil || r.Home != types.AccountScope(school) || r.Waiting() || r.Check != "1176" || r.Merchant != "Hilltop Plumbing" {
		t.Fatalf("the school's check: %+v, %v", r, err)
	}

	// Filed, read, and its payee on the transaction, newest first.
	events, _ := w.history.Recent(w.ctx, types.AccountScope(school), 10)
	if len(events) < 3 || events[1].Action != receiptbus.CheckRead || events[2].Action != receiptbus.Filed {
		t.Errorf("the school account's history: %+v", events)
	}

	// The parish's, not cleared yet: it waits there, with what was read.
	if r, err := read(1, "1195", "75.00", "9999-4321"); err != nil || r.Home != types.AccountScope(main) || !r.Waiting() || r.Check != "1195" {
		t.Errorf("the parish's check, not cleared: %+v, %v", r, err)
	}

	// Refused, each leaving the image where it was.
	_, err = read(2, "1177", "300.00", "9999-4321")
	if _, ok := errors.AsType[receiptbus.AmountDiffers](err); !ok {
		t.Errorf("a misread amount: %v", err)
	}

	_, err = read(2, "1177", "30.00", "9999-0000")
	if e, ok := errors.AsType[receiptbus.AccountUnknown](err); !ok || e.Last4 != "0000" || e.Matches != 0 {
		t.Errorf("an account number nobody has: %v", err)
	}

	if _, err := read(2, "1177", "30.00", "321"); err == nil {
		t.Error("three digits were taken for an account number")
	}

	if _, err := read(2, "", "30.00", "9999-4321"); err == nil {
		t.Error("a reading with no number was taken")
	}

	w.checkingEnding("Another bank", "4321")

	_, err = read(2, "1177", "30.00", "9999-4321")
	if e, ok := errors.AsType[receiptbus.AccountUnknown](err); !ok || e.Matches != 2 {
		t.Errorf("two accounts ending 4321: %v", err)
	}

	box, _ := w.receipts.CheckInbox(w.ctx, w.owner)
	if len(box.Waiting) != 2 || len(box.Filed) != 2 {
		t.Errorf("after the refusals: %d waiting, %d filed", len(box.Waiting), len(box.Filed))
	}

	// A viewer of an account cannot file into it, and is told why.
	w.grant(types.AccountScope(school), "pilgrim@example.org", tenancybus.Viewer)
	theirs, _ := w.receipts.AddToCheckInbox(w.ctx, now, w.pilgrim, w.shots(w.pilgrim, "Screenshot_9.png"))

	_, err = w.receipts.ReadInboxCheck(w.ctx, now, w.pilgrim, theirs[0].ID, receiptbus.CheckReading{Number: "1177", Amount: 3000}, receiptbus.OnFace{Account: "9999 8765"})
	if e, ok := errors.AsType[receiptbus.AccountUnknown](err); !ok || !e.ReadOnly {
		t.Errorf("the viewer: %v", err)
	}

	// And nobody reads somebody else's.
	if _, err := w.receipts.ReadInboxCheck(w.ctx, now, w.stranger, in[2].ID, receiptbus.CheckReading{Number: "1177", Amount: 3000}, receiptbus.OnFace{Account: "9999 4321"}); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("the stranger: %v", err)
	}

	// What else the check says (docs/phone.md, 7) comes with it: a memo
	// too long for the transaction is refused before anything moves, and
	// the memo and the day written are kept on the check filed.
	written, _ := types.ParseDate("2026-09-23")
	reading := receiptbus.CheckReading{Number: "1200", Amount: 4500, On: written, Memo: strings.Repeat("m", ledgerbus.MaxCheckMemo+1)}

	if _, err := w.receipts.ReadInboxCheck(w.ctx, now, w.owner, in[3].ID, reading, receiptbus.OnFace{Account: "9999 8765"}); err == nil {
		t.Error("a memo too long was taken")
	}

	if box, _ := w.receipts.CheckInbox(w.ctx, w.owner); len(box.Waiting) != 2 {
		t.Errorf("the memo too long moved the check: %d waiting", len(box.Waiting))
	}

	reading.Memo = "Summer work"
	if r, err := w.receipts.ReadInboxCheck(w.ctx, now, w.owner, in[3].ID, reading, receiptbus.OnFace{Account: "9999 8765"}); err != nil || r.Memo != "Summer work" || r.WrittenOn != written {
		t.Errorf("the memo and the day written: %+v, %v", r, err)
	}
}
