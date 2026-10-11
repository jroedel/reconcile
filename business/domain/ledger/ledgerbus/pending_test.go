package ledgerbus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Pending charges (docs/clearing.md, 4). The shops and amounts are
// invented.

// withStatus is a card page's export with a status column.
func withStatus(rows ...string) []byte {
	return []byte("Date,Description,Amount,Status\n" + strings.Join(rows, "\n") + "\n")
}

// find is the account's transaction in a month with a description.
func (w *world) find(actor, account types.ID, month, desc string) (ledgerbus.Transaction, bool) {
	w.t.Helper()

	txs, err := w.ledger.Transactions(w.t.Context(), actor, account, month)
	if err != nil {
		w.t.Fatal(err)
	}

	for _, tx := range txs {
		if tx.Description == desc {
			return tx, true
		}
	}

	return ledgerbus.Transaction{}, false
}

// line is the detail of the account's history line of an action.
func (w *world) line(account types.ID, action eventbus.Action) map[string]string {
	w.t.Helper()

	events, err := w.history.Recent(w.t.Context(), types.AccountScope(account), 50)
	if err != nil {
		w.t.Fatal(err)
	}

	for _, e := range events {
		if e.Action == action {
			return e.Detail
		}
	}

	return nil
}

// held is a card whose September page was downloaded with a parking hold
// still pending, sorted into two parts by a person since.
func held(t *testing.T, w *world, me types.ID, parts ...money.Amount) (card types.ID, hold ledgerbus.Transaction) {
	t.Helper()

	card = w.account(me, "checking")

	st := w.add(me, card, "september.csv", withStatus(
		"2026-09-29,CITY PARKING,-7.25,Pending",
		"2026-09-16,CORNER BAKERY,-12.00,Posted"))

	if st.Added != 2 {
		t.Fatalf("added %d", st.Added)
	}

	hold, ok := w.find(me, card, "2026-09", "CITY PARKING")
	if !ok || !hold.Pending {
		t.Fatalf("the hold: %+v", hold)
	}

	if bakery, _ := w.find(me, card, "2026-09", "CORNER BAKERY"); bakery.Pending {
		t.Error("a posted row stored as pending")
	}

	if len(parts) > 0 {
		var ps []ledgerbus.Part
		for _, a := range parts {
			ps = append(ps, ledgerbus.Part{Amount: a, Memo: "for the hall"})
		}

		if _, err := w.ledger.SetSplits(t.Context(), now, me, hold.ID, ps); err != nil {
			t.Fatal(err)
		}
	}

	return card, hold
}

// The posted charge, two days later and with the tip on it, takes the
// pending one's place and keeps how a person sorted it; the tip goes to
// the last part.
func TestAPendingChargePosts(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card, hold := held(t, w, me, -400, -325)

	october := withStatus("2026-10-01,CITY PARKING 0412 SPRINGFIELD,-12.00,Posted", "2026-10-02,HARDWARE BARN,-4.00,Posted")

	d, _ := w.preview(me, card, "october.csv", october)
	if d.Statement.Posted != 1 || d.Statement.Added != 1 {
		t.Errorf("preview: posted %d, added %d", d.Statement.Posted, d.Statement.Added)
	}

	st := w.add(me, card, "october.csv", october)
	if st.Posted != 1 || st.Added != 1 || st.Already != 0 {
		t.Errorf("import: posted %d, added %d, already %d", st.Posted, st.Added, st.Already)
	}

	if _, ok := w.find(me, card, "2026-09", "CITY PARKING"); ok {
		t.Error("the pending charge is still there")
	}

	posted, ok := w.find(me, card, "2026-10", "CITY PARKING 0412 SPRINGFIELD")
	if !ok || posted.ID != hold.ID || posted.Pending || posted.Amount != -1200 || posted.StatementID != st.ID {
		t.Fatalf("the posted charge: %+v", posted)
	}

	ed, err := w.ledger.Transaction(ctx, me, hold.ID)
	if err != nil {
		t.Fatal(err)
	}

	if s := ed.Transaction.Splits; len(s) != 2 || s[0].Amount != -400 || s[1].Amount != -800 || s[1].Memo != "for the hall" {
		t.Errorf("its parts: %+v", s)
	}

	if line := w.line(card, ledgerbus.TransactionPosted); line["pending"] != "-7.25" || line["posted"] != "-12.00" || line["resplit"] != "0" || line["parts"] != "2" {
		t.Errorf("the history: %v", line)
	}

	// The same October page again changes nothing.
	if again := w.add(me, card, "october-again.csv", append(october, "\n"...)); again.Added != 0 || again.Posted != 0 || again.Already != 2 {
		t.Errorf("again: %+v", again)
	}
}

