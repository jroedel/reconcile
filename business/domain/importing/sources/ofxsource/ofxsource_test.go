package ofxsource_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/ofxsource"
	"github.com/jroedel/reconcile/business/types/money"
)

// sgml is the OFX 1.x dialect: a key/value header block, then SGML with
// unclosed leaf tags. This is what nearly every .qfx download looks like.
//
// The account and routing numbers below are invented.
const sgml = `OFXHEADER:100
DATA:OFXSGML
VERSION:102
SECURITY:NONE
ENCODING:USASCII
CHARSET:1252
COMPRESSION:NONE
OLDFILEUID:NONE
NEWFILEUID:NONE

<OFX>
<SIGNONMSGSRSV1><SONRS><STATUS><CODE>0<SEVERITY>INFO</STATUS>
<DTSERVER>20260813120000[-5:EST]<LANGUAGE>ENG</SONRS></SIGNONMSGSRSV1>
<BANKMSGSRSV1><STMTTRNRS><TRNUID>1<STATUS><CODE>0<SEVERITY>INFO</STATUS>
<STMTRS>
<CURDEF>USD
<BANKACCTFROM><BANKID>000000000<ACCTID>000123456789<ACCTTYPE>CHECKING</BANKACCTFROM>
<BANKTRANLIST>
<DTSTART>20260701<DTEND>20260731
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20260714120000.000[-5:EST]
<TRNAMT>-33.99
<FITID>202607140001
<NAME>CORNER GROCERY
<MEMO>CORNER GROCERY STORE #1182
</STMTTRN>
<STMTTRN>
<TRNTYPE>CREDIT
<DTPOSTED>20260701
<TRNAMT>3450.00
<FITID>202607010007
<NAME>PAYROLL DEPOSIT ACME LTD
</STMTTRN>
<STMTTRN>
<TRNTYPE>CHECK
<DTPOSTED>20260722
<TRNAMT>-125.00
<FITID>202607220003
<NAME>CHECK
<CHECKNUM>1042
</STMTTRN>
</BANKTRANLIST>
<LEDGERBAL><BALAMT>4821.55<DTASOF>20260731120000</LEDGERBAL>
</STMTRS>
</STMTTRNRS></BANKMSGSRSV1>
</OFX>`

// xml is the OFX 2.x dialect: well-formed XML, every tag closed.
const xml = `<?xml version="1.0" encoding="UTF-8"?>
<?OFX OFXHEADER="200" VERSION="211" SECURITY="NONE" OLDFILEUID="NONE" NEWFILEUID="NONE"?>
<OFX>
  <CREDITCARDMSGSRSV1>
    <CCSTMTTRNRS>
      <CCSTMTRS>
        <CURDEF>USD</CURDEF>
        <CCACCTFROM><ACCTID>XXXXXXXXXXXX4242</ACCTID></CCACCTFROM>
        <BANKTRANLIST>
          <STMTTRN>
            <TRNTYPE>DEBIT</TRNTYPE>
            <DTPOSTED>20260709000000</DTPOSTED>
            <TRNAMT>-64.75</TRNAMT>
            <FITID>CC-2026-07-09-0001</FITID>
            <NAME>AMZN Mktp US*2K4LM9XY3</NAME>
          </STMTTRN>
        </BANKTRANLIST>
      </CCSTMTRS>
    </CCSTMTTRNRS>
  </CREDITCARDMSGSRSV1>
</OFX>`

func read(t *testing.T, body string) []struct {
	Date, Description, Amount, ExternalID string
} {
	t.Helper()

	res, err := ofxsource.Read([]byte(body))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	out := make([]struct{ Date, Description, Amount, ExternalID string }, 0, len(res.Records))
	for _, r := range res.Records {
		out = append(out, struct{ Date, Description, Amount, ExternalID string }{
			Date:        r.Date.Format("2006-01-02"),
			Description: r.Description,
			Amount:      r.Amount.String(),
			ExternalID:  r.ExternalID,
		})
	}

	return out
}

func TestSGMLDialect(t *testing.T) {
	got := read(t, sgml)

	if len(got) != 3 {
		t.Fatalf("got %d records, want 3: %+v", len(got), got)
	}

	if got[0].Date != "2026-07-14" {
		t.Errorf("date = %q, want 2026-07-14", got[0].Date)
	}

	if got[0].Amount != "-33.99" {
		t.Errorf("amount = %q, want -33.99", got[0].Amount)
	}

	if got[1].Amount != "3450.00" {
		t.Errorf("credit amount = %q, want 3450.00", got[1].Amount)
	}
}

