package receiptbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A check's number from its file's name, and none from a name that might
// be anything else's.
func TestNumberFromName(t *testing.T) {
	for name, want := range map[string]string{
		"1176.jpg":               "1176",
		"check-1176-front.jpg":   "1176",
		"Check_01176_back.PNG":   "1176",
		"cheque 1176 verso.pdf":  "1176",
		"chk1176.jpg":            "",
		"1176-1.jpg":             "1176",
		"nº 1176 frente.jpeg":    "1176",
		"IMG_4521.jpg":           "",
		"scan 2026-07-02.pdf":    "",
		"check 1176 2026.jpg":    "",
		"12.jpg":                 "",
		"plumber check 1176.jpg": "",
		"1176 page 2.jpg":        "1176",
		"":                       "",
	} {
		if got := receiptbus.NumberFromName(name); got != want {
			t.Errorf("NumberFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

// checking is an account with invented checks: 1176 and 1177 once each,
// 1180 twice.
func (w *world) checking() types.ID {
	w.t.Helper()

	acct, err := w.ten.CreateAccount(w.ctx, now, w.owner, w.org, tenancybus.AccountFields{Name: "Parish checking", Kind: "checking"})
	if err != nil {
		w.t.Fatal(err)
	}

	const file = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
		"2026-07-02,CHECK,-120.00,880.00,0001176\n" +
		"2026-07-09,Check 1177,-30.00,850.00,1177\n" +
		"2026-07-10,CHECK,-10.00,840.00,1180\n" +
		"2026-07-11,CHECK,-12.00,828.00,1180\n"

	f := w.upload(w.owner, "checking.csv", file, nil)

	d, err := w.ledger.Prepare(w.ctx, w.owner, acct.ID, f, nil)
	if err != nil {
		w.t.Fatal(err)
	}

	if _, err := w.ledger.Import(w.ctx, now, w.owner, acct.ID, f, ledgerbus.Options{Mapping: d.Mapping}); err != nil {
		w.t.Fatal(err)
	}

	return acct.ID
}

// A check's front and back, named after it, are one receipt attached to
// the check's transaction, with its date and amount; whom it was paid to
// goes onto the transaction. A number not typed must be digits, and a
// payee is for one check at a time.
func TestCheckImages(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	image := func(name string) types.ID { return w.upload(w.owner, name, photo+name, receiptbus.Accept) }

	added, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1176-front.jpg"), image("1176-back.jpg")}, "", "Hilltop Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 1 || len(added[0].Files) != 2 || added[0].Check != "1176" || !added[0].CheckImage || added[0].Amount.String() != "120.00" ||
		added[0].SpentOn.String() != "2026-07-02" || added[0].Merchant != "Hilltop Plumbing" || len(added[0].Links) != 1 {
		t.Fatalf("added: %+v", added)
	}

	check, _ := w.ledger.Lookup(w.ctx, added[0].Links[0].TransactionID)
	if check.CheckNumber != "1176" || check.Payee != "Hilltop Plumbing" {
		t.Errorf("the check: %+v", check)
	}

	// Typed, for a file named by the phone; and corrected on its page,
	// which corrects the transaction.
	typed, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("IMG_4521.jpg")}, "1177", "")
	if err != nil || len(typed) != 1 || typed[0].Check != "1177" {
		t.Fatalf("typed: %+v, %v", typed, err)
	}

	if _, err := w.receipts.SetDetails(w.ctx, now, w.owner, typed[0].ID, receiptbus.Details{Merchant: "Diocesan Office"}); err != nil {
		t.Fatal(err)
	}

	if c, _ := w.ledger.Lookup(w.ctx, typed[0].Links[0].TransactionID); c.Payee != "Diocesan Office" {
		t.Errorf("the corrected payee: %q", c.Payee)
	}

	for name, c := range map[string]struct {
		files  []string
		number string
		payee  string
	}{
		"a number that is no number": {[]string{"IMG_4522.jpg"}, "#12a", ""},
		"a payee for two checks":     {[]string{"1176.jpg", "1177.jpg"}, "", "Somebody"},
	} {
		var ids []types.ID
		for _, f := range c.files {
			ids = append(ids, image(f))
		}

		if _, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, ids, c.number, c.payee); err == nil {
			t.Errorf("%s: added", name)
		} else if _, ok := errors.AsType[receiptbus.Invalid](err); !ok {
			t.Errorf("%s: %v", name, err)
		}
	}

	in, err := w.receipts.Inbox(w.ctx, w.owner, types.AccountScope(acct))
	if err != nil || len(in.Matched) != 2 || len(in.Waiting) != 0 {
		t.Errorf("the inbox: %d matched, %d waiting, %v", len(in.Matched), len(in.Waiting), err)
	}

	// Somebody given only the project cannot add a check's image to the
	// account; a viewer of the account may not.
	if _, err := w.receipts.AddChecks(w.ctx, now, w.pilgrim, acct, []types.ID{w.upload(w.pilgrim, "1177.jpg", photo, receiptbus.Accept)}, "", ""); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("the pilgrim: %v", err)
	}

	viewer := w.user("viewer@example.org")
	w.grant(types.AccountScope(acct), "viewer@example.org", tenancybus.Viewer)

	if _, err := w.receipts.AddChecks(w.ctx, now, viewer, acct, []types.ID{w.upload(viewer, "1177.jpg", photo, receiptbus.Accept)}, "", ""); !errors.Is(err, receiptbus.ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}
}

