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
	"strings"
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

	// Holder is the cardholder the file says made the charge: a PDF's
	// person column, or a CSV's column mapped as the cardholder. On an
	// account whose statements arrive one file per cardholder it is part
	// of the row's identity (docs/clearing.md, 3).
	Holder string

	// CheckNumber is the number of the check the row paid, when the file
	// says: a bank statement's checks paid, OFX's CHECKNUM, or a CSV's
	// column mapped as the check number (CheckNumber). It is no part of
	// the row's identity; it is what a check's image is matched by.
	CheckNumber string

	// Pending is a charge the document lists as not yet posted: in a
	// section headed so, among the rows a stated pending total accounts
	// for, or with "pending" in a CSV's status column (docs/clearing.md,
	// 4). Its amount may change when it posts.
	Pending bool
}

// CheckNumber is a check's number as a file printed it, as it is kept: its
// digits, without the leading zeros some banks pad it with, so that the
// check numbered "0001176" in one file and "1176" in another is one check.
// Anything that is not a number of up to ten digits -- a slip's reference,
// a word -- is no check number, and is "".
func CheckNumber(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))

	notDigit := func(r rune) bool { return r < '0' || r > '9' }

	if s == "" || len(s) > 10 || strings.ContainsFunc(s, notDigit) {
		return ""
	}

	if s = strings.TrimLeft(s, "0"); s == "" {
		return ""
	}

	return s
}

// CheckUnsaid is a check's number when its description does not already
// say it, for a page to show beside the description: a bank's statement
// calls its row "Check 1176", and a CSV may call it "CHECK" and put the
// number in a column of its own.
func CheckUnsaid(number, description string) string {
	if number == "" || strings.Contains(description, number) {
		return ""
	}

	return number
}

// CheckUnsaid is the row's check number when its description does not say
// it (CheckUnsaid).
func (r Record) CheckUnsaid() string { return CheckUnsaid(r.CheckNumber, r.Description) }

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

	// Pending is the total the file says is pending, when it says
	// ("Pending purchases $14.50"), and Unplaced a non-zero one that no
	// rows at the head of the list add up to, so that none were marked.
	Pending  Total
	Unplaced bool

	// Daily is the balances the file states at the end of some days apart
	// from its rows: a bank statement's "daily ending balance" table. Each
	// is checked against the rows (ledgerbus.verify), as a balance printed
	// on a row is.
	Daily []Balance

	// Last4 is the last four digits of the account number a file of one
	// account names, when it names one: an OFX download's ACCTID, the
	// account number a PDF prints. It says which account the file is for,
	// when many files arrive at once (ledgerbus, bulk.go).
	Last4 string

	// Accounts is a document that holds several accounts -- a bank's
	// consolidated statement -- read as one Result each; Records and the
	// rest above are then empty, and whoever imports it chooses one.
	Accounts []Account

	// Structure is the words the file used for its layout (shape.go), for
	// recognizing it. The whole document's, on the Result a reader
	// returns, and not on each of its Accounts.
	Structure Structure
}

// Last4 is the last four digits of an account number as a file prints
// it, or nothing if it has fewer: whatever is between them -- spaces,
// hyphens, the asterisks that mask the rest -- is not part of it.
func Last4(number string) string {
	var digits []byte

	for i := range len(number) {
		if c := number[i]; c >= '0' && c <= '9' {
			digits = append(digits, c)
		}
	}

	if len(digits) < 4 {
		return ""
	}

	return string(digits[len(digits)-4:])
}

// Account is one account's part of a document that holds several.
type Account struct {
	// Last4 is the last four digits of the number the document printed
	// for it: enough to tell the parts apart and to match the account
	// they are imported into, and no more is kept.
	Last4 string

	Result Result
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