// A posted amount that would turn the last part round puts the charge
// back to one part, for a person to split again.
func TestAPostedAmountThatUndoesTheParts(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card, hold := held(t, w, me, -700, -25)

	w.add(me, card, "october.csv", withStatus("2026-10-01,CITY PARKING,-5.00,Posted"))

	ed, err := w.ledger.Transaction(t.Context(), me, hold.ID)
	if err != nil {
		t.Fatal(err)
	}

	if s := ed.Transaction.Splits; len(s) != 1 || s[0].Amount != -500 || !s[0].CategoryID.Zero() {
		t.Errorf("its parts: %+v", s)
	}

	if line := w.line(card, ledgerbus.TransactionPosted); line["resplit"] != "1" {
		t.Errorf("the history: %v", line)
	}
}

// Posted as it was pending -- same day, words and amount -- it is the
// same row, and only stops being pending.
func TestPostedAsItWasPending(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card, hold := held(t, w, me)

	st := w.add(me, card, "september-later.csv", withStatus("2026-09-29,CITY PARKING,-7.25,Posted"))
	if st.Added != 0 || st.Already != 1 || st.Posted != 1 {
		t.Errorf("import: %+v", st)
	}

	if tx, _ := w.find(me, card, "2026-09", "CITY PARKING"); tx.ID != hold.ID || tx.Pending {
		t.Errorf("the charge: %+v", tx)
	}
}

// Too late, or worded otherwise, a posted charge is not taken for the
// pending one, which is listed as still pending once the account's
// statements have gone well past it.
func TestNotTakenForAPendingCharge(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card, hold := held(t, w, me)

	st := w.add(me, card, "october.csv", withStatus(
		"2026-10-01,PARKING AUTHORITY,-7.25,Posted",
		"2026-10-12,CITY PARKING,-7.25,Posted"))
	if st.Added != 2 || st.Posted != 0 {
		t.Errorf("import: %+v", st)
	}

	still, err := w.ledger.StillPending(ctx, me, card)
	if err != nil {
		t.Fatal(err)
	}

	if len(still) != 1 || still[0].ID != hold.ID {
		t.Errorf("still pending: %+v", still)
	}

	// Not before the statements are past it.
	recent, _ := held(t, w, me)
	if still, _ := w.ledger.StillPending(ctx, me, recent); len(still) != 0 {
		t.Errorf("still pending on a page just read: %+v", still)
	}
}

// Releasing a hold that never posted is a bookkeeper's, of a pending
// charge, outside a reconciled period.
func TestReleasingAHold(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card, hold := held(t, w, me)
	bakery, _ := w.find(me, card, "2026-09", "CORNER BAKERY")

	viewer := w.user("viewer@example.org")
	w.grant(me, types.AccountScope(card), "viewer@example.org", tenancybus.Viewer)

	stranger := w.user("stranger@example.org")

	if err := w.ledger.Release(ctx, now, viewer, hold.ID); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}

	if err := w.ledger.Release(ctx, now, stranger, hold.ID); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.ledger.StillPending(ctx, stranger, card); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger's list: %v", err)
	}

	if err := w.ledger.Release(ctx, now, me, bakery.ID); !errors.Is(err, ledgerbus.ErrNotPending) {
		t.Errorf("a posted charge: %v", err)
	}

	if err := w.ledger.Release(ctx, now, me, hold.ID); err != nil {
		t.Fatal(err)
	}

	if _, ok := w.find(me, card, "2026-09", "CITY PARKING"); ok {
		t.Error("the hold is still there")
	}

	if line := w.line(card, ledgerbus.TransactionReleased); line["amount"] != "-7.25" || line["description"] != "CITY PARKING" {
		t.Errorf("the history: %v", line)
	}

	// In a reconciled period it holds still.
	card2, hold2 := held(t, w, me)
	sts := w.statements(me, card2)

	if _, err := w.ledger.Reconcile(ctx, now, me, sts[0].ID, types.Date{}, types.Date{}, ""); err != nil {
		t.Fatal(err)
	}

	if err := w.ledger.Release(ctx, now, me, hold2.ID); !errors.Is(err, ledgerbus.ErrLocked) {
		t.Errorf("in a reconciled period: %v", err)
	}
}

