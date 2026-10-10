package pdfsource_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
	"github.com/jroedel/reconcile/business/types/money"
)

// The documents below are invented, and laid out as pdftotext -layout lays
// out the two shapes this reads: a card's activity page printed from a
// browser, and a bank's statement with a running balance.

// printout is a card's activity page as a browser prints it: a header and
// a footer on every page, the cardholder in a column of their own, and a
// row whose description was pushed onto the next page.
const printout = `Example Card Services Account Activity                                  10/2/26, 4:15 PM


          GOOD SHEPHERD PARISH

          Current Balance                       Available Credit
          $1,234.56                             $8,765.44

          Sep 1, 2026 to Sep 30, 2026 | PAT EXAMPLE

          Date                     Description                                Name                    Amount


          Sep 30, 2026                       Corner Hardware                  PAT EXAMPLE…             $3.78



          Sep 29, 2026                                                        PAT EXAMPLE…            $40.00

https://cards.example.invalid/activity?accountId=00000000-0000-0000-0000-000000000000          Page 1 of 2
` + "\f" + `Example Card Services Account Activity                                  10/2/26, 4:15 PM



                                             Hilltop Clinic Spri…



          Sep 23, 2026                       Café Lumen                       PAT EXAMPLE…    -$0.44



          Sep 02, 2026                       TFR FRM CHK 1080                 PAT EXAMPLE…   $1,203.22



          End of Activity                Total Activity Date range                    $1,246.56


                                         Purchases                                    $1,247.00

                                         Credits                                         -$0.44

https://cards.example.invalid/activity?accountId=00000000-0000-0000-0000-000000000000          Page 2 of 2
` + "\f"

func TestAPrintedActivityPage(t *testing.T) {
	res, err := pdfsource.Read(printout)
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		date, desc, memo, amount string
		page                     int
	}

	var got []row
	for _, r := range res.Records {
		got = append(got, row{r.Date.Format("2006-01-02"), r.Description, r.Memo, r.Amount.String(), r.Page})

		// The person column is the cardholder, for an account split by
		// cardholder (docs/clearing.md, 3).
		if r.Holder != r.Memo {
			t.Errorf("holder %q, memo %q", r.Holder, r.Memo)
		}
	}

	want := []row{
		{"2026-09-30", "Corner Hardware", "PAT EXAMPLE…", "3.78", 1},
		{"2026-09-29", "Hilltop Clinic Spri…", "PAT EXAMPLE…", "40.00", 1},
		{"2026-09-23", "Café Lumen", "PAT EXAMPLE…", "-0.44", 2},
		{"2026-09-02", "TFR FRM CHK 1080", "PAT EXAMPLE…", "1203.22", 2},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d:\n%v", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if res.Total != (importbus.Total{Amount: money.MustParse("1246.56"), Known: true}) {
		t.Errorf("total = %+v", res.Total)
	}

	if res.Opening.Known || res.Closing.Known {
		t.Errorf("balances = %+v, %+v; a current balance is not the period's", res.Opening, res.Closing)
	}

	if !res.Start.Equal(date("2026-09-01")) || !res.End.Equal(date("2026-09-30")) {
		t.Errorf("period = %v to %v", res.Start, res.End)
	}

	for _, r := range res.Records {
		if strings.Contains(r.Description+r.Memo, "accountId") || strings.Contains(r.Description, "Activity") {
			t.Errorf("furniture reached a row: %+v", r)
		}
	}
}

// statement is a bank's statement: money out and money in in two columns
// without signs, a balance on every row, dates without a year, and a
// period across the new year.
const statement = `                              EXAMPLE SAVINGS BANK
                       Statement period 12/15/2025 through 01/14/2026

Date      Description                              Withdrawals      Deposits        Balance
12/15     Beginning balance                                                        1,000.00
12/18     Card purchase Corner Hardware                  25.00                       975.00
12/31     Deposit                                                    500.00        1,475.00
01/05     Check 1042                                    100.00                     1,375.00
          Payee: Parish Office Supply
01/14     Ending balance                                                           1,375.00

Interest rates are subject to change. Please see the reverse for details.
`

func TestAStatementWithARunningBalance(t *testing.T) {
	res, err := pdfsource.Read(statement)
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		date, desc, amount, balance string
	}{
		{"2025-12-18", "Card purchase Corner Hardware", "-25.00", "975.00"},
		{"2025-12-31", "Deposit", "500.00", "1475.00"},
		{"2026-01-05", "Check 1042 Payee: Parish Office Supply", "-100.00", "1375.00"},
	}

	if len(res.Records) != len(want) {
		t.Fatalf("got %d rows: %+v", len(res.Records), res.Records)
	}

	for i, w := range want {
		r := res.Records[i]
		if r.Date.Format("2006-01-02") != w.date || r.Description != w.desc || r.Amount.String() != w.amount || !r.HasBalance || r.Balance.String() != w.balance {
			t.Errorf("row %d = %s %q %s %s, want %+v", i, r.Date.Format("2006-01-02"), r.Description, r.Amount, r.Balance, w)
		}
	}

	if !res.Opening.Known || res.Opening.Amount.String() != "1000.00" || !res.Closing.Known || res.Closing.Amount.String() != "1375.00" {
		t.Errorf("balances = %+v, %+v", res.Opening, res.Closing)
	}
}

