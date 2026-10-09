package pdfsource_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/pdftext"
)

// A bank's own statement for two accounts in one document, drawn and read
// back by the pdftotext the server runs (pdfsourcetest.Consolidated says
// what is on it). Each account is read on its own, every amount takes its
// sign from its section, and the daily balances land on the rows they
// follow.
func TestAConsolidatedStatement(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	text, err := pdftext.Extract(t.Context(), pdfsourcetest.Consolidated())
	if err != nil {
		t.Fatal(err)
	}

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Accounts) != 2 || len(res.Records) != 0 || res.Start.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("read %d accounts and %d loose rows:\n%s", len(res.Accounts), len(res.Records), text)
	}

	first, second := res.Accounts[0], res.Accounts[1]
	if first.Last4 != pdfsourcetest.First || second.Last4 != pdfsourcetest.Second {
		t.Errorf("accounts %q and %q", first.Last4, second.Last4)
	}

	lines := func(r importbus.Result) []string {
		var out []string

		for _, rec := range r.Records {
			bal := ""
			if rec.HasBalance {
				bal = " = " + rec.Balance.String()
			}

			out = append(out, fmt.Sprintf("%s %s %s%s", rec.Date.Format("01-02"), rec.Amount, rec.Description, bal))
		}

		return out
	}

	want := []string{
		"09-02 300.00 Remote Online Deposit = 1300.00",
		"09-03 -40.00 Check 1001 = 1260.00",
		"09-10 -25.00 Check 1002 = 1235.00",
		"09-12 -10.00 Check 1003 = 1225.00",
		"09-15 150.00 Orig CO Name:Example Payroll Descr:Dir Dep Sec:PPD = 1375.00",
		"09-16 -5.00 09/14/2026 Debit For An Item Processed Twice = 1370.00",
		"09-20 -25.00 Online Transfer To Chk ...2222 = 1345.00",
		"09-28 75.00 Remote Online Deposit = 1420.00",
	}

	if got := lines(first.Result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the first account:\n got %s\nwant %s\n\n%s", strings.Join(got, "\n     "), strings.Join(want, "\n     "), text)
	}

	o, c := first.Result.Opening, first.Result.Closing
	if !o.Known || !c.Known || o.Amount != money.MustParse(pdfsourcetest.FirstOpening) || c.Amount != money.MustParse(pdfsourcetest.FirstClosing) {
		t.Errorf("the first account's balances: %+v, %+v", o, c)
	}

	want = []string{
		"09-20 25.00 Online Transfer From Chk ...1111 = 525.00",
		"09-21 -75.00 Card Purchase 09/19 Corner Grocery Card 9999 = 450.00",
	}

	if got := lines(second.Result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the second account:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}

	if o := second.Result.Opening; o.Amount != money.MustParse(pdfsourcetest.SecondOpening) {
		t.Errorf("the second account's opening: %+v", o)
	}
}

// A statement whose amounts carry their own signs keeps them, whatever its
// headings say: a section's direction is only for amounts printed without
// one.
func TestSignedAmountsKeepTheirSigns(t *testing.T) {
	const text = `
          Statement period 09/01/2026 to 09/30/2026

          PAYMENTS AND CREDITS

          Date         Description                              Amount
          09/05        PAYMENT THANK YOU                       -100.00

          PURCHASES

          Date         Description                              Amount
          09/07        CORNER HARDWARE                           40.00
`

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Records) != 2 || res.Records[0].Amount != money.MustParse("-100.00") || res.Records[1].Amount != money.MustParse("40.00") {
		t.Errorf("records: %+v", res.Records)
	}
}
