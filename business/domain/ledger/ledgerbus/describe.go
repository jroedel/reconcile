package ledgerbus

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A transaction's description is the bank's: "Check 1322", "POS 4471
// HILLTOP". It is what the bank will say again in the next file, so it is
// what deduplicating, a pending charge's posting and sorting rules read,
// and it never changes. What it was is a person's to say -- "Summer work",
// read off the check's memo line -- and that goes beside it, as the
// transaction's own description (Transaction.OwnDescription), shown in its
// place on every page, with the bank's still on the transaction's own.
//
// Sorting rules and suggestions keep reading the bank's words and the
// payee (Words), not this: a rule is written for what next month's
// statement will say, and a person's words for one charge are not that.
// Finding a transaction reads both, since a person looks for it by either.
//
// Changing it is bookkeeping, as its parts are: a bookkeeper's, and not in
// a reconciled period, whose package the accountant may already have.
// Through a key it is marked, as a part sorted through one is, until a
// person saves the transaction on the web.

// ErrDescription is a description longer than MaxDescription, the same
// length as an entry by hand's (hand.go): both are a person's words.
var ErrDescription = errors.New("a description of your own is at most 200 characters")

// TransactionDescribed is the history's line for a description of one's
// own written, changed or taken away.
const TransactionDescribed eventbus.Action = "transaction.described"

// Shown is what a project's book calls the line's transaction, as
// Transaction.Shown.
func (l ProjectLine) Shown() string {
	if l.OwnDescription != "" {
		return l.OwnDescription
	}

	return l.Description
}

// Describe writes what a transaction was, in place of the bank's
// description wherever it is shown, or takes it away with "".
//
// Saved on the web, it takes off the mark of a program that wrote it, even
// unchanged: saving says a person has looked. Saved through a key with the
// words it has already, nothing changes, so that a program repeating
// itself does not mark again what a person has checked.
func (b *Business) Describe(ctx context.Context, now time.Time, actor, id types.ID, description string) (Transaction, error) {
	description = strings.Join(strings.Fields(description), " ")
	if utf8.RuneCountInString(description) > MaxDescription {
		return Transaction{}, ErrDescription
	}

	t, err := b.store.TransactionByID(ctx, id)
	if err != nil {
		return Transaction{}, err
	}

	account, access, err := b.accounts.Account(ctx, actor, t.AccountID)
	if err != nil {
		return Transaction{}, err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return Transaction{}, ErrForbidden
	}

	lock, err := b.lock(ctx, t.AccountID, t.PostedOn)
	if err != nil {
		return Transaction{}, err
	}

	if lock.Made() {
		return Transaction{}, ErrLocked
	}

	// There is nothing to check about a description taken away, so it
	// carries no mark, whoever took it.
	via := eventbus.ViaFrom(ctx)
	if description == "" {
		via = ""
	}

	var ev *eventbus.Event

	switch {
	case description != t.OwnDescription:
		e := eventbus.New(now, actor, account.Scope(), TransactionDescribed, map[string]string{
			"description": t.Description,
			"date":        t.PostedOn.String(),
			"before":      t.OwnDescription,
			"own":         description,
		})
		ev = &e
	case via == "" && t.OwnVia != "":
		// A person saved what a program wrote: the mark goes, and the
		// history has nothing new to say.
	default:
		return t, nil
	}

	if err := b.store.SetOwnDescription(ctx, id, description, via, ev); err != nil {
		return Transaction{}, err
	}

	t.OwnDescription, t.OwnVia = description, via

	return t, nil
}
