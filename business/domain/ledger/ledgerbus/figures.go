package ledgerbus

import (
	"context"

	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Figures is what a list says beside an account or a project, so that a
// person can tell a busy one from a quiet one, and one that came out even
// from one that is short, without opening each (issue #106).
//
// The issue first asked for the net with a percentage beside it, "+$100
// (+3%)", for a sense of how much passed through. A percentage of what
// moved is a number nobody can check against anything else on the site,
// and income and expenses side by side give the same sense in words the
// project's own book and the accountant already use; so it is those, with
// the net, and the percentage was left out.
type Figures struct {
	// First and Last are the days of the earliest and the latest
	// transaction; zero when there is none.
	First, Last types.Date

	// Count is how many transactions. A project's counts each transaction
	// with a part in it once, however many of its parts are.
	Count int

	// Totals is per currency, as a project's book keeps them, under no
	// heading. A project's is its parts, by kind and as cash. An account's
	// is its transactions as cash only: what came in and went out of it,
	// which is the measure of how busy it is. The kinds of an account's
	// money are on its month list, where they can be read month by month.
	Totals []Total

	// Balance is the closing balance of an account's latest statement that
	// was checked against one, and BalanceOn the last day of that
	// statement: the balance the bank stated, which is the one a person
	// can compare with theirs. A project has none, and nor has an account
	// whose statements never stated one.
	Balance   money.Amount
	BalanceOn types.Date
}

// HasBalance reports whether the figures have a balance to show.
func (f Figures) HasBalance() bool { return !f.BalanceOn.Zero() }

// AccountFigures is the figures of each of the accounts the actor may read.
// One they hold nothing on is left out of the answer, as one that does not
// exist is, rather than failing the whole list: the page still shows the
// others, and says nothing about the one left out.
func (b *Business) AccountFigures(ctx context.Context, actor types.ID, ids []types.ID) (map[types.ID]Figures, error) {
	readable, err := b.mayRead(ctx, actor, ids, types.AccountScope)
	if err != nil {
		return nil, err
	}

	return b.store.AccountFigures(ctx, readable)
}

// ProjectFigures is the figures of each of the projects the actor may read,
// with the same answer for one they may not as AccountFigures.
func (b *Business) ProjectFigures(ctx context.Context, actor types.ID, ids []types.ID) (map[types.ID]Figures, error) {
	readable, err := b.mayRead(ctx, actor, ids, types.ProjectScope)
	if err != nil {
		return nil, err
	}

	return b.store.ProjectFigures(ctx, readable)
}

// mayRead is those of the ids whose scope the actor may read. One question
// to tenancybus for each: a list on a page is a handful, and asking for
// each is what keeps the rule in one place.
func (b *Business) mayRead(ctx context.Context, actor types.ID, ids []types.ID, scope func(types.ID) types.Scope) ([]types.ID, error) {
	var out []types.ID

	for _, id := range ids {
		access, err := b.accounts.AccessTo(ctx, actor, scope(id))
		if err != nil {
			return nil, err
		}

		if access.Can(tenancybus.Read) {
			out = append(out, id)
		}
	}

	return out, nil
}
