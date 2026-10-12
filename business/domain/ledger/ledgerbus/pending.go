package ledgerbus

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Pending charges (docs/clearing.md, 4).
//
// A bank's page lists charges that have not posted yet, and a pending
// amount often changes when it posts: a parking hold, a restaurant before
// the tip, a hotel's deposit. Imported as if posted, the pending charge
// and the posted one would be two, with different amounts, so that not
// even the count rule sees them.
//
// So a row the document says is pending is stored marked so, and the
// posted charge, when a file brings it, clears it (issue #104): the
// pending row stays where it is, in the statement that was checked with
// it, and is marked as having posted as the new row (PostedAs). From then
// on only the posted charge counts. What a person did to the pending one
// -- its sorting, its receipts, the explanation it is part of -- goes to
// the posted one, because it is the same charge.
//
// It used to be the other way round: the posted row took the pending
// one's place, its date, amount and description written over the
// pending ones. That kept one row, but the statement the pending charge
// came in, checked by its sum with the pending amount in it, stopped
// adding up, and a printed page's hold could not be told from the
// charge it became.
//
// A pending charge is cleared at import only when its posted form is
// plain: a row of the same account (and holder) dated on the pending
// one's day or up to PostWithin after, not pending itself, that fits it
// better than any other row fits it and better than it fits any other
// pending charge (ledgerdb's fit and pair). Two holds at one shop and one
// charge, or one hold and two charges, are left for a person to pair
// (Pairs, ClearPending), as receipts' matches are. The store does it,
// inside the import (ledgerdb/pending.go), so that the preview's counts
// are the import's.

// PostWithin is how many days after a pending charge its posted form may
// be dated and still be taken for it, and how many days before the end of
// its account's latest statement a pending charge may be dated before it
// is listed as still pending.
const PostWithin = 10

// PostSlack is the least a posted charge's amount may differ from its
// pending one's and still be taken for it, in the currency's smallest
// units: five dollars, euros or reais. A third of the pending amount is
// the rule (PostsAs); this is its floor, for a charge so small that a
// third of it is less than a tip.
const PostSlack money.Amount = 500

// PostsAs reports whether a posted charge's amount may be what a pending
// charge of another amount became, when their descriptions are alike but
// not the same (sameWording false).
//
// The holds docs/clearing.md names are the reason pending and posted
// differ at all: a restaurant's charge before the tip, which a tip of up
// to a third covers; a parking or fuel hold, and a hotel's deposit, which
// settle at whatever was spent and are, in the files read so far, worded
// the same pending and posted. So:
//
//   - The same wording keeps the tolerance it always had: any amount the
//     same way round. A fuel hold of 1.00 that posts at 48.20 under the
//     same description is the same charge.
//   - Wording that is only alike -- one inside the other, or cut short --
//     must also be close in amount: within a third of the pending amount,
//     or PostSlack, whichever is more. A 40.00 charge at one shop and a
//     25.00 charge at the same shop days later are two charges, which is
//     what the issue found one taking the other's place.
//   - Never turned round: a refund is not the posted form of a charge.
//
// A posted charge refused here is imported as new, and the pending one
// stays until "still pending" lists it -- both visible, where a wrong
// match was invisible.
func PostsAs(pending, posted money.Amount, sameWording bool) bool {
	if pending == 0 || posted == 0 || (pending < 0) != (posted < 0) {
		return false
	}

	if sameWording {
		return true
	}

	return (posted - pending).Abs() <= max(pending.Abs()/3, PostSlack)
}

// Tipped reports whether a posted amount may be a pending one with a tip:
// the same way round, the same or more, and by no more than a third or
// PostSlack (PostsAs).
//
// It is the amount rule for wording that shares only its first words --
// "CAFE EXAMPLE PAL A…" on a printed page, "CAFE EXAMPLE CITY ST" in the
// bank's download (SameMerchant) -- which is the weakest likeness there is
// between a pending charge and its posted one, so the amount must say the
// rest: the same, or more by what a tip adds, never less. A hold that
// settles lower (a hotel, a fuel pump) is worded the same when pending
// and posted, in the files read so far, and PostsAs takes it.
func Tipped(pending, posted money.Amount) bool {
	return PostsAs(pending, posted, false) && posted.Abs() >= pending.Abs()
}

