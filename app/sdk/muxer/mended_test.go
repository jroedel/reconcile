package muxer

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// A card's month as its CSV and its OFX give it, the same three charges;
// the OFX gives each the bank's identifier. Invented shops and amounts.
var (
	mendCSV = "Date,Description,Amount\n2025-11-03,CORNER HARDWARE,-1.00\n2025-11-04,COFFEE CART,-2.00\n2025-11-05,BOOK STALL,-3.00\n"

	mendOFX = func() string {
		var b strings.Builder

		b.WriteString("OFXHEADER:100\nDATA:OFXSGML\nVERSION:102\n\n<OFX>\n<CREDITCARDMSGSRSV1><CCSTMTTRNRS><TRNUID>1\n<CCSTMTRS>\n<CURDEF>USD\n<BANKTRANLIST>\n<DTSTART>20251101<DTEND>20251130\n")

		for i, n := range []string{"CORNER HARDWARE", "COFFEE CART", "BOOK STALL"} {
			fmt.Fprintf(&b, "<STMTTRN>\n<TRNTYPE>DEBIT\n<DTPOSTED>202511%02d\n<TRNAMT>-%d.00\n<FITID>NOV-%d\n<NAME>%s\n</STMTTRN>\n", i+3, i+1, i, n)
		}

		b.WriteString("</BANKTRANLIST>\n</CCSTMTRS></CCSTMTTRNRS></CREDITCARDMSGSRSV1>\n</OFX>\n")

		return b.String()
	}()
)

// An import from the bulk list that gives the rows already here what its
// file knows of them says so: before, on the list, and after, on the list
// and on the statement's page, for good.
func TestAnImportSaysWhatItMended(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)
	card := strings.TrimPrefix(e.ownAccount, "/accounts/")

	imported(t, e.owner, e.ownAccount, "november.csv", mendCSV)

	list := e.owner.uploadMany("/imports", [2]string{"november.ofx", mendOFX}).Header().Get("Location")
	q, _ := url.ParseQuery(strings.TrimPrefix(list, "/imports?"))

	form := url.Values{"f": q["f"], "to-" + q["f"][0]: {card}}
	before := e.owner.post("/imports/check", form).Header().Get("Location")

	wantBody(t, e.owner.get(before), "3 of the transactions already here will take the bank&#39;s identifier from this file")

	done := e.owner.post("/imports/import", form).Header().Get("Location")
	page := e.owner.get(done)
	wantBody(t, page, "Imported: 1.", "3 transactions already here took the bank&#39;s identifier from this file")

	m := statementLink.FindStringSubmatch(page.Body.String())
	if m == nil {
		t.Fatal("no link to the statement")
	}

	wantBody(t, e.owner.get(m[1]), "3 transactions already here took the bank&#39;s identifier from this file")
}
