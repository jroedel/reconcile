package muxer

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// A CSV with a column of check numbers: the preview finds the column and
// shows each check's number where its description does not say it, the
// import keeps it, and the month and the transaction show it.
func TestCheckNumbersFromACSV(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	const file = "Date,Description,Amount,Balance,Check Number\n" +
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
		"2026-07-02,CHECK,-120.00,880.00,0001176\n" +
		"2026-07-03,Check 1177,-30.00,850.00,1177\n"

	preview := uploaded(t, e.owner, e.account, "july.csv", file)
	wantBody(t, e.owner.get(preview), `<option value="Check Number" selected>`, "Check number 1176")

	form := columns("import")
	form.Set("check", "Check Number")

	rec := e.owner.post(preview, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d\n%s", rec.Code, rec.Body.String())
	}

	month := e.owner.get(e.account + "/transactions?month=2026-07").Body.String()

	// Once beside the bare "CHECK", and not beside "Check 1177", which
	// says it already.
	if strings.Count(month, "Check number 1176") != 1 || strings.Contains(month, "Check number 1177") {
		t.Errorf("the month shows the check numbers wrongly:\n%s", month)
	}

	link := regexp.MustCompile(`/transactions/[0-9a-f]+`).FindAllString(month, -1)

	var seen bool

	for _, l := range link {
		if body := e.owner.get(l).Body.String(); strings.Contains(body, "Check number 1177") {
			seen = true
		}
	}

	if !seen {
		t.Errorf("no transaction page shows check 1177's number")
	}

}
