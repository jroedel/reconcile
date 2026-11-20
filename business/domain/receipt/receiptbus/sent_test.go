package receiptbus_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

func day(t *testing.T, s string) types.Date {
	t.Helper()

	d, err := types.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}

	return d
}

// A check a program printed: kept outstanding with all it said, or
// attached at once when the bank paid the number for its amount. Sent
// again, it is the same check; its number saying anything else is
// refused, as is a number the bank paid for another amount.
func TestSendingAPrintedCheck(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	printed := func(name string) types.ID { return w.upload(w.owner, name, photo+name, receiptbus.Accept) }
	reading := receiptbus.CheckReading{Number: "1190", Amount: 1234, Payee: "Hilltop  Plumbing", On: day(t, "2026-08-05"), Memo: "Supplies"}

	sent, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, printed("check-1190.png"), reading)
	if err != nil || sent.Again {
		t.Fatalf("sent: %+v, %v", sent, err)
	}

	c := sent.Check
	if c.Check != "1190" || !c.Waiting() || c.Amount.String() != "12.34" || c.Merchant != "Hilltop Plumbing" || c.Memo != "Supplies" ||
		c.WrittenOn.String() != "2026-08-05" || c.SpentOn.String() != "2026-08-05" || len(c.Files) != 1 {
		t.Errorf("the check: %+v", c)
	}

	out, err := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{})
	if err != nil || len(out.Checks) != 1 || out.Total.String() != "12.34" {
		t.Errorf("outstanding: %+v, %v", out, err)
	}

	// Again, drawn again (new bytes), with the payee spelled in another
	// case: the same check, and nothing kept.
	reading.Payee = "hilltop plumbing"

	again, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, printed("check-1190-again.png"), reading)
	if err != nil || !again.Again || again.Check.ID != c.ID || len(again.Check.Files) != 1 {
		t.Errorf("sent again: %+v, %v", again, err)
	}

	// The number, saying something else.
	other := reading
	other.Amount = 999

	if _, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, printed("check-1190-other.png"), other); err == nil {
		t.Error("a second check 1190 was kept")
	} else if used, ok := errors.AsType[receiptbus.NumberUsed](err); !ok || used.Existing.ID != c.ID {
		t.Errorf("a second check 1190: %v", err)
	}

	// Paid already: attached at once, and what it says written on the
	// transaction.
	paid, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, printed("check-1176.png"),
		receiptbus.CheckReading{Number: "1176", Amount: 12000, Payee: "Diocesan Office", On: day(t, "2026-06-28"), Memo: "Assessment"})
	if err != nil || paid.Check.Waiting() || paid.Check.SpentOn.String() != "2026-07-02" || paid.Check.WrittenOn.String() != "2026-06-28" {
		t.Fatalf("check 1176: %+v, %v", paid, err)
	}

	if tx, _ := w.ledger.Lookup(w.ctx, paid.Check.Links[0].TransactionID); tx.Payee != "Diocesan Office" || tx.CheckMemo != "Assessment" {
		t.Errorf("the transaction: %+v", tx)
	}

	// Paid for another amount: refused with both, nothing kept.
	_, err = w.receipts.SendCheck(w.ctx, now, w.owner, acct, printed("check-1177.png"),
		receiptbus.CheckReading{Number: "1177", Amount: 3100, Payee: "Somebody", On: day(t, "2026-07-01")})
	if differs, ok := errors.AsType[receiptbus.AmountDiffers](err); !ok || differs.Bank != money.Amount(3000) {
		t.Errorf("check 1177 for 31.00: %v", err)
	}

	if st, _ := w.receipts.CheckNumberState(w.ctx, w.owner, acct, "1177"); st.State != receiptbus.NumberCleared {
		t.Errorf("1177 after a refusal: %+v", st)
	}

	// What will not do.
	for name, rd := range map[string]receiptbus.CheckReading{
		"no number": {Amount: 100, Payee: "A", On: day(t, "2026-08-01")},
		"no amount": {Number: "1200", Payee: "A", On: day(t, "2026-08-01")},
		"no payee":  {Number: "1200", Amount: 100, On: day(t, "2026-08-01")},
		"no day":    {Number: "1200", Amount: 100, Payee: "A"},
	} {
		if _, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, printed(name+".png"), rd); err == nil {
			t.Errorf("%s: kept", name)
		} else if _, ok := errors.AsType[receiptbus.Invalid](err); !ok {
			t.Errorf("%s: %v", name, err)
		}
	}

	card, err := w.ten.CreateAccount(w.ctx, now, w.owner, w.org, tenancybus.AccountFields{Name: "Card", Kind: "card"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.receipts.SendCheck(w.ctx, now, w.owner, card.ID, printed("card.png"), reading); !errors.Is(err, receiptbus.ErrNotChecking) {
		t.Errorf("a check on a card: %v", err)
	}

	if _, err := w.receipts.SendCheck(w.ctx, now, w.stranger, acct, w.upload(w.stranger, "s.png", photo+"s", receiptbus.Accept), reading); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	// Somebody else's file is nobody's to send.
	if _, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, w.upload(w.stranger, "t.png", photo+"t", receiptbus.Accept), reading); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("another's file: %v", err)
	}
}

