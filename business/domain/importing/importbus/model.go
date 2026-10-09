// Package importbus is what every statement reader produces: the
// transactions a file lists, the balances it states, and what it could not
// read. Adapted from eumaeus, where it was written for one person's ledger
// read from the command line; here the reader is a web page, so what could
// not be read is a code a template words (Warning), not an English sentence.
//
// The readers are in sources/: csvsource for any bank's CSV export, with a
// column mapping, ofxsource for OFX and QFX downloads, and pdfsource for a
// statement or a bank's page as a PDF. What becomes of
// the records -- checking them against their balances, setting aside the
// ones already imported, storing the rest -- is the ledger's (ledgerbus).
package importbus

import (
	"time"

	"github.com/jroedel/reconcile/business/types/money"
)

// Record is one transaction as a file reported it.
type Record struct {
	// Line is where it is in the file, counting from 1, so that a page can
	// point at the row a check failed on. In a PDF it is a line of the
	// text read out of it, which nobody can see; Page is what a person can
	// find, and is zero for any other format.
	Line int
	Page int

	Date        time.Time
	Description string

	// Amount is signed: money into the account is positive.
	Amount money.Amount

	// Balance is the running balance the file printed beside this row, when
	// it has a balance column. HasBalance separates "the file said zero"
	// from "the file said nothing".
	Balance    money.Amount
	HasBalance bool

	// ExternalID is the bank's own identifier for the transaction, when the
	// format carries one: an OFX FITID. A CSV has none, and the ledger falls
	// back to a hash of the content (ledgerbus).
	ExternalID string

	// Memo is what the file says about the transaction beside its
	// description: on a card's printout, which cardholder made the charge.
	// It becomes the memo of the transaction's one part, and is no part of
	// its identity.
	Memo string
}

// Balance is a balance a file stated, at a date.
type Balance struct {
	Amount money.Amount
	AsOf   time.Time
	Known  bool
}

// Total is a sum of the rows a file states, in the file's own sign.
type Total struct {
	Amount money.Amount
	Known  bool
}

// Warning is a line the reader could not use, said as a code and the value
// that was wrong, for a template to word. Page is a PDF's, as Record's.
type Warning struct {
	Line    int
	Page    int
	Problem string
	Value   string
}

// The problems a Warning names.
const (
	BadRow            = "row"       // not a CSV row at all: a stray quote, say
	NoDate            = "no-date"   // the date column is empty
	BadDate           = "date"      // the date is in no format we know
	NoAmount          = "no-amount" // no amount, or neither debit nor credit
	BadAmount         = "amount"    // an amount that is not a number
	BadBalance        = "balance"   // a balance that is not a number
	NoFITID           = "no-fitid"  // an OFX transaction with no identifier
	SeveralStatements = "several"   // an OFX file holding several accounts
	BadLedgerBalance  = "ledger-balance"
	Unclear           = "unclear" // a PDF line that looks like a transaction and is not one this can read
)

// Result is what one read of a file produced.
//
// A partial read is the ordinary outcome, not an exceptional one: an export
// repeats its header mid-file, or ends in a total row. The rows that could
// not be read are counted and described rather than failing the file -- but
// never dropped silently, because a silent skip is how a month goes missing.
type Result struct {
	Records  []Record
	Skipped  int
	Warnings []Warning

	// Opening and Closing are the balances the file states at its start
	// and its end, if it states them: OFX's ledger balance, a statement's
	// previous and new balance.
	Opening, Closing Balance

	// Total is what the file says its rows add up to, when it says: a
	// card's printed activity states its total and no balance at all.
	Total Total

	// Start and End are the period the file says it covers, when it says.
	Start, End time.Time
}

// maxWarnings caps the list. A wrong column mapping produces one warning per
// row, and three hundred of them is not a better message than twenty and a
// count.
const maxWarnings = 20

// Warn records a line that could not be used.
func (r *Result) Warn(w Warning) {
	if len(r.Warnings) < maxWarnings {
		r.Warnings = append(r.Warnings, w)
	}
}
