package ledgerbus

import (
	"context"
	"errors"
	"time"

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
// posted charge, when a later file brings it, takes its place rather than
// joining it: a row of the same account (and holder) dated on the
// pending one's day or up to PostWithin after, not pending itself, whose
// description is the pending one's or contains it. Its date, amount and
// description become the posted ones; what a person did to it -- its
// sorting, its receipts, the explanation it is part of -- stays, because
// it is the same charge. The store does it, inside the import
// (ledgerdb/pending.go), so that the preview's counts are the import's.

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

// The history lines of a pending charge.
const (
	TransactionPosted   eventbus.Action = "transaction.posted"
	TransactionReleased eventbus.Action = "transaction.released"
)

// ErrNotPending is a removal of a transaction that is not pending: only a
// hold that never posted may go without its statement.
var ErrNotPending = errors.New("only a pending charge can be removed this way")

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

	if !t.Pending {
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
