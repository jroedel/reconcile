package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var questionPath = regexp.MustCompile(`^(/questions/[0-9a-f]+)\?done=asked$`)

// asked asks a question about a thing as b, from the form its page has,
// and returns the question's address.
func asked(t *testing.T, b *browser, kind, id, body string) string {
	t.Helper()

	rec := b.post("/questions", url.Values{"kind": {kind}, "id": {id}, "body": {body}})

	m := questionPath.FindStringSubmatch(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || m == nil {
		t.Fatalf("asking about a %s: %d to %q\n%s", kind, rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	return m[1]
}

// Questions on the site (docs/accountant.md, 4): asked from a thing's
// page, read on their own page with the link that opens it, answered by
// another's reply, listed on the accountant's page; read but not answered
// by a viewer; seen by a role on a project as the project's part only; and
// found by nobody else. Everything invented.
func TestQuestionsOnTheSite(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)
	coffee, grocery := txs["COFFEE CART"], txs["CORNER GROCERY"]
	coffeeID := strings.TrimPrefix(coffee, "/transactions/")

	// The card is on the page, folded, with nothing asked yet.
	wantBody(t, e.owner.get(coffee), `id="questions"`, "No questions about this yet.", "<summary>Ask a question about this</summary>",
		`name="kind" value="transaction"`, `name="id" value="`+coffeeID+`"`)

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"accountant@example.org"}, "role": {"accountant"}}), e.org+"?done=granted")
	accountant := signUpAs("accountant@example.org")

	question := asked(t, accountant, "transaction", coffeeID, "Whose coffee was this?\nIt is not on any receipt.")

	page := accountant.get(question + "?done=asked").Body.String()
	for _, want := range []string{
		"Question asked.", `<span class="badge warn">Open</span>`, "Back to COFFEE CART, 2026-07-03", `href="` + coffee + `"`,
		"Whose coffee was this?\nIt is not on any receipt.", "accountant", `value="https://reconcile.example.invalid` + question + `"`, "Mark answered", "Send reply",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the question, to its asker: no %q in\n%s", want, page)
		}
	}

	// The thing's page says a question waits, and lists it.
	wantBody(t, e.owner.get(coffee), "A question about this is waiting for its answer.", `href="`+question+`"`, "Whose coffee was this?</a>")

	// The owner's reply answers it, and the owner may not mark it.
	if rec := e.owner.post(question+"/answered", nil); rec.Code != http.StatusForbidden {
		t.Errorf("the owner marking the accountant's question: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(question+"/reply", url.Values{"body": {"Mine, on the way to the retreat."}}), question+"?done=replied")

	answered := accountant.get(question + "?done=replied").Body.String()
	for _, want := range []string{"Reply sent.", `<span class="badge ok">Answered</span>`, "Mine, on the way to the retreat.", "owner"} {
		if !strings.Contains(answered, want) {
			t.Errorf("the question, answered: no %q in\n%s", want, answered)
		}
	}

	if strings.Contains(answered, "Mark answered") {
		t.Error("an answered question offers to be marked answered")
	}

	// An empty reply is said, and the page kept.
	wantBody(t, e.owner.post(question+"/reply", url.Values{"body": {"   "}}), "Write the reply, in at most 2,000 characters.")

	// An empty question too.
	if rec := accountant.post("/questions", url.Values{"kind": {"transaction"}, "id": {coffeeID}, "body": {""}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Write the question") {
		t.Errorf("an empty question: %d\n%s", rec.Code, rec.Body)
	}

	// The accountant's page has the third tab.
	wantBody(t, e.owner.get(e.org+"/accountant"), "Questions (0 open)")
	wantBody(t, e.owner.get(e.org+"/accountant?context=questions"), "No open questions.")
	wantBody(t, e.owner.get(e.org+"/accountant?context=questions&state=answered"), `href="`+question+`"`, "Whose coffee was this?</a>", "About COFFEE CART, 2026-07-03, asked by")
	wantBody(t, e.owner.get(e.org+"/accountant?context=questions&state=answered&mine=1"), "None of your questions here has been answered yet.")

	// The statement, the project and the payee have the card too.
	statement := imported(t, e.owner, e.account, "checking-august.csv", "Date,Description,Amount,Balance\n2026-08-03,FARMERS MARKET,-12.00,1077.01\n")
	payee := addedPayee(t, e.owner, e.org+"/payees", "Hilltop Plumbing")

	for _, p := range []string{statement, e.project, payee} {
		wantBody(t, e.owner.get(p), `id="questions"`, "Ask a question about this")
	}

	// A viewer reads, and may not answer.
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	seen := viewer.get(question).Body.String()
	if !strings.Contains(seen, "Your role here lets you read questions, not ask or answer them.") || strings.Contains(seen, "Send reply") {
		t.Errorf("the question, to a viewer:\n%s", seen)
	}

	if strings.Contains(viewer.get(coffee).Body.String(), "Ask a question about this") {
		t.Error("a viewer is offered to ask")
	}

	if rec := viewer.post(question+"/reply", url.Values{"body": {"Me too."}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer replying: %d", rec.Code)
	}

	// Somebody given the project alone reads a question about the
	// groceries as the project's part, with no way to the rest.
	choices := options(e.owner.get(grocery).Body.String())
	wantRedirect(t, e.owner.post(grocery, url.Values{
		"action":   {"save"},
		"amount-0": {"20.00"}, "category-0": {choices["Groceries"]}, "project-0": {choices["World Youth Day"]}, "memo-0": {""},
		"amount-1": {"13.99"}, "category-1": {choices["Groceries"]}, "project-1": {""}, "memo-1": {""},
	}), e.account+"/transactions?month=2026-07&done=sorted")

	groceries := asked(t, e.owner, "transaction", strings.TrimPrefix(grocery, "/transactions/"), "Which part was the retreat's?")

	wantRedirect(t, e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"contributor"}}), e.project+"?done=granted")
	pilgrim := signUpAs("pilgrim@example.org")

	part := pilgrim.get(groceries).Body.String()
	if !strings.Contains(part, "-$20.00") || strings.Contains(part, "-$33.99") || strings.Contains(part, `href="`+grocery+`"`) ||
		!strings.Contains(part, "Only the part of this charge in your projects is shown: World Youth Day.") || !strings.Contains(part, "Send reply") {
		t.Errorf("the groceries' question, to the pilgrim:\n%s", part)
	}

	// Nobody else finds it; signed out, the link asks to sign in and
	// comes back to it.
	stranger := signUpAs("stranger@example.org")
	if rec := stranger.get(question); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger reading: %d", rec.Code)
	}

	if rec := newBrowser(t, e.owner.h).get(question); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "next=%2Fquestions%2F") {
		t.Errorf("signed out: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}