// What a pending charge's amount may become (issue #81): any amount for
// the same wording, within a third or PostSlack for wording only alike,
// and never turned round.
func TestPostsAs(t *testing.T) {
	for _, c := range []struct {
		pending, posted string
		same, want      bool
	}{
		{"-40.00", "-25.00", false, false}, // the issue's: another charge at the same shop
		{"-40.00", "-52.00", false, true},  // a tip of 30%
		{"-40.00", "-54.00", false, false}, // 35% is past a third
		{"-40.00", "-25.00", true, true},   // the same wording keeps any amount
		{"-1.00", "-48.20", true, true},    // a fuel hold
		{"-1.00", "-48.20", false, false},
		{"-7.25", "-12.00", false, true}, // a small charge's tip, inside the floor
		{"-40.00", "40.00", true, false}, // a refund is not the posted form
		{"25.00", "30.00", false, true},  // money in, the same way round
	} {
		if got := ledgerbus.PostsAs(money.MustParse(c.pending), money.MustParse(c.posted), c.same); got != c.want {
			t.Errorf("PostsAs(%s, %s, same wording %v) = %v", c.pending, c.posted, c.same, got)
		}
	}
}

// A pending charge is not taken by a posted one that is only alike in
// wording and far off in amount: both are kept, the pending one until
// "still pending" lists it.
func TestAnotherAmountIsAnotherCharge(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	w.add(me, card, "september.csv", withStatus("2026-09-28,SHOP X,-40.00,Pending"))

	st := w.add(me, card, "october.csv", withStatus("2026-10-03,SHOP X 123 MAIN ST,-25.00,Posted"))
	if st.Posted != 0 || st.Added != 1 {
		t.Errorf("posted %d, added %d", st.Posted, st.Added)
	}

	if hold, ok := w.find(me, card, "2026-09", "SHOP X"); !ok || !hold.Pending || hold.Amount != -4000 {
		t.Errorf("the pending charge: %+v", hold)
	}

	// With the tip on it, it is the same charge.
	if st := w.add(me, card, "october-2.csv", withStatus("2026-10-04,SHOP X 123 MAIN ST,-48.00,Posted")); st.Posted != 1 || st.Added != 0 {
		t.Errorf("with the tip: posted %d, added %d", st.Posted, st.Added)
	}
}

// One holder's posted charge never takes another holder's pending one,
// whether or not the account keeps holders apart (issue #81).
func TestAnotherHoldersPostedCharge(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "checking")

	held := func(rows ...string) []byte {
		return []byte("Date,Description,Amount,Status,Card Member\n" + strings.Join(rows, "\n") + "\n")
	}

	w.add(me, card, "ana.csv", held("2026-09-28,SHOP X,-40.00,Pending,Ana"))

	st := w.add(me, card, "ben.csv", held("2026-10-03,SHOP X,-40.00,Posted,Ben"))
	if st.Posted != 0 || st.Added != 1 {
		t.Errorf("Ben's charge: posted %d, added %d", st.Posted, st.Added)
	}

	if hold, ok := w.find(me, card, "2026-09", "SHOP X"); !ok || !hold.Pending || hold.Holder != "Ana" {
		t.Errorf("Ana's pending charge: %+v", hold)
	}

	// Ana's own posted charge takes it.
	if st := w.add(me, card, "ana-october.csv", held("2026-10-02,SHOP X,-44.00,Posted,Ana")); st.Posted != 1 {
		t.Errorf("Ana's posted charge: posted %d, added %d", st.Posted, st.Added)
	}
}
