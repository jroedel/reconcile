package csvsource_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
	"github.com/jroedel/reconcile/business/types/money"
)

// Every export below is invented.

func read(t *testing.T, data string, m csvsource.Mapping) importbus.Result {
	t.Helper()

	res, err := csvsource.Read([]byte(data), m)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

func detect(t *testing.T, data string) csvsource.Mapping {
	t.Helper()

	in, err := csvsource.Inspect([]byte(data), 0)
	if err != nil {
		t.Fatal(err)
	}

	return in.Detected
}

func amounts(res importbus.Result) []money.Amount {
	out := make([]money.Amount, len(res.Records))
	for i, r := range res.Records {
		out[i] = r.Amount
	}

	return out
}

func wantAmounts(t *testing.T, res importbus.Result, want ...string) {
	t.Helper()

	got := amounts(res)
	if len(got) != len(want) {
		t.Fatalf("%d records, want %d: %v (warnings %+v)", len(got), len(want), got, res.Warnings)
	}

	for i, w := range want {
		if got[i] != money.MustParse(w) {
			t.Errorf("record %d = %s, want %s", i, got[i].Display(), w)
		}
	}
}

func TestSignedAmountColumn(t *testing.T) {
	const data = "Date,Description,Amount\n2026-07-01,PAYROLL,3450.00\n2026-07-14,CORNER GROCERY,-33.99\n"

	m := detect(t, data)
	if m.Date != "Date" || m.Description != "Description" || m.Amount != "Amount" || m.Separator != "," || m.DecimalComma {
		t.Fatalf("Detect = %+v", m)
	}

	res := read(t, data, m)
	wantAmounts(t, res, "3450.00", "-33.99")

	if r := res.Records[1]; r.Description != "CORNER GROCERY" || r.Date.Format("2006-01-02") != "2026-07-14" || r.Line != 3 {
		t.Errorf("record = %+v", r)
	}
}

func TestDebitCreditColumns(t *testing.T) {
	const data = "Posted Date,Payee,Debit,Credit\n07/01/2026,DEPOSIT,,100.00\n07/02/2026,RENT,-900.00,\n07/03/2026,FEE,5,\n"

	m := detect(t, data)
	if m.Debit != "Debit" || m.Credit != "Credit" || m.Amount != "" {
		t.Fatalf("Detect = %+v", m)
	}

	// A debit is money out however it was signed.
	wantAmounts(t, read(t, data, m), "100.00", "-900.00", "-5.00")
}

func TestInvertForACardExport(t *testing.T) {
	const data = "Date,Description,Amount\n2026-07-01,COFFEE,4.50\n2026-07-02,PAYMENT THANK YOU,-200.00\n"

	m := detect(t, data)
	m.Invert = true

	wantAmounts(t, read(t, data, m), "-4.50", "200.00")
}

// A Brazilian export: semicolons, a decimal comma, R$, D and C, and Latin-1.
func TestABrazilianExport(t *testing.T) {
	data := []byte("Data;Hist\xf3rico;Valor;Saldo\n01/07/2026;Dep\xf3sito;R$ 1.500,00 C;1.500,00\n15/07/2026;Mercado;R$ 33,99 D;1.466,01\n")

	in, err := csvsource.Inspect(data, 0)
	if err != nil {
		t.Fatal(err)
	}

	m := in.Detected
	if m.Separator != ";" || !m.DecimalComma || m.Date != "Data" || m.Description != "Histórico" || m.Amount != "Valor" || m.Balance != "Saldo" {
		t.Fatalf("Detect = %+v", m)
	}

	res, err := csvsource.Read(data, m)
	if err != nil {
		t.Fatal(err)
	}

	wantAmounts(t, res, "1500.00", "-33.99")

	r := res.Records[1]
	if !r.HasBalance || r.Balance != money.MustParse("1466.01") || r.Date.Format("2006-01-02") != "2026-07-15" {
		t.Errorf("record = %+v", r)
	}

	if res.Records[0].Description != "Depósito" {
		t.Errorf("Latin-1 was read as %q", res.Records[0].Description)
	}
}

// Day-first is chosen only when some date cannot be month-first, and then
// for the whole file -- including the dates that could have been either.
func TestTheDateFormatIsChosenForTheWholeFile(t *testing.T) {
	const data = "Fecha,Concepto,Importe\n03/04/2026,A,-1.00\n25/04/2026,B,-2.00\n"

	res := read(t, data, detect(t, data))

	if got := res.Records[0].Date.Format("2006-01-02"); got != "2026-04-03" {
		t.Errorf("03/04/2026 in a day-first file read as %s", got)
	}

	// Month-first, when every date allows it.
	const us = "Date,Description,Amount\n03/04/2026,A,-1.00\n12/04/2026,B,-2.00\n"
	if got := read(t, us, detect(t, us)).Records[0].Date.Format("2006-01-02"); got != "2026-03-04" {
		t.Errorf("03/04/2026 in a month-first file read as %s", got)
	}

	// And a person's choice wins.
	m := detect(t, us)
	m.DateFormat = "02/01/2006"

	if got := read(t, us, m).Records[0].Date.Format("2006-01-02"); got != "2026-04-03" {
		t.Errorf("the chosen format was ignored: %s", got)
	}
}

func TestParseAmount(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"1,234.56", ""}:    "1234.56",
		{"1.234,56", "c"}:   "1234.56",
		{"-12,5", "c"}:      "-12.50",
		{"R$ 10,00 D", "c"}: "-10.00",
		{"10,00 C", "c"}:    "10.00",
		{"12.50-", ""}:      "-12.50",
		{"(45.00)", ""}:     "-45.00",
		{"US$ 7.00", ""}:    "7.00",
		{"100.00 USD", ""}:  "100.00",
	} {
		got, err := csvsource.ParseAmount(in[0], in[1] == "c")
		if err != nil || got != money.MustParse(want) {
			t.Errorf("ParseAmount(%q, comma=%v) = %s, %v; want %s", in[0], in[1] == "c", got.Display(), err, want)
		}
	}

	if _, err := csvsource.ParseAmount("twelve", false); err == nil {
		t.Error("a word was read as an amount")
	}
}

