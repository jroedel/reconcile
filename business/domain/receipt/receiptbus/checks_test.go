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
// goes onto the transaction. A number with no transaction, or with two, a
// file that names no number, and a payee for two checks add nothing.
func TestCheckImages(t *testing.T) {
	w := newWorld(t)
	acct := w.checking()

	image := func(name string) types.ID { return w.upload(w.owner, name, photo+name, receiptbus.Accept) }

	added, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, []types.ID{image("1176-front.jpg"), image("1176-back.jpg")}, "", "Hilltop Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 1 || len(added[0].Files) != 2 || added[0].Check != "1176" || added[0].Amount.String() != "120.00" ||
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
		want   func(error) bool
	}{
		"a number with no check": {[]string{"1190.jpg"}, "", "", func(err error) bool {
			e, ok := errors.AsType[receiptbus.NoCheck](err)
			return ok && e.Number == "1190" && !e.Several
		}},
		"a number with two checks": {[]string{"1180.jpg"}, "", "", func(err error) bool {
			e, ok := errors.AsType[receiptbus.NoCheck](err)
			return ok && e.Several
		}},
		"no number": {[]string{"IMG_4522.jpg"}, "", "", func(err error) bool {
			e, ok := errors.AsType[receiptbus.Invalid](err)
			return ok && e.Field == "number"
		}},
		"a payee for two checks": {[]string{"1176.jpg", "1177.jpg"}, "", "Somebody", func(err error) bool {
			e, ok := errors.AsType[receiptbus.Invalid](err)
			return ok && e.Field == "payee"
		}},
		"one good and one with no check": {[]string{"1176.jpg", "1191.jpg"}, "", "", func(err error) bool {
			_, ok := errors.AsType[receiptbus.NoCheck](err)
			return ok
		}},
	} {
		var ids []types.ID
		for _, f := range c.files {
			ids = append(ids, image(f))
		}

		if _, err := w.receipts.AddChecks(w.ctx, now, w.owner, acct, ids, c.number, c.payee); !c.want(err) {
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
