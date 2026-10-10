package ledgerbus

import (
	"slices"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// Check is what checking a statement found.
type Check struct {
	Method Method
	OK     bool

	// When it did not balance: the line it broke on (zero for a check of
	// totals, which cannot say where), what the balance should have been
	// there, and what the statement said.
	Line             int
	Expected, Stated money.Amount

	// Page and Date are the row it broke on as a person finds it in a PDF,
	// whose lines are the text read out of it rather than anything on the
	// page.
	Page int
	Date time.Time

	// Opening and Closing are the balance before the first row and after
	// the last, as the file printed them, when it balanced row by row:
	// the closing balance is what a person compares with the paper
	// statement when reconciling, and a file that prints a balance on
	// every row states it as surely as one that prints it once.
	Opening, Closing money.Amount
}

// Failed reports a check that was made and did not hold. An unchecked
// statement has not failed.
func (c Check) Failed() bool { return c.Method != Unchecked && !c.OK }

// verify checks what a file listed against the balances it states or a
// person typed. The idea is eumaeus's: the running balance is the truth,
// and an import is believed only as far as it agrees with it.
//
// Neither the order of the rows nor the sign of a balance is known. Banks
// list newest first as often as oldest first, and a card's balance is what
// is owed, so a charge (money out, negative) raises it. Each is tried both
// ways, and the statement balances if any of the four does. Accepting the
// wrong combination by accident needs the rows to balance read backwards or
// sign-flipped, which a dropped or doubled row does not do.
//
// A document that states neither balances nor a balance on each row, but
// the total of its rows -- a card's printed activity -- is checked by that
// (BySum), in either sign, since the total is printed the bank's way round
// and the rows may have been turned to the account holder's. Balances come
// first: they check the rows and give the statement its balances as well.
//
// debt says which sign is likelier -- what the account's balance means to
// the person reading it -- and so which to report a failure in.
func verify(recs []importbus.Record, opening, closing importbus.Balance, total importbus.Total, debt bool) Check {
	signs := []money.Amount{1, -1}
	if debt {
		signs = []money.Amount{-1, 1}
	}

	if balances(recs) >= 2 {
		// Oldest first is the likelier reading of a file whose dates run
		// forward, and newest first of one whose dates run back. Both are
		// tried; the likelier one, with the sign the account's kind
		// suggests, is the one a failure is reported in. "Furthest before
		// it broke" was tried instead, and on a short file it picked a
		// reading that was wrong from its first row.
		forward := slices.Clone(recs)
		backward := slices.Clone(recs)
		slices.Reverse(backward)

		orders := [][]importbus.Record{forward, backward}
		if last := len(recs) - 1; recs[last].Date.Before(recs[0].Date) {
			orders[0], orders[1] = backward, forward
		}

		for _, ordered := range orders {
			for _, sign := range signs {
				if c := chain(ordered, sign); c.OK {
					return heldToTotals(c, recs, opening, closing, sign)
				}
			}
		}

		return chain(orders[0], signs[0])
	}

	if opening.Known && closing.Known {
		var sum money.Amount
		for _, r := range recs {
			sum += r.Amount
		}

		for _, sign := range signs {
			if opening.Amount+sign*sum == closing.Amount {
				return Check{Method: ByTotals, OK: true}
			}
		}

		return Check{Method: ByTotals, Expected: opening.Amount + signs[0]*sum, Stated: closing.Amount}
	}

	if total.Known {
		var sum money.Amount
		for _, r := range recs {
			sum += r.Amount
		}

		if sum == total.Amount || -sum == total.Amount {
			return Check{Method: BySum, OK: true}
		}

		// Said in the total's own sign, so the two figures on the page
		// can be compared at a glance.
		if (sum < 0) != (total.Amount < 0) {
			sum = -sum
		}

		return Check{Method: BySum, Expected: sum, Stated: total.Amount}
	}

	return Check{Method: Unchecked}
}

// Verify is verify, for a page that tries a reading of a document
// without importing it (shapebus.Try): the same check, so that what it
// says of a draft's reading is what an import would say.
func Verify(recs []importbus.Record, opening, closing importbus.Balance, total importbus.Total, debt bool) Check {
	return verify(recs, opening, closing, total, debt)
}

// heldToTotals holds a statement that balanced row by row to the opening and
// closing balances it states as well. The rows only check each other from
// the first printed balance on: a statement that prints one at the end of
// each day (pdfsource's daily balances) says nothing of a row missing from
// before its first day's balance, and the opening balance does.
func heldToTotals(c Check, recs []importbus.Record, opening, closing importbus.Balance, sign money.Amount) Check {
	if !opening.Known || !closing.Known {
		return c
	}

	var sum money.Amount
	for _, r := range recs {
		sum += r.Amount
	}

	if opening.Amount+sign*sum != closing.Amount {
		return Check{Method: ByTotals, Expected: opening.Amount + sign*sum, Stated: closing.Amount}
	}

	return c
}

// balances counts the rows that print a balance.
func balances(recs []importbus.Record) int {
	n := 0

	for _, r := range recs {
		if r.HasBalance {
			n++
		}
	}

	return n
}

// chain walks the rows in order, carrying the balance forward by each
// amount, and stops at the first printed balance that disagrees. A row with
// no balance printed -- some banks print one a day -- is carried through.
func chain(recs []importbus.Record, sign money.Amount) Check {
	var (
		running, before money.Amount
		known           bool
		opening         money.Amount
	)

	for _, r := range recs {
		if known {
			running += sign * r.Amount
		} else {
			before += sign * r.Amount
		}

		if !r.HasBalance {
			continue
		}

		if known && running != r.Balance {
			return Check{Method: ByBalances, Line: r.Line, Page: r.Page, Date: r.Date, Expected: running, Stated: r.Balance}
		}

		if !known {
			opening = r.Balance - before
		}

		running, known = r.Balance, true
	}

	return Check{Method: ByBalances, OK: true, Opening: opening, Closing: running}
}