// A photo of a check, numbered before anybody said its amount, is
// completed by the printed check's sending rather than refused.
func TestSendingCompletesAPhoto(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	photoOf, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{w.upload(w.owner, "1190.jpg", photo+"1190", receiptbus.Accept)}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	sent, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, w.upload(w.owner, "printed.png", photo+"printed", receiptbus.Accept),
		receiptbus.CheckReading{Number: "1190", Amount: 4500, Payee: "Hilltop Plumbing", On: day(t, "2026-08-05")})
	if err != nil || sent.Check.ID != photoOf[0].ID || len(sent.Check.Files) != 2 || sent.Check.Amount.String() != "45.00" {
		t.Errorf("sent: %+v, %v", sent, err)
	}
}

// A spoiled check is voided: kept and listed, out of the checks
// outstanding, and its number free to print again. One that cleared
// cannot be; one voided by mistake is outstanding again with the mark
// off, unless its number has been printed since.
func TestVoidingACheck(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	send := func(number string, amount money.Amount) receiptbus.Receipt {
		t.Helper()

		s, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, w.upload(w.owner, number+".png", photo+number, receiptbus.Accept),
			receiptbus.CheckReading{Number: number, Amount: amount, Payee: "Hilltop Plumbing", On: day(t, "2026-08-05")})
		if err != nil {
			t.Fatal(err)
		}

		return s.Check
	}

	spoiled := send("1190", 1234)

	voided, err := w.receipts.VoidCheck(w.ctx, now, w.owner, acct, "01190")
	if err != nil || !voided.Voided() || voided.ID != spoiled.ID || voided.Waiting() {
		t.Fatalf("voided: %+v, %v", voided, err)
	}

	if again, err := w.receipts.VoidCheck(w.ctx, now, w.owner, acct, "1190"); err != nil || again.ID != spoiled.ID {
		t.Errorf("voided twice: %+v, %v", again, err)
	}

	if out, _ := w.receipts.OutstandingChecks(w.ctx, w.owner, acct, types.Date{}); len(out.Checks) != 0 {
		t.Errorf("a void check is outstanding: %+v", out)
	}

	in, err := w.receipts.Inbox(w.ctx, w.owner, types.AccountScope(acct))
	if err != nil || len(in.Voided) != 1 || len(in.Waiting) != 0 {
		t.Errorf("the inbox: %d void, %d waiting, %v", len(in.Voided), len(in.Waiting), err)
	}

	if st, _ := w.receipts.CheckNumberState(w.ctx, w.owner, acct, "1190"); st.State != receiptbus.NumberVoid || st.Check.ID != spoiled.ID {
		t.Errorf("1190 voided: %+v", st)
	}

	// Printed again on the next sheet with the same number, for another
	// amount: a new check.
	reprinted := send("1190", 1243)
	if reprinted.ID == spoiled.ID || !reprinted.Waiting() {
		t.Errorf("reprinted: %+v", reprinted)
	}

	if st, _ := w.receipts.CheckNumberState(w.ctx, w.owner, acct, "1190"); st.State != receiptbus.NumberOutstanding || st.Check.ID != reprinted.ID {
		t.Errorf("1190 reprinted: %+v", st)
	}

	// Its mark cannot come off now: another check has the number.
	if _, err := w.receipts.SetCheckVoided(w.ctx, now, w.owner, spoiled.ID, false); err == nil {
		t.Error("the void mark came off a number printed again")
	} else if _, ok := errors.AsType[receiptbus.NumberUsed](err); !ok {
		t.Errorf("unvoiding: %v", err)
	}

	// A void check is matched to nothing, numbered or read no further.
	if _, err := w.receipts.ReadCheck(w.ctx, now, w.owner, spoiled.ID, receiptbus.CheckReading{Number: "1190", Amount: 1234}); !errors.Is(err, receiptbus.ErrVoided) {
		t.Errorf("reading a void check: %v", err)
	}

	// On its page, by mistake and back.
	mistake := send("1191", 500)

	if r, err := w.receipts.SetCheckVoided(w.ctx, now, w.owner, mistake.ID, true); err != nil || !r.Voided() {
		t.Fatalf("voided on its page: %+v, %v", r, err)
	}

	if r, err := w.receipts.SetCheckVoided(w.ctx, now, w.owner, mistake.ID, false); err != nil || r.Voided() || !r.Waiting() {
		t.Errorf("the mark off: %+v, %v", r, err)
	}

	// Cleared: it cannot be voided.
	paid := send("1176", 12000)
	if _, err := w.receipts.VoidCheck(w.ctx, now, w.owner, acct, "1176"); !errors.Is(err, receiptbus.ErrAttached) {
		t.Errorf("voiding a cleared check: %v", err)
	}

	if _, err := w.receipts.SetCheckVoided(w.ctx, now, w.owner, paid.ID, true); !errors.Is(err, receiptbus.ErrAttached) {
		t.Errorf("voiding a cleared check on its page: %v", err)
	}

	if _, err := w.receipts.VoidCheck(w.ctx, now, w.owner, acct, "4444"); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("voiding no check: %v", err)
	}

	if _, err := w.receipts.VoidCheck(w.ctx, now, w.stranger, acct, "1191"); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a stranger voiding: %v", err)
	}
}