func TestXMLDialect(t *testing.T) {
	got := read(t, xml)

	if len(got) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(got), got)
	}

	if got[0].Date != "2026-07-09" || got[0].Amount != "-64.75" {
		t.Errorf("got %+v", got[0])
	}

	if got[0].Description != "AMZN Mktp US*2K4LM9XY3" {
		t.Errorf("description = %q", got[0].Description)
	}
}

// The whole reason to prefer OFX: the bank's own transaction id survives into
// the ledger, so dedupe does not have to guess from content.
func TestFITIDBecomesExternalID(t *testing.T) {
	got := read(t, sgml)

	want := []string{"202607140001", "202607010007", "202607220003"}
	for i, w := range want {
		if got[i].ExternalID != w {
			t.Errorf("record %d external id = %q, want %q", i, got[i].ExternalID, w)
		}
	}
}

// A timezone suffix must not shift a transaction into the previous day, which
// at a month boundary would move it into the wrong report.
func TestTimezoneSuffixDoesNotShiftTheDate(t *testing.T) {
	body := strings.Replace(sgml,
		"<DTPOSTED>20260714120000.000[-5:EST]",
		"<DTPOSTED>20260701000000.000[-11:MIT]", 1)

	got := read(t, body)

	if got[0].Date != "2026-07-01" {
		t.Errorf("date = %q, want 2026-07-01 — a timezone must not move the day", got[0].Date)
	}
}

// OFX caps NAME at 32 characters and banks spill the rest into MEMO. Joining
// them must not double a payee when MEMO merely repeats or extends NAME.
func TestDescriptionJoinsNameAndMemoSensibly(t *testing.T) {
	got := read(t, sgml)

	// MEMO extends NAME here, so the longer form wins outright.
	if got[0].Description != "CORNER GROCERY STORE #1182" {
		t.Errorf("description = %q, want the untruncated MEMO form", got[0].Description)
	}

	// A check number is worth keeping: "CHECK" alone identifies nothing.
	if !strings.Contains(got[2].Description, "1042") {
		t.Errorf("check description = %q, want the check number in it", got[2].Description)
	}
}

func TestDuplicateMemoIsNotAppended(t *testing.T) {
	body := strings.Replace(sgml,
		"<MEMO>CORNER GROCERY STORE #1182", "<MEMO>CORNER GROCERY", 1)

	got := read(t, body)

	if got[0].Description != "CORNER GROCERY" {
		t.Errorf("description = %q, want the payee once", got[0].Description)
	}
}