// A row that cannot be read is counted and described, never dropped quietly,
// and the rest of the file still imports.
func TestBadRowsAreCountedNotFatal(t *testing.T) {
	const data = "Date,Description,Amount\n2026-07-01,OK,1.00\n,NO DATE,2.00\nyesterday,BAD DATE,3.00\n2026-07-04,BAD AMOUNT,lots\n2026-07-05,NO AMOUNT,\n\n2026-07-06,OK TOO,4.00\n"

	res := read(t, data, detect(t, data))
	wantAmounts(t, res, "1.00", "4.00")

	if res.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4 (a blank line is not a skipped row)", res.Skipped)
	}

	want := []importbus.Warning{
		{Line: 3, Problem: importbus.NoDate},
		{Line: 4, Problem: importbus.BadDate, Value: "yesterday"},
		{Line: 5, Problem: importbus.BadAmount, Value: "lots"},
		{Line: 6, Problem: importbus.NoAmount},
	}

	if len(res.Warnings) != len(want) {
		t.Fatalf("warnings = %+v", res.Warnings)
	}

	for i, w := range want {
		if res.Warnings[i] != w {
			t.Errorf("warning %d = %+v, want %+v", i, res.Warnings[i], w)
		}
	}
}

func TestWarningsAreCapped(t *testing.T) {
	data := "Date,Description,Amount\n"
	for range 50 {
		data += "nope,X,1.00\n"
	}

	res := read(t, data, csvsource.Mapping{Date: "Date", Description: "Description", Amount: "Amount"})
	if res.Skipped != 50 || len(res.Warnings) != 20 {
		t.Errorf("Skipped %d, %d warnings", res.Skipped, len(res.Warnings))
	}
}

func TestBOMAndRaggedRows(t *testing.T) {
	const data = "\uFEFFDate,Description,Amount\n2026-07-01,SHORT\n2026-07-02,LONG,1.00,extra,junk\n"

	m := detect(t, data)
	if m.Date != "Date" {
		t.Fatalf("the byte-order mark spoiled the first column: %+v", m)
	}

	res := read(t, data, m)
	wantAmounts(t, res, "1.00")

	if res.Skipped != 1 {
		t.Errorf("Skipped = %d", res.Skipped)
	}
}

func TestDetectPrefersExactMatches(t *testing.T) {
	m := csvsource.Detect([]string{"Transaction", "Transaction Date", "Amount"})
	if m.Date != "Transaction Date" || m.Description != "Transaction" {
		t.Errorf("Detect = %+v", m)
	}
}