// Without an opening balance the first row's sign comes from the column
// its amount is in, which the other rows' balances have told apart.
func TestTheFirstRowsSignFromItsColumn(t *testing.T) {
	text := `Date        Description              Debits       Credits       Balance
03/02/2026  Coffee for the hall       12.00                       988.00
03/03/2026  Bake sale                               40.00       1,028.00
03/04/2026  Paper                      8.00                     1,020.00
`

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range res.Records {
		got = append(got, r.Amount.String())
	}

	if strings.Join(got, " ") != "-12.00 40.00 -8.00" {
		t.Errorf("amounts = %v", got)
	}
}

// Newest first, as a bank's page lists them; the signs still follow the
// balances in the order of the dates.
func TestNewestFirst(t *testing.T) {
	text := `Date        Description              Amount        Balance
03/04/2026  Paper                      8.00       1,020.00
03/03/2026  Bake sale                 40.00       1,028.00
03/02/2026  Coffee for the hall       12.00         988.00
`

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range res.Records {
		got = append(got, r.Amount.String())
	}

	// The first by date has no balance before it and every amount ends in
	// one column, so it keeps its sign as printed: the ledger's check,
	// which tries both, decides whether the rest hold.
	if strings.Join(got, " ") != "-8.00 40.00 12.00" {
		t.Errorf("amounts = %v", got)
	}
}

func TestDayFirstDatesAndMarkedAmounts(t *testing.T) {
	text := `Statement 01/03/2026 - 31/03/2026
Date        Description                   Amount
13/03/2026  Refund, Corner Hardware       (12.50)
14/03/2026  Payment received             100.00 CR
15/03/2026  Annual fee                    45.00-
16/03/2026  Hall rental                   60.00 DR
`

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"2026-03-13 -12.50", "2026-03-14 -100.00", "2026-03-15 -45.00", "2026-03-16 60.00"}

	for i, r := range res.Records {
		if got := r.Date.Format("2006-01-02") + " " + r.Amount.String(); i >= len(want) || got != want[i] {
			t.Errorf("row %d = %s", i, got)
		}
	}

	if len(res.Records) != len(want) {
		t.Errorf("got %d rows", len(res.Records))
	}
}

// A row with more figures than an amount and a balance is not guessed at:
// it is skipped and said, and the ledger's check finds what it missed.
func TestARowItCannotRead(t *testing.T) {
	text := `Date        Description                Amount     Balance
03/02/2026  Coffee                      12.00      988.00     5.00
03/03/2026  Bake sale                   40.00    1,028.00
`

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Records) != 1 || res.Skipped != 1 || len(res.Warnings) != 1 || res.Warnings[0].Problem != importbus.Unclear {
		t.Errorf("records %d, skipped %d, warnings %+v", len(res.Records), res.Skipped, res.Warnings)
	}
}

func TestNothingToRead(t *testing.T) {
	for _, text := range []string{"", "\f\f", "Dear member,\n\nThank you for banking with us.\n"} {
		if _, err := pdfsource.Read(text); !errors.Is(err, pdfsource.ErrNoRows) {
			t.Errorf("Read(%q) = %v, want ErrNoRows", text, err)
		}
	}
}

func date(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)

	return t
}

// A shop named for its website is a row, though the page's footer --
// its address, a web address too -- is not. Here in the layout that puts
// the cardholder before the description.
func TestAShopNamedForItsWebsite(t *testing.T) {
	text := `Example Card Services Account Activity                                  10/2/26, 4:15 PM

          Apr 1, 2026 to Apr 30, 2026 | PAT EXAMPLE

          Date                 Name                     Description                          Amount

          Apr 23, 2026         PAT EXAMPLE              Corner Hardware                      $27.11

          Apr 01, 2026         PAT EXAMPLE              WWW.EXAMPLE-INSURER.INVALID…        $956.04

          End of Activity                 Total Activity Date range                       $983.15

https://cards.example.invalid/activity?accountId=0000-0000          Page 1 of 1
`

	res, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	var got []string

	sum := money.Amount(0)
	for _, r := range res.Records {
		got = append(got, r.Description)
		sum += r.Amount
	}

	if len(res.Records) != 2 || got[1] != "WWW.EXAMPLE-INSURER.INVALID…" || res.Records[1].Holder != "PAT EXAMPLE" {
		t.Fatalf("rows %q: %+v", got, res.Records)
	}

	if !res.Total.Known || res.Total.Amount != sum {
		t.Errorf("total %+v, the rows %s", res.Total, sum)
	}
}