// SameMerchant reports whether two descriptions begin with the same two
// words, letters and digits only and ignoring case, after a payment
// processor's prefix ("SQ *", "TST* ", "PAYPAL *": whatever comes before
// a star near the start). Two words, because one is too often a chain's
// or a city's.
func SameMerchant(a, b string) bool {
	wa, wb := merchantWords(a), merchantWords(b)

	return len(wa) >= 2 && len(wb) >= 2 && wa[0] == wb[0] && wa[1] == wb[1]
}

func merchantWords(s string) []string {
	if i := strings.IndexByte(s, '*'); i >= 0 && i <= 10 {
		s = s[i+1:]
	}

	return strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Pair is a pending charge and a posted charge that may be what it
// became, for a person to say whether it is (Pairs).
type Pair struct {
	Pending, Posted Transaction
}

// The history lines of a pending charge.
const (
	TransactionPosted    eventbus.Action = "transaction.posted"
	TransactionReleased  eventbus.Action = "transaction.released"
	TransactionUncleared eventbus.Action = "transaction.uncleared"
)

var (
	// ErrNotPending is a removal of a transaction that is not pending, or
	// one already cleared: only a hold that never posted may go without
	// its statement.
	ErrNotPending = errors.New("only a pending charge that has not posted can be removed this way")

	// ErrNotPosted is a pairing of a pending charge with a transaction
	// that cannot be what it became: another account's, pending itself,
	// the posted form of another, or the other way round from it.
	ErrNotPosted = errors.New("that transaction cannot be the posted form of this pending charge")

	// ErrCleared is a change to the parts of a pending charge that has
	// posted: they are its posted charge's to change.
	ErrCleared = errors.New("this pending charge has posted; sort its posted charge instead")
)

// StillPending is the account's pending charges that the account's
// statements should have posted by now: older by PostWithin than the end of
// its latest statement. Each is a hold that was released, or a charge
// whose posted form was worded too differently to be taken for it; a
// person decides which.
func (b *Business) StillPending(ctx context.Context, actor, accountID types.ID) ([]Transaction, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	return b.store.StillPending(ctx, accountID, PostWithin)
}

// Release removes a pending charge that never posted, a bookkeeper's
// word that the hold was released. Outside a reconciled period only, and
// the history keeps what it was.
func (b *Business) Release(ctx context.Context, now time.Time, actor, id types.ID) error {
	t, err := b.store.TransactionByID(ctx, id)
	if err != nil {
		return err
	}

	account, access, err := b.accounts.Account(ctx, actor, t.AccountID)
	if err != nil {
		return err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return ErrForbidden
	}

	if !t.Pending || t.Cleared() {
		return ErrNotPending
	}

	ev := eventbus.New(now, actor, account.Scope(), TransactionReleased, map[string]string{
		"description": t.Description,
		"amount":      t.Amount.String(),
		"currency":    account.Currency,
		"date":        t.PostedOn.String(),
	})

	return b.store.Release(ctx, id, ev)
}

// Pairs is the account's pending charges that have not posted and the
// posted charges each may have become, by the rule an import clears one
// by, but where it could not tell which: two holds at one shop and one
// charge. A person says which is which (ClearPending), or that none is;
// a pending charge with no pair here waits on StillPending.
func (b *Business) Pairs(ctx context.Context, actor, accountID types.ID) ([]Pair, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	return b.store.Pairs(ctx, accountID, PostWithin)
}

// ClearPending says that a pending charge posted as another transaction
// of its account: a person's answer to Pairs, or to a hold whose posted
// form is worded too differently for any rule. The posted charge may be
// any not pending, not already another's posted form, the same way
// round, of the same holder or one that names nobody. Bookkeep on the
// account, and neither in a reconciled period: the pending charge stops
// counting, and its parts become the posted charge's.
func (b *Business) ClearPending(ctx context.Context, now time.Time, actor, pendingID, postedID types.ID) error {
	p, err := b.store.TransactionByID(ctx, pendingID)
	if err != nil {
		return err
	}

	account, access, err := b.accounts.Account(ctx, actor, p.AccountID)
	if err != nil {
		return err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return ErrForbidden
	}

	if !p.Pending || p.Cleared() {
		return ErrNotPending
	}

	t, err := b.store.TransactionByID(ctx, postedID)
	if errors.Is(err, ErrNotFound) || err == nil && (t.AccountID != p.AccountID || t.Pending ||
		(t.Amount < 0) != (p.Amount < 0) || !SameHolder(p.Holder, t.Holder)) {
		return ErrNotPosted
	}

	if err != nil {
		return err
	}

	if err := b.store.Clear(ctx, p, t, PostedEvent(now, actor, account, p, t)); err != nil {
		return err
	}

	b.followed(ctx, now, actor, account.ID)

	return nil
}

// PostedEvent is the history line of a pending charge cleared by its
// posted form, by a person or an import.
func PostedEvent(now time.Time, actor types.ID, account tenancybus.Account, p, t Transaction) eventbus.Event {
	return eventbus.New(now, actor, account.Scope(), TransactionPosted, map[string]string{
		"description": t.Description,
		"date":        t.PostedOn.String(),
		"pending":     p.Amount.String(),
		"posted":      t.Amount.String(),
		"currency":    account.Currency,
	})
}

// Unclear undoes a clearing that was wrong: the pending charge counts
// again, with one part for its whole amount, unsorted, and waits for its
// posted form or to be released. The posted charge keeps the parts it
// was given, which a person may have changed since. Bookkeep, outside a
// reconciled period.
func (b *Business) Unclear(ctx context.Context, now time.Time, actor, pendingID types.ID) error {
	p, err := b.store.TransactionByID(ctx, pendingID)
	if err != nil {
		return err
	}

	account, access, err := b.accounts.Account(ctx, actor, p.AccountID)
	if err != nil {
		return err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return ErrForbidden
	}

	if !p.Cleared() {
		return ErrNotPending
	}

	return b.store.Unclear(ctx, p, eventbus.New(now, actor, account.Scope(), TransactionUncleared, map[string]string{
		"description": p.Description,
		"amount":      p.Amount.String(),
		"currency":    account.Currency,
		"date":        p.PostedOn.String(),
	}))
}

// ClearedHolds is the account's pending charges that have posted, by the
// posted charge each became: for what follows them there, such as their
// receipts.
func (b *Business) ClearedHolds(ctx context.Context, actor, accountID types.ID) (map[types.ID]types.ID, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	return b.store.ClearedHolds(ctx, accountID)
}

// pendingOf fills in an editor's pending charge and posted charge, each
// the other's, or the pairs a pending charge that has not posted may be.
func (b *Business) pendingOf(ctx context.Context, e *Editor) error {
	t := e.Transaction

	switch {
	case t.Cleared():
		posted, err := b.store.TransactionByID(ctx, t.PostedAs)
		if err != nil {
			return err
		}

		e.PostedAs = &posted
	case t.Pending:
		pairs, err := b.store.Pairs(ctx, t.AccountID, PostWithin)
		if err != nil {
			return err
		}

		for _, p := range pairs {
			if p.Pending.ID == t.ID {
				e.Pairs = append(e.Pairs, p)
			}
		}
	default:
		hold, ok, err := b.store.HoldOf(ctx, t.ID)
		if err != nil {
			return err
		}

		if ok {
			e.Hold = &hold
		}
	}

	return nil
}