// Fifty screenshots from a bank's app, in one upload: none refused because
// another cannot be matched. A named check with one transaction is
// attached; one with none, or with two, waits with its number; a
// screenshot waits with none, a check of its own. Each waiting image is
// numbered later, on its page, which attaches it when it can; the one
// with two transactions is offered both.
func TestChecksWait(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	image := func(name string) types.ID { return w.upload(w.owner, name, photo+name, receiptbus.Accept) }

	added, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{
		image("1176.jpg"), image("1190.jpg"), image("1180.jpg"),
		image("Screenshot_20260712-101500.png"), image("Screenshot_20260712-101530.png"),
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 5 {
		t.Fatalf("%d checks added, want 5: %+v", len(added), added)
	}

	for i, want := range []struct {
		number   string
		attached bool
	}{{"1176", true}, {"1190", false}, {"1180", false}, {"", false}, {"", false}} {
		r := added[i]
		if !r.CheckImage || r.Check != want.number || r.Waiting() == want.attached || len(r.Files) != 1 {
			t.Errorf("check %d: %+v, want number %q attached %v", i, r, want.number, want.attached)
		}
	}

	in, err := w.receipts.Inbox(w.ctx, w.owner, types.AccountScope(acct))
	if err != nil || len(in.Matched) != 1 || len(in.Waiting) != 4 {
		t.Fatalf("the inbox: %d matched, %d waiting, %v", len(in.Matched), len(in.Waiting), err)
	}

	// The check with two transactions is offered both, and nothing else.
	s, err := w.receipts.Suggestions(w.ctx, w.owner, []receiptbus.Receipt{added[2]})
	if err != nil || len(s[added[2].ID]) != 2 {
		t.Errorf("the suggestions for check 1180: %+v, %v", s, err)
	}

	// A screenshot numbered, with whom it was paid to typed before: it is
	// attached, takes the bank's date and amount, and the payee goes to
	// the transaction.
	shot := added[3].ID
	if _, err := w.receipts.SetDetails(w.ctx, now, w.owner, shot, receiptbus.Details{Merchant: "Diocesan Office"}); err != nil {
		t.Fatal(err)
	}

	r, err := w.receipts.NumberCheck(w.ctx, now, w.owner, shot, "0001177")
	if err != nil || r.Check != "1177" || r.Waiting() || r.Amount.String() != "30.00" || r.SpentOn.String() != "2026-07-09" {
		t.Fatalf("numbered: %+v, %v", r, err)
	}

	if c, _ := w.ledger.Lookup(w.ctx, r.Links[0].TransactionID); c.CheckNumber != "1177" || c.Payee != "Diocesan Office" {
		t.Errorf("the transaction: %+v", c)
	}

	// Numbered again once attached: refused, since it has a transaction.
	if _, err := w.receipts.NumberCheck(w.ctx, now, w.owner, shot, "1176"); !errors.Is(err, receiptbus.ErrAttached) {
		t.Errorf("renumbering an attached check: %v", err)
	}

	// The other numbered for a check not cleared: it waits with its number.
	r, err = w.receipts.NumberCheck(w.ctx, now, w.owner, added[4].ID, "1191")
	if err != nil || r.Check != "1191" || !r.Waiting() {
		t.Errorf("a check not cleared: %+v, %v", r, err)
	}

	if got, _ := w.receipts.Receipt(w.ctx, w.owner, added[4].ID); got.Receipt.Check != "1191" {
		t.Errorf("the stored number: %q", got.Receipt.Check)
	}

	// No digits; a receipt that is no check; and somebody who may not.
	if _, err := w.receipts.NumberCheck(w.ctx, now, w.owner, added[4].ID, "soon"); err == nil {
		t.Error("a number with no digits was taken")
	}

	plain, err := w.receipts.Add(w.ctx, now, w.owner, types.AccountScope(acct), []types.ID{image("grocery.jpg")}, false, receiptbus.Details{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.receipts.NumberCheck(w.ctx, now, w.owner, plain[0].ID, "1190"); err == nil {
		t.Error("a grocery receipt was given a check number")
	}

	if _, err := w.receipts.NumberCheck(w.ctx, now, w.pilgrim, added[1].ID, "1190"); !errors.Is(err, receiptbus.ErrNotFound) {
		t.Errorf("the pilgrim: %v", err)
	}
}

// A check photographed before it cleared waits with its number, and the
// import of the statement that lists it attaches it, with the bank's date
// and amount and whom it was paid to on the transaction. A check whose
// number the import does not bring, and one with two transactions, still
// wait.
func TestChecksMatchWhenTheyClear(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	image := func(name string) types.ID { return w.upload(w.owner, name, photo+name, receiptbus.Accept) }

	early, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1190-front.jpg"), image("1190-back.jpg")}, "", "Hilltop Plumbing")
	if err != nil || len(early) != 1 || !early[0].Waiting() {
		t.Fatalf("check 1190: %+v, %v", early, err)
	}

	later, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1192.jpg"), image("1180.jpg")}, "", "")
	if err != nil || len(later) != 2 {
		t.Fatalf("checks 1192 and 1180: %+v, %v", later, err)
	}

	const august = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-08-03,CHECK,-50.00,778.00,1190\n" +
		"2026-08-04,CHECK,-25.00,753.00,1191\n"

	f := w.upload(w.owner, "august.csv", august, nil)

	d, err := w.ledger.Prepare(w.ctx, w.owner, acct, f, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.Import(w.ctx, now, w.owner, acct, f, ledgerbus.Options{Mapping: d.Mapping}); err != nil {
		t.Fatal(err)
	}

	got, err := w.receipts.Receipt(w.ctx, w.owner, early[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	if r := got.Receipt; r.Waiting() || r.Amount.String() != "50.00" || r.SpentOn.String() != "2026-08-03" || len(got.Transactions) != 1 {
		t.Fatalf("check 1190 after the import: %+v", got)
	}

	if c := got.Transactions[0]; c.CheckNumber != "1190" || c.Payee != "Hilltop Plumbing" {
		t.Errorf("check 1190's transaction: %+v", c)
	}

	in, err := w.receipts.Inbox(w.ctx, w.owner, types.AccountScope(acct))
	if err != nil || len(in.Matched) != 1 || len(in.Waiting) != 2 {
		t.Errorf("the inbox: %d matched, %d waiting, %v", len(in.Matched), len(in.Waiting), err)
	}
}
