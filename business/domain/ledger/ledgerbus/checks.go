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

//
// The image says two more things the books need (issue #71): what the
// check was for, on its memo line, and the day it was written, which is
// not the day it cleared -- a check written on 23 September and cleared on
// the 24th, or on 2 January, belongs to the year it was written for an
// accountant's 1099s and lists of checks outstanding. Both are written on
// the transaction beside the payee (SetWritten), and the clearing date
// stays its date.

// MaxPayee is how long a payee may be, as long as a receipt's shop.
const MaxPayee = 100

// MaxCheckMemo is how long a check's memo may be: a line on a check.
const MaxCheckMemo = 100

// ErrPayee is a payee longer than MaxPayee.
var ErrPayee = errors.New("who a check was paid to is at most 100 characters")

// ErrCheckMemo is a memo longer than MaxCheckMemo.
var ErrCheckMemo = errors.New("a check's memo is at most 100 characters")

// TransactionPaidTo is the history's line for a payee written down,
// changed or taken away.
const TransactionPaidTo eventbus.Action = "transaction.paid-to"

// TransactionWritten is the history's line for a check's memo and the day
// it was written, written down, changed or taken away.
const TransactionWritten eventbus.Action = "transaction.written"

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

// SetWritten writes down what a check's memo line says and the day written
// on it, or takes them away with "" and a zero day. Who may is who may
// write its payee, and for the same reason a reconciled period does not
// stop it: a note, not money.
func (b *Business) SetWritten(ctx context.Context, now time.Time, actor, id types.ID, memo string, on types.Date) (Transaction, error) {
	memo = strings.Join(strings.Fields(memo), " ")
	if utf8.RuneCountInString(memo) > MaxCheckMemo {
		return Transaction{}, ErrCheckMemo
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

	if t.CheckMemo == memo && t.WrittenOn == on {
		return t, nil
	}

	ev := eventbus.New(now, actor, account.Scope(), TransactionWritten, map[string]string{
		"description": t.Description,
		"date":        t.PostedOn.String(),
		"memo":        memo,
		"written":     on.String(),
	})

	if err := b.store.SetWritten(ctx, id, memo, on, ev); err != nil {
		return Transaction{}, err
	}

	t.CheckMemo, t.WrittenOn = memo, on

	return t, nil
}
