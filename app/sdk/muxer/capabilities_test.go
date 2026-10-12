package muxer

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// A card's month from its bank's OFX, which cuts every name at twelve
// characters and gives each row an identifier, and from its CSV, which
// has the whole names. The shops and amounts are invented.
var (
	decemberOFX = func() string {
		var b strings.Builder

		b.WriteString("OFXHEADER:100\nDATA:OFXSGML\nVERSION:102\n\n<OFX>\n<CREDITCARDMSGSRSV1><CCSTMTTRNRS><TRNUID>1\n<CCSTMTRS>\n<CURDEF>USD\n<BANKTRANLIST>\n<DTSTART>20251201<DTEND>20251231\n")

		for i, r := range [][2]string{{"20251202", "HARDWARE BAR"}, {"20251203", "COFFEE CART "}, {"20251204", "BOOK STALL #"}} {
			fmt.Fprintf(&b, "<STMTTRN>\n<TRNTYPE>DEBIT\n<DTPOSTED>%s\n<TRNAMT>-%d.00\n<FITID>DEC-%d\n<NAME>%s\n</STMTTRN>\n", r[0], i+1, i, r[1])
		}

		b.WriteString("</BANKTRANLIST>\n</CCSTMTRS></CCSTMTTRNRS></CREDITCARDMSGSRSV1>\n</OFX>\n")

		return b.String()
	}()

	decemberCSV = "Date,Description,Amount\n2025-12-02,HARDWARE BARN #12 SPRINGFIELD,-1.00\n2025-12-03,COFFEE CART 0902 SEATTLE,-2.00\n2025-12-04,BOOK STALL #4 PORTLAND,-3.00\n"
)

// What each file of a batch can do, and what the OFX and the CSV of the
// same days do together, on the imports page (issue #111).
func TestWhatTheFilesCanDo(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)
	card := strings.TrimPrefix(e.ownAccount, "/accounts/")

	rec := e.owner.uploadMany("/imports", [2]string{"december.ofx", decemberOFX}, [2]string{"december.csv", decemberCSV})
	list := rec.Header().Get("Location")

	q, _ := url.ParseQuery(strings.TrimPrefix(list, "/imports?"))
	if len(q["f"]) != 2 {
		t.Fatalf("upload: %d to %q", rec.Code, list)
	}

	// Neither names its account: both are chosen for the card.
	form := url.Values{"f": q["f"]}
	for _, f := range q["f"] {
		form.Set("to-"+f, card)
	}

	again := e.owner.post("/imports/check", form).Header().Get("Location")

	wantBody(t, e.owner.get(again),
		"✓ Bank&#39;s identifiers", "✗ Whole descriptions", "✗ Opening and closing balances", "✓ Whole descriptions",
		"A CSV has no identifiers from the bank",
		"Together: My card, 2025-12-01 to 2025-12-31",
		"The bank&#39;s identifiers come from december.ofx.",
		"The whole descriptions come from december.csv.",
		"None of these files has balances")

	// A file on its own page says the same, and says plainly what another
	// download would add: a CSV, the whole descriptions; the statement,
	// the balances.
	wantBody(t, e.owner.get(e.ownAccount+"/imports/"+q["f"][0]), "The bank cut the descriptions short",
		"What another download of the same days would add:",
		"A CSV download of the same days usually has the descriptions whole.",
		"The bank&#39;s monthly statement, as a PDF, states the opening and closing balances.")
}

// list_inbox carries each part's capabilities, and the batch's together.
func TestTheInboxSaysWhatItsFilesCanDo(t *testing.T) {
	t.Parallel()

	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Claude", "books-read")

	orgID := strings.TrimPrefix(e.org, "/orgs/")
	made(t, e.owner, "/accounts", url.Values{"org": {orgID}, "name": {"Checking A"}, "kind": {"checking"}, "last4": {pdfsourcetest.First}})

	if rec := s.send(keyFor(t, e.owner, "Gmail script", "upload"), "statements.pdf", string(pdfsourcetest.Consolidated())); rec.Code != http.StatusCreated {
		t.Fatalf("sending to the inbox: %d %s", rec.Code, rec.Body)
	}

	got := s.get(t, "/api/v1/inbox", key)
	if _, ok := got["together"].([]any); !ok {
		t.Errorf("no together: %v", got)
	}

	for _, part := range field(list(t, got, "files")[0], "parts").([]any) {
		if field(part, "account") == nil {
			continue
		}

		caps, _ := field(part, "capabilities").([]any)
		if len(caps) < 3 || field(caps[0], "name") != "ends" || field(caps[0], "can") != "yes" ||
			field(caps[2], "name") != "identifiers" || field(caps[2], "can") != "na" || field(caps[2], "better") != "ofx" {
			t.Errorf("the part's capabilities: %v", caps)
		}
	}
}