// A transaction with no FITID is kept -- losing it would be worse -- and
// said, because it will be deduplicated by its content instead.
func TestMissingFITIDIsKeptAndSaid(t *testing.T) {
	body := strings.Replace(sgml, "<FITID>202607140001\n", "", 1)

	res, err := ofxsource.Read([]byte(body))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if res.Skipped != 0 || len(res.Records) != 3 || res.Records[0].ExternalID != "" {
		t.Errorf("Skipped %d, %d records", res.Skipped, len(res.Records))
	}

	if len(res.Warnings) != 1 || res.Warnings[0].Problem != importbus.NoFITID {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestNotOFXIsAnError(t *testing.T) {
	for _, body := range []string{"", "Date,Description,Amount\n2026-07-01,X,1.00\n", "<html><body>hi</body></html>"} {
		if _, err := ofxsource.Read([]byte(body)); !errors.Is(err, ofxsource.ErrNotOFX) {
			t.Errorf("expected an error for %q", body)
		}
	}
}

// The institution's real account number is in the file and must not end up in
// the record — this project keeps those out of its database on purpose.
func TestBankAccountNumberDoesNotLeakIntoRecords(t *testing.T) {
	res, err := ofxsource.Read([]byte(sgml))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range res.Records {
		if strings.Contains(r.Description, "000123456789") {
			t.Error("the bank account number reached a transaction description")
		}

		if strings.Contains(r.ExternalID, "000123456789") {
			t.Error("the bank account number reached an external id")
		}
	}
}

func TestLooks(t *testing.T) {
	if !ofxsource.Looks([]byte(sgml[:80])) {
		t.Error("SGML header not recognized")
	}

	if !ofxsource.Looks([]byte(xml[:200])) {
		t.Error("XML dialect not recognized")
	}

	if ofxsource.Looks([]byte("Date,Description,Amount\n")) {
		t.Error("a CSV header was taken for OFX")
	}
}

func TestAmountsAreExact(t *testing.T) {
	got := read(t, sgml)

	if got[0].Amount != money.MustParse("-33.99").String() {
		t.Errorf("amount round trip = %q", got[0].Amount)
	}
}

func TestReadSurfacesThePeriod(t *testing.T) {
	got, err := ofxsource.Read([]byte(sgml))
	if err != nil {
		t.Fatal(err)
	}

	if got.Start.Format("2006-01-02") != "2026-07-01" || got.End.Format("2006-01-02") != "2026-07-31" {
		t.Errorf("period %s to %s", got.Start, got.End)
	}
}

func TestReadSurfacesTheLedgerBalance(t *testing.T) {
	// The reason this matters: a transaction feed says what MOVED, and a
	// snapshot needs what IS. Everything else in this ledger infers a balance
	// from rows that happen to have been imported; this is the institution's
	// own figure at a moment it names.
	got, err := ofxsource.Read([]byte(sgml))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if !got.Closing.Known {
		t.Fatal("the file states a ledger balance and Read reported none")
	}

	if want := money.MustParse("4821.55"); got.Closing.Amount != want {
		t.Errorf("balance = %s, want %s", got.Closing.Amount.Display(), want.Display())
	}

	if want := "2026-07-31"; got.Closing.AsOf.Format("2006-01-02") != want {
		t.Errorf("as of %s, want %s", got.Closing.AsOf.Format("2006-01-02"), want)
	}
}

// availableAfterLedger is the SGML dialect with no closing tag on the balance
// block, an available balance following the ledger one, and the two disagreeing
// — which is exactly how a credit card export looks.
const availableAfterLedger = `OFXHEADER:100
DATA:OFXSGML
VERSION:102

<OFX>
<CREDITCARDMSGSRSV1><CCSTMTTRNRS><TRNUID>1
<CCSTMTRS>
<CURDEF>USD
<CCACCTFROM><ACCTID>000000004321</CCACCTFROM>
<BANKTRANLIST>
<DTSTART>20260731<DTEND>20260817
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20260810
<TRNAMT>-10.42
<FITID>202608100001
<NAME>CORNER GROCERY
</STMTTRN>
</BANKTRANLIST>
<LEDGERBAL>
<BALAMT>-38.33
<DTASOF>20260818223650.471
<AVAILBAL>
<BALAMT>3961.68
<DTASOF>20260818223650.471
</CCSTMTRS></CCSTMTTRNRS></CREDITCARDMSGSRSV1>
</OFX>
`

func TestReadDoesNotMistakeAvailableCreditForABalance(t *testing.T) {
	// SGML OFX closes nothing, so a balance block stays "open" until something
	// else is entered. Left alone, the AVAILABLE balance overwrites the ledger
	// one — and on a credit card that is not a small error: it reports nearly
	// four thousand dollars of unused credit as four thousand dollars of
	// assets, with the sign wrong as well as the figure.
	got, err := ofxsource.Read([]byte(availableAfterLedger))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if !got.Closing.Known {
		t.Fatal("the file states a ledger balance and Read reported none")
	}

	if want := money.MustParse("-38.33"); got.Closing.Amount != want {
		t.Fatalf("balance = %s, want %s — available credit was read as the balance",
			got.Closing.Amount.Display(), want.Display())
	}
}

func TestReadReportsNoBalanceWhenTheFileStatesNone(t *testing.T) {
	// "The file said zero" and "the file said nothing" are different facts, and
	// a bare amount would let a zero stand in for both — quietly writing $0.00
	// over an account that simply was not reported on.
	withoutBalance := strings.Replace(sgml,
		"<LEDGERBAL><BALAMT>4821.55<DTASOF>20260731120000</LEDGERBAL>", "", 1)

	got, err := ofxsource.Read([]byte(withoutBalance))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if got.Closing.Known {
		t.Errorf("Read reported a balance of %s from a file that states none",
			got.Closing.Amount.Display())
	}
}

func TestReadRefusesABalanceWithNoDate(t *testing.T) {
	// A snapshot records what an account held at a moment. A figure that cannot
	// say which moment cannot be checked against anything later, so it is
	// reported as a warning rather than recorded as fact.
	undated := strings.Replace(sgml,
		"<LEDGERBAL><BALAMT>4821.55<DTASOF>20260731120000</LEDGERBAL>",
		"<LEDGERBAL><BALAMT>4821.55</LEDGERBAL>", 1)

	got, err := ofxsource.Read([]byte(undated))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if got.Closing.Known {
		t.Error("an undated balance was recorded as fact")
	}

	if len(got.Warnings) == 0 {
		t.Error("an undated balance was dropped without a word")
	}
}
