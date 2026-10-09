// Package pdfsourcetest draws the statements the PDF reader's tests and the
// ledger's read: invented accounts, payees and amounts, laid out the way a
// bank lays out its own statements, so that what the reader is tested on is
// the layout and nothing of anybody's money.
package pdfsourcetest

import (
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
)

// The consolidated statement's two accounts, by the last four digits of
// their invented numbers, and what each comes to.
const (
	First  = "1111"
	Second = "2222"

	FirstOpening, FirstClosing   = "1000.00", "1420.00"
	SecondOpening, SecondClosing = "500.00", "450.00"
)

// Consolidated draws a bank's statement for two checking accounts in one
// document, the way a bank's own statement lays them out rather than a
// page printed from its website:
//
//   - each account under its own "Account Number", with a summary of its
//     beginning and ending balance;
//   - its rows by kind, under headings that say which way the money went,
//     with no sign on any amount;
//   - the checks paid, each row starting with the check's number, one with
//     the date written on the check in its description, and one whose
//     number the page left alone on the line above the rest of its row;
//   - a description that starts with an earlier date than the row's;
//   - the last deposit of a section broken at the foot of the page, its
//     date on one line and the rest on the next;
//   - the balance at the end of each day with activity, in a table of its
//     own, three pairs to a line, one pair broken across lines by a mark
//     the bank hides in the text;
//   - the date range, the primary account and the page count at the top
//     of every page.
func Consolidated() []byte {
	head := func(n string) pdftexttest.Page {
		p := pdftexttest.Row(20, 330, "September 01, 2026 through September 30, 2026")
		p = append(p, pdftexttest.Row(32, 330, "Primary Account: 000000001111")...)

		return append(p, pdftexttest.Row(44, 480, "Page "+n+" of 3")...)
	}

	cols := func(y float64) []pdftexttest.Text {
		return pdftexttest.Row(y, 60, "DATE", 130, "DESCRIPTION", 520, "AMOUNT")
	}

	row := func(y float64, date, desc, amount string) []pdftexttest.Text {
		return pdftexttest.Row(y, 60, date, 130, desc, 520, amount)
	}

	// Page 1: the summary of both, and the first account's deposits.
	p1 := head("1")
	p1 = append(p1, pdftexttest.Row(80, 40, "CONSOLIDATED BALANCE SUMMARY")...)
	p1 = append(p1, pdftexttest.Row(100, 40, "Example Business Checking", 250, "000000001111", 400, "$1,000.00", 500, "$1,420.00")...)
	p1 = append(p1, pdftexttest.Row(115, 40, "Example Business Checking", 250, "000000002222", 400, "$500.00", 500, "$450.00")...)
	p1 = append(p1, pdftexttest.Row(150, 40, "EXAMPLE BUSINESS CHECKING", 330, "Account Number: 000000001111")...)
	p1 = append(p1, pdftexttest.Row(175, 40, "CHECKING SUMMARY")...)
	p1 = append(p1, pdftexttest.Row(190, 60, "Beginning Balance", 500, "$1,000.00")...)
	p1 = append(p1, pdftexttest.Row(205, 60, "Ending Balance", 400, "8", 500, "$1,420.00")...)
	p1 = append(p1, pdftexttest.Row(240, 10, "*start*deposits and additions")...)
	p1 = append(p1, pdftexttest.Row(255, 40, "DEPOSITS AND ADDITIONS")...)
	p1 = append(p1, cols(275)...)
	p1 = append(p1, row(295, "09/02", "Remote Online Deposit", "$300.00")...)
	p1 = append(p1, row(310, "09/15", "Orig CO Name:Example Payroll", "150.00")...)
	p1 = append(p1, pdftexttest.Row(322, 130, "Descr:Dir Dep Sec:PPD")...)
	p1 = append(p1, pdftexttest.Row(735, 60, "09/28")...)
	p1 = append(p1, pdftexttest.Row(745, 10, "*end*deposits and additions")...)
	p1 = append(p1, row(757, "", "Remote Online Deposit", "75.00")...)

	// Page 2: the first account's checks, withdrawals and daily balances.
	p2 := head("2")
	p2 = append(p2, pdftexttest.Row(80, 40, "CHECKS PAID")...)
	p2 = append(p2, pdftexttest.Row(95, 430, "DATE")...)
	p2 = append(p2, pdftexttest.Row(105, 40, "CHECK NO.", 130, "DESCRIPTION", 430, "PAID", 520, "AMOUNT")...)
	p2 = append(p2, pdftexttest.Row(125, 40, "1001 ^", 430, "09/03", 520, "$40.00")...)
	p2 = append(p2, pdftexttest.Row(140, 40, "1002 * ^", 130, "09/09", 430, "09/10", 520, "25.00")...)
	p2 = append(p2, pdftexttest.Row(155, 40, "1003 ^")...)
	p2 = append(p2, pdftexttest.Row(167, 430, "09/12", 520, "10.00")...)
	p2 = append(p2, pdftexttest.Row(185, 60, "Total Checks Paid", 520, "$75.00")...)
	p2 = append(p2, pdftexttest.Row(215, 40, "ELECTRONIC WITHDRAWALS")...)
	p2 = append(p2, cols(230)...)
	p2 = append(p2, row(250, "09/16", "09/14/2026 Debit For An Item Processed Twice", "$5.00")...)
	p2 = append(p2, row(265, "09/20", "Online Transfer To Chk ...2222", "25.00")...)
	p2 = append(p2, pdftexttest.Row(290, 40, "DAILY ENDING BALANCE")...)
	p2 = append(p2, pdftexttest.Row(305, 60, "DATE", 160, "AMOUNT", 260, "DATE", 360, "AMOUNT", 440, "DATE", 520, "AMOUNT")...)
	p2 = append(p2, pdftexttest.Row(320, 60, "09/02", 160, "$1,300.00", 260, "09/12", 360, "1,225.00", 440, "09/20", 520, "1,345.00")...)
	p2 = append(p2, pdftexttest.Row(333, 60, "09/03", 160, "1,260.00", 260, "09/15", 360, "1,375.00", 440, "09/28", 520, "1,420.00")...)
	p2 = append(p2, pdftexttest.Row(346, 60, "09/10")...)
	p2 = append(p2, pdftexttest.Row(354, 10, "*end*daily ending balance")...)
	p2 = append(p2, pdftexttest.Row(362, 160, "1,235.00", 260, "09/16", 360, "1,370.00")...)
	p2 = append(p2, pdftexttest.Row(390, 40, "SERVICE CHARGE SUMMARY")...)
	p2 = append(p2, pdftexttest.Row(405, 60, "Monthly Service Fee", 520, "$0.00")...)

	// Page 3: the second account.
	p3 := head("3")
	p3 = append(p3, pdftexttest.Row(80, 40, "EXAMPLE BUSINESS CHECKING", 330, "Account Number: 000000002222")...)
	p3 = append(p3, pdftexttest.Row(100, 40, "CHECKING SUMMARY")...)
	p3 = append(p3, pdftexttest.Row(115, 60, "Beginning Balance", 500, "$500.00")...)
	p3 = append(p3, pdftexttest.Row(130, 60, "Ending Balance", 400, "2", 500, "$450.00")...)
	p3 = append(p3, pdftexttest.Row(160, 40, "DEPOSITS AND ADDITIONS")...)
	p3 = append(p3, cols(175)...)
	p3 = append(p3, row(195, "09/20", "Online Transfer From Chk ...1111", "$25.00")...)
	p3 = append(p3, pdftexttest.Row(225, 40, "ATM & DEBIT CARD WITHDRAWALS")...)
	p3 = append(p3, cols(240)...)
	p3 = append(p3, row(260, "09/21", "Card Purchase 09/19 Corner Grocery Card 9999", "$75.00")...)
	p3 = append(p3, pdftexttest.Row(290, 40, "DAILY ENDING BALANCE")...)
	p3 = append(p3, pdftexttest.Row(305, 60, "DATE", 160, "AMOUNT", 260, "DATE", 360, "AMOUNT")...)
	p3 = append(p3, pdftexttest.Row(320, 60, "09/20", 160, "$525.00", 260, "09/21", 360, "450.00")...)

	return pdftexttest.Draw(p1, p2, p3)
}
