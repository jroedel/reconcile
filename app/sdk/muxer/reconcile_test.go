package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var statementLink = regexp.MustCompile(`href="(/statements/[0-9a-f]{32})"`)

// julyLate is one more July row after the paper statement was reconciled:
// a fee the bank posted late.
const julyLate = "Date,Description,Amount,Balance\n" +
	"2026-07-28,ELECTRIC CO,-120.00,1089.01\n" +
	"2026-07-30,BANK FEE,-5.00,1084.01\n"

// The end of the month, as the treasurer does it: the month-by-month page
// says July is short three days, the statement page says what is unsorted,
// the period is widened to the paper's and marked, and July holds still
// until it is reopened with a reason.
func TestReconcilingAMonth(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)

	m := statementLink.FindStringSubmatch(e.owner.get(e.account + "/transactions").Body.String())
	if m == nil {
		t.Fatal("no statement on the account's transactions")
	}

	statement := m[1]

	wantBody(t, e.owner.get(e.account), `href="`+e.account+`/months"`)
	wantBody(t, e.owner.get(e.account+"/months"), "2026-07", "Some days missing", "Not in any statement:", "2026-07-29 to 2026-07-31", "6 not sorted yet")

	page := e.owner.get(statement).Body.String()
	for _, want := range []string{"Mark reconciled", "6 of them are not sorted", "CORNER GROCERY", "$1,089.01", `value="2026-07-01"`, `value="2026-07-28"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the statement page lacks %q", want)
		}
	}

	// Narrower than the file is refused, and what was typed is kept.
	rec := e.owner.post(statement+"/reconcile", url.Values{"from": {"2026-07-02"}, "to": {"2026-07-31"}, "note": {"Agrees with the paper."}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a narrow period: %d", rec.Code)
	}

	wantBody(t, rec, "must include every day of this statement", `value="2026-07-02"`, "Agrees with the paper.")

	wantRedirect(t, e.owner.post(statement+"/reconcile", url.Values{"from": {"2026-07-01"}, "to": {"2026-07-31"}, "note": {"Agrees with the paper."}}),
		statement+"?done=reconciled")

	wantBody(t, e.owner.get(statement+"?done=reconciled"), "Reconciled. The transactions", "for 2026-07-01 to 2026-07-31", "Agrees with the paper.", "Why reopen it?")
	wantBody(t, e.owner.get(e.account+"/months"), "Reconciled")
	wantBody(t, e.owner.get(e.account), "reconciled checking-july.csv for 2026-07-01 to 2026-07-31")

	if strings.Contains(e.owner.get(statement).Body.String(), "/remove") {
		t.Error("a reconciled statement offers removal")
	}

	// The grocery holds still, and its page says why and where.
	grocery := txs["CORNER GROCERY"]
	wantBody(t, e.owner.get(grocery), "is in a reconciled period, 2026-07-01 to 2026-07-31", statement+"#reconcile")

	if strings.Contains(e.owner.get(grocery).Body.String(), `name="category-0"`) {
		t.Error("a locked transaction offers its parts")
	}

	if rec := e.owner.post(grocery, url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {""}}); rec.Code != http.StatusConflict {
		t.Errorf("sorting a locked transaction: %d", rec.Code)
	}

	// A late July row cannot slip in.
	preview := uploaded(t, e.owner, e.account, "late.csv", julyLate)
	wantBody(t, e.owner.get(preview), "1 of the new transactions fall in a period that has been reconciled")

	if rec := e.owner.post(preview, columns("import")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nothing was imported") {
		t.Errorf("importing into July: %d", rec.Code)
	}

	// A contributor can see how it stands and cannot reopen it.
	wantRedirect(t, e.owner.post(e.account+"/people", url.Values{"email": {"helper@example.org"}, "role": {"contributor"}}), e.account+"?done=granted")
	helper := signUpAs("helper@example.org")

	if body := helper.get(statement).Body.String(); !strings.Contains(body, "Reconciled") || strings.Contains(body, "Why reopen it?") {
		t.Error("the contributor's view of a reconciled statement")
	}

	if rec := helper.post(statement+"/reopen", url.Values{"reason": {"I want to"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a contributor reopens: %d", rec.Code)
	}

	// Reopening asks why, and keeps the answer.
	if rec := e.owner.post(statement+"/reopen", url.Values{"reason": {" "}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("reopening without a reason: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(statement+"/reopen", url.Values{"reason": {"The bank posted a late fee."}}), statement+"?done=reopened")
	wantBody(t, e.owner.get(e.account), "because: The bank posted a late fee.")
	wantBody(t, e.owner.get(grocery), `name="category-0"`)

	imported(t, e.owner, e.account, "late.csv", julyLate)
}