// What an account knows of a number, and the numbers it has used.
func TestCheckNumbers(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	if _, err := w.receipts.SendCheck(w.ctx, now, w.owner, acct, w.upload(w.owner, "a.png", photo+"a", receiptbus.Accept),
		receiptbus.CheckReading{Number: "998", Amount: 100, Payee: "A", On: day(t, "2026-08-01")}); err != nil {
		t.Fatal(err)
	}

	for number, want := range map[string]receiptbus.CheckState{
		"998":  receiptbus.NumberOutstanding,
		"1180": receiptbus.NumberCleared,
		"1181": receiptbus.NumberFree,
	} {
		st, err := w.receipts.CheckNumberState(w.ctx, w.owner, acct, number)
		if err != nil || st.State != want {
			t.Errorf("%s: %+v, %v, want %s", number, st, err, want)
		}
	}

	if st, _ := w.receipts.CheckNumberState(w.ctx, w.owner, acct, "1180"); len(st.Cleared) != 2 {
		t.Errorf("1180 was paid twice: %+v", st.Cleared)
	}

	if _, err := w.receipts.CheckNumberState(w.ctx, w.owner, acct, "soon"); err == nil {
		t.Error("a number with no digits")
	}

	if _, err := w.receipts.CheckNumberState(w.ctx, w.stranger, acct, "1180"); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	ns, err := w.receipts.CheckNumbers(w.ctx, w.owner, acct)
	if err != nil || ns.Highest != "1180" || !slices.Equal(ns.Last, []string{"1180", "1177", "1176", "998"}) {
		t.Errorf("the numbers: %+v, %v", ns, err)
	}
}