// A card's export for several cards says whose each charge was; "Name" is
// a payee as often as a person, and is never taken for one.
func TestTheHolderColumn(t *testing.T) {
	m := csvsource.Detect([]string{"Date", "Description", "Card Member", "Amount"})
	if m.Holder != "Card Member" {
		t.Errorf("Detect = %+v", m)
	}

	if m := csvsource.Detect([]string{"Date", "Name", "Amount"}); m.Holder != "" || m.Description != "Name" {
		t.Errorf("Detect = %+v", m)
	}

	res := read(t, "Date,Description,Card Member,Amount\n2026-09-04,CITY GARAGE,  Ana   Lima ,-12.00\n", m)
	if res.Records[0].Holder != "Ana Lima" {
		t.Errorf("holder = %q", res.Records[0].Holder)
	}
}

// A status column marks the charges that have not posted.
func TestAStatusColumn(t *testing.T) {
	const data = "Date,Description,Amount,Status\n2026-09-29,CITY PARKING HOLD,-7.25,Pending\n2026-09-16,CORNER BAKERY,-12.00,Posted\n"

	m := csvsource.Detect([]string{"Date", "Description", "Amount", "Status"})
	if m.Status != "Status" {
		t.Fatalf("Detect = %+v", m)
	}

	res := read(t, data, m)
	if !res.Records[0].Pending || res.Records[1].Pending {
		t.Errorf("records: %+v", res.Records)
	}
}

// A column of check numbers is found by its heading, and read as the
// numbers they are: padding gone, and a slip's reference no number at all.
func TestACheckNumberColumn(t *testing.T) {
	const data = "Details,Posting Date,Description,Amount,Check or Slip #\n" +
		"CHECK,07/02/2026,CHECK,-120.00,0001176\n" +
		"DSLIP,07/03/2026,DEPOSIT,300.00,DS-41\n" +
		"DEBIT,07/04/2026,CORNER BAKERY,-12.00,\n"

	m := csvsource.Detect([]string{"Details", "Posting Date", "Description", "Amount", "Check or Slip #"})
	if m.Check != "Check or Slip #" {
		t.Fatalf("Detect = %+v", m)
	}

	res := read(t, data, m)

	var got []string
	for _, r := range res.Records {
		got = append(got, r.CheckNumber)
	}

	if strings.Join(got, "|") != "1176||" {
		t.Errorf("check numbers %q", got)
	}

	if m := csvsource.Detect([]string{"Date", "Description", "Amount", "Account number"}); m.Check != "" {
		t.Errorf("an account's number taken for a check's: %+v", m)
	}
}

func TestSkipLines(t *testing.T) {
	const data = "Account activity for July\n\nDate,Description,Amount\n2026-07-01,A,1.00\n"

	in, err := csvsource.Inspect([]byte(data), 2)
	if err != nil || in.Detected.Date != "Date" || in.Detected.SkipLines != 2 {
		t.Fatalf("Inspect = %+v, %v", in.Detected, err)
	}

	res := read(t, data, in.Detected)
	wantAmounts(t, res, "1.00")

	if res.Records[0].Line != 4 {
		t.Errorf("line = %d; it should count the skipped lines", res.Records[0].Line)
	}
}

func TestAMappingThatNamesNoDateIsRefused(t *testing.T) {
	_, err := csvsource.Read([]byte("When,What,How much\n1,2,3\n"), csvsource.Mapping{Date: "Date", Amount: "How much"})
	if !errors.Is(err, csvsource.ErrNoHeader) {
		t.Errorf("err = %v", err)
	}

	_, err = csvsource.Read([]byte("Date,What\n2026-01-01,x\n"), csvsource.Mapping{Date: "Date"})
	if !errors.Is(err, csvsource.ErrNoHeader) {
		t.Errorf("no amount column: %v", err)
	}
}

func TestFingerprintNormalizes(t *testing.T) {
	a := csvsource.Fingerprint([]string{" Date ", "DESCRIPTION", "Amount", ""})
	b := csvsource.Fingerprint([]string{"date", "description", "  amount"})

	if a != b || a != "date,description,amount" {
		t.Errorf("%q and %q", a, b)
	}
}

func TestCSVSuppliesNoExternalIDs(t *testing.T) {
	const data = "Date,Description,Amount\n2026-07-01,A,1.00\n"

	for _, r := range read(t, data, detect(t, data)).Records {
		if r.ExternalID != "" {
			t.Errorf("a CSV row was given an identifier %q; the ledger hashes content instead", r.ExternalID)
		}
	}
}
