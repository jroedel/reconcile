package receiptbus_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
)

// One check, one receipt: every way an image of a check still waiting in
// its account can arrive -- uploaded again, numbered on its page, read by
// Claude, filed from a person's own checks -- makes it more pages of that
// check, so that the checks outstanding count it once. What the joined
// image said that the check did not is kept. A check already on its
// transaction is not joined: a second image of it is attached beside it.
func TestOneCheckOneReceipt(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()
	home := types.AccountScope(acct)

	image := func(name string) types.ID { return w.upload(w.owner, name, photo+name, receiptbus.Accept) }

	first, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1190-front.jpg")}, "", "")
	if err != nil || len(first) != 1 || first[0].Check != "1190" || !first[0].Waiting() {
		t.Fatalf("the first image: %+v, %v", first, err)
	}

	check := first[0].ID

	// Its back, uploaded later, with whom it was paid to.
	back, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1190-back.jpg")}, "", "Hilltop Plumbing")
	if err != nil || len(back) != 1 || back[0].ID != check || len(back[0].Files) != 2 || back[0].Merchant != "Hilltop Plumbing" {
		t.Fatalf("the back: %+v, %v", back, err)
	}

	// A screenshot with a memo typed, numbered on its page.
	shots, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("Screenshot_1.png"), image("Screenshot_2.png")}, "", "")
	if err != nil || len(shots) != 2 {
		t.Fatalf("the screenshots: %+v, %v", shots, err)
	}

	if _, err := w.receipts.SetDetails(w.ctx, now, w.owner, shots[0].ID, receiptbus.Details{Memo: "Gutters"}); err != nil {
		t.Fatal(err)
	}

	numbered, err := w.receipts.NumberCheck(w.ctx, now, w.owner, shots[0].ID, "1190")
	if err != nil || numbered.ID != check || len(numbered.Files) != 3 || numbered.Memo != "Gutters" || numbered.Merchant != "Hilltop Plumbing" {
		t.Fatalf("numbered: %+v, %v", numbered, err)
	}

	if v, err := w.receipts.Receipt(w.ctx, w.owner, shots[0].ID); err != nil || !v.Receipt.Removed() {
		t.Errorf("the numbered screenshot should be removed: %+v, %v", v.Receipt, err)
	}

	// The other, read by Claude: the reading is the check's.
	read, err := w.receipts.ReadCheck(w.ctx, now, w.owner, shots[1].ID, receiptbus.CheckReading{Number: "1190", Amount: 4500})
	if err != nil || read.ID != check || len(read.Files) != 4 || read.Amount.String() != "45.00" || !read.Waiting() {
		t.Fatalf("read: %+v, %v", read, err)
	}

	// From the treasurer's own checks, filed with its number.
	in, err := w.receipts.AddToCheckInbox(w.ctx, now, w.owner, w.shots(w.owner, "cheque 1190 verso.png"))
	if err != nil {
		t.Fatal(err)
	}

	filed, err := w.receipts.FileInboxCheck(w.ctx, now, w.owner, in[0].ID, acct, "")
	if err != nil || filed.ID != check || len(filed.Files) != 5 {
		t.Fatalf("filed: %+v, %v", filed, err)
	}

	if box, _ := w.receipts.CheckInbox(w.ctx, w.owner); len(box.Waiting) != 0 || len(box.Filed) != 1 {
		t.Errorf("the check inbox: %d waiting, %d filed", len(box.Waiting), len(box.Filed))
	}

	// One check outstanding, for its amount once.
	out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{})
	if err != nil || len(out.Checks) != 1 || out.Checks[0].ID != check || out.Total.String() != "45.00" || out.Unpriced != 0 {
		t.Errorf("outstanding: %+v, %v", out, err)
	}

	in2, err := w.receipts.Inbox(w.ctx, w.owner, home)
	if err != nil || len(in2.Waiting) != 1 {
		t.Errorf("the account's inbox: %d waiting, %v", len(in2.Waiting), err)
	}

	// A check on its transaction already: a second image is a second
	// receipt, attached beside the first.
	paid, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1176.jpg")}, "", "")
	if err != nil || len(paid) != 1 || paid[0].Waiting() {
		t.Fatalf("check 1176: %+v, %v", paid, err)
	}

	again, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1176-back.jpg")}, "", "")
	if err != nil || len(again) != 1 || again[0].ID == paid[0].ID || again[0].Waiting() {
		t.Errorf("check 1176 again: %+v, %v", again, err)
	}
}
