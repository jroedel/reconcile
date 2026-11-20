package receiptbus

import (
	"context"
	"slices"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// What a check was on a day, rather than what it is now.
//
// A statement from March and an accountant's package for March both ask
// which checks were outstanding on March 31. "Still waiting now" is the
// answer only while March is the month going: a check written on the 28th
// that cleared on April 2 was outstanding on the 31st -- it is the very
// difference between the bank's balance that day and the books' -- and by
// May it is attached and waiting no longer. So the answer for a day is
// worked out from what the check's image keeps: the day it was written,
// the transactions it is attached to and the days they were posted, and
// when it was voided. Nothing is stored for a day.

// CheckLine is one check's image and what it was on the day asked about.
type CheckLine struct {
	Receipt

	// State is NumberOutstanding, NumberAttached (cleared) or NumberVoid,
	// on the day.
	State CheckState

	// ClearedOn is the day the bank paid it, and VoidedOn the day it was
	// voided, whenever that was: after the day asked about too, so that a
	// list of what was outstanding then can say what became of each.
	ClearedOn types.Date
	VoidedOn  types.Date
}

// checksIn is every numbered check image in the account that was not
// removed, each with the transactions it is attached to.
func (b *Business) checksIn(ctx context.Context, accountID types.ID) ([]Receipt, map[types.ID]ledgerbus.Transaction, error) {
	all, err := b.store.InHomes(ctx, []types.Scope{types.AccountScope(accountID)}, false)
	if err != nil {
		return nil, nil, err
	}

	var (
		checks []Receipt
		ids    []types.ID
	)

	for _, r := range all {
		if !r.CheckImage || r.Check == "" || r.Removed() {
			continue
		}

		checks = append(checks, r)

		for _, l := range r.Links {
			ids = append(ids, l.TransactionID)
		}
	}

	paid := map[types.ID]ledgerbus.Transaction{}

	if len(ids) > 0 {
		txs, err := b.ledger.LookupAll(ctx, ids)
		if err != nil {
			return nil, nil, err
		}

		for _, t := range txs {
			paid[t.ID] = t
		}
	}

	return checks, paid, nil
}

// lineOn is what the check was on day, in loc for the moment it was
// voided: cleared once a transaction it is attached to had been posted,
// void once it had been voided, and outstanding otherwise. A void is a
// moment and a posting is a day, so the void is taken as the day it fell
// on where the reader is.
func lineOn(r Receipt, paid map[types.ID]ledgerbus.Transaction, day types.Date, loc *time.Location) CheckLine {
	line := CheckLine{Receipt: r, State: NumberOutstanding}

	for _, l := range r.Links {
		t, ok := paid[l.TransactionID]
		if ok && (line.ClearedOn.Zero() || t.PostedOn.Before(line.ClearedOn)) {
			line.ClearedOn = t.PostedOn
		}
	}

	if r.Voided() {
		line.VoidedOn = types.DateOf(r.VoidedAt.In(loc))
	}

	switch {
	case !line.ClearedOn.Zero() && !day.Before(line.ClearedOn):
		line.State = NumberAttached
	case !line.VoidedOn.Zero() && !day.Before(line.VoidedOn):
		line.State = NumberVoid
	}

	return line
}

// written is the day the check counts as written: the day written on it,
// or, when nobody has read that, the day its image was added here. A check
// photographed in May whose date is not read is not one of March's.
func written(r Receipt, loc *time.Location) types.Date {
	if !r.WrittenOn.Zero() {
		return r.WrittenOn
	}

	return types.DateOf(r.CreatedAt.In(loc))
}

// outstandingOn is the account's checks that were outstanding at the end of
// day: written by then, and neither cleared nor voided by then, whatever
// became of them since.
func (b *Business) outstandingOn(ctx context.Context, accountID types.ID, day types.Date, loc *time.Location) ([]CheckLine, error) {
	checks, paid, err := b.checksIn(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var out []CheckLine

	for _, r := range checks {
		if day.Before(written(r, loc)) {
			continue
		}

		if line := lineOn(r, paid, day, loc); line.State == NumberOutstanding {
			out = append(out, line)
		}
	}

	return slices.Clip(out), nil
}
