package ledgerbus

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A check is a transaction with a number (Transaction.CheckNumber), and
// what the bank never says about it is to whom it was written. The image
// of the check says, and whoever attaches it may write it down as the
// transaction's payee (receiptbus): beside the description on every page,
// and in what sorting rules and suggestions read (Transaction.Words), so
// that "Check 1176" paid to the plumber is sorted as the plumber's
// charges are (docs/shapes.md, 3).

// MaxPayee is how long a payee may be, as long as a receipt's shop.
const MaxPayee = 100

// ErrPayee is a payee longer than MaxPayee.
var ErrPayee = errors.New("who a check was paid to is at most 100 characters")

// TransactionPaidTo is the history's line for a payee written down,
// changed or taken away.
const TransactionPaidTo eventbus.Action = "transaction.paid-to"

// WithCheck is the account's transactions that paid the check of that
// number, asking nobody's permission: for a domain that has asked already,
// as receiptbus asks before it matches a check's image.
func (b *Business) WithCheck(ctx context.Context, account types.ID, number string) ([]Transaction, error) {
	number = importbus.CheckNumber(number)
	if number == "" {
		return nil, nil
	}

	return b.store.WithCheck(ctx, account, number)
}

// SetPayee writes down to whom a check was written, or takes it away with
// "". It is whoever may attach receipts to the account's transactions who
// may: the person with the check's image in hand. A note, not money, so a
// reconciled period does not stop it.
func (b *Business) SetPayee(ctx context.Context, now time.Time, actor, id types.ID, payee string) (Transaction, error) {
	payee = strings.Join(strings.Fields(payee), " ")
	if utf8.RuneCountInString(payee) > MaxPayee {
		return Transaction{}, ErrPayee
	}

	t, err := b.store.TransactionByID(ctx, id)
	if err != nil {
		return Transaction{}, err
	}

	account, access, err := b.accounts.Account(ctx, actor, t.AccountID)
	if err != nil {
		return Transaction{}, err
	}

	if !access.Can(tenancybus.Receipts) {
		return Transaction{}, ErrForbidden
	}

	if t.Payee == payee {
		return t, nil
	}

	ev := eventbus.New(now, actor, account.Scope(), TransactionPaidTo, map[string]string{
		"description": t.Description,
		"date":        t.PostedOn.String(),
		"before":      t.Payee,
		"payee":       payee,
	})

	if err := b.store.SetPayee(ctx, id, payee, ev); err != nil {
		return Transaction{}, err
	}

	t.Payee = payee

	return t, nil
}
