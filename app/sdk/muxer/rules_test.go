package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// august follows july: a coffee and the electric bill, both of which have
// rules by the time it arrives.
const august = "Date,Description,Amount,Balance\n" +
	"2026-08-02,COFFEE CART,-3.50,1085.51\n" +
	"2026-08-09,ELECTRIC CO,-118.00,967.51\n"

var ruleForm = regexp.MustCompile(`action="(/rules/[0-9a-f]{32})"`)

// The "always" box makes a rule from a transaction; the rules page sorts
// what is waiting, makes another, and the next statement arrives sorted
// and marked; a person's saving takes the mark away.
func TestSortingRules(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)

	coffee := txs["COFFEE CART"]
	page := e.owner.get(coffee).Body.String()
	choices := options(page)

	if !strings.Contains(page, "Always sort charges like this the same way") || !strings.Contains(page, `value="COFFEE CART"`) {
		t.Fatal("the transaction page offers no rule")
	}

	wantRedirect(t, e.owner.post(coffee, url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Groceries"]}, "project-0": {""}, "memo-0": {""},
		"always": {"1"}, "match": {"coffee cart"},
	}), e.account+"/transactions?month=2026-07&done=sorted-rule")

	// Too short a text: the parts are saved, the rule is not.
	grocery := txs["CORNER GROCERY"]
	rec := e.owner.post(grocery, url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Groceries"]}, "project-0": {""}, "memo-0": {""},
		"always": {"1"}, "match": {"CO"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a rule of two letters: %d", rec.Code)
	}

	wantBody(t, rec, "The parts are saved, but not the rule")

	// The other coffee is waiting for it.
	rules := e.account + "/rules"
	wantBody(t, e.owner.get(rules), "“coffee cart”", "Money out", "Groceries", "1 transactions not sorted yet meet a rule")
	wantRedirect(t, e.owner.post(rules+"/apply", nil), rules+"?done=applied&n=1")
	wantBody(t, e.owner.get(rules+"?done=applied&n=1"), "Sorted 1 transactions.")

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-07"), "1 sorted by rule, to check", `href="`+rules+`"`)

	if body := e.owner.get(e.account + "/transactions?month=2026-07&byrule=1").Body.String(); strings.Count(body, "by rule</span>") != 1 || strings.Contains(body, "ELECTRIC") {
		t.Errorf("the by-rule list:\n%s", body)
	}

	// One from the rules page, and one that will not do.
	rec = e.owner.post(rules, url.Values{"match": {"ab"}, "direction": {"out"}, "category": {choices["Utilities"]}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a rule of two letters: %d", rec.Code)
	}

	wantBody(t, rec, "Give the text to look for", `value="ab"`)
	wantRedirect(t, e.owner.post(rules, url.Values{"match": {"Electric"}, "direction": {"out"}, "category": {choices["Utilities"]}}), rules+"?done=made")

	// August arrives sorted, and says so.
	preview := uploaded(t, e.owner, e.account, "checking-august.csv", august)
	wantBody(t, e.owner.get(preview), "The account&#39;s sorting rules will sort 2 of the new ones")
	e.owner.post(preview, columns("import"))

	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-08"), "2 sorted by rule, to check", "Every transaction this month is sorted")
	wantBody(t, e.owner.get(e.account), "2 of them sorted by rule", "made a sorting rule for “Electric”")

	aug := map[string]string{}
	for _, m := range transactionLink.FindAllStringSubmatch(e.owner.get(e.account+"/transactions?month=2026-08").Body.String(), -1) {
		aug[m[2]] = m[1]
	}

	wantBody(t, e.owner.get(aug["ELECTRIC CO"]), "sorted by rule", "A sorting rule chose this")
	wantRedirect(t, e.owner.post(aug["ELECTRIC CO"], url.Values{
		"action": {"save"}, "amount-0": {""}, "category-0": {choices["Utilities"]}, "project-0": {""}, "memo-0": {""}, "match": {"ELECTRIC CO"},
	}), e.account+"/transactions?month=2026-08&done=sorted")
	wantBody(t, e.owner.get(e.account+"/transactions?month=2026-08"), "1 sorted by rule, to check")

	// Change one, then remove it.
	forms := ruleForm.FindAllStringSubmatch(e.owner.get(rules).Body.String(), -1)
	if len(forms) < 2 {
		t.Fatalf("%d rule forms", len(forms))
	}

	rule := forms[0][1] // "coffee cart" sorts before "Electric"
	wantRedirect(t, e.owner.post(rule, url.Values{"match": {"COFFEE"}, "direction": {"any"}, "category": {choices["Utilities"]}}), rules+"?done=changed")
	wantRedirect(t, e.owner.post(rule, url.Values{"match": {"CO"}, "direction": {"any"}, "category": {choices["Utilities"]}}), rules+"?problem=match")

	// Who: a stranger finds nothing, a viewer is refused.
	stranger := signUpAs("stranger@example.org")
	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")
	viewer := signUpAs("viewer@example.org")

	for name, c := range map[string]struct {
		b    *browser
		want int
	}{"a stranger": {stranger, http.StatusNotFound}, "a viewer": {viewer, http.StatusForbidden}} {
		if got := c.b.get(rules).Code; got != c.want {
			t.Errorf("%s: GET %s = %d", name, rules, got)
		}

		for path, form := range map[string]url.Values{
			rules:            {"match": {"mine"}, "direction": {"out"}, "category": {choices["Utilities"]}},
			rules + "/apply": nil,
			rule:             {"match": {"mine"}, "direction": {"out"}, "category": {choices["Utilities"]}},
			rule + "/remove": nil,
		} {
			if got := c.b.post(path, form).Code; got != c.want {
				t.Errorf("%s: POST %s = %d", name, path, got)
			}
		}

		if strings.Contains(c.b.get(e.account+"/transactions").Body.String(), `href="`+rules+`"`) {
			t.Errorf("%s is offered the rules page", name)
		}
	}

	wantRedirect(t, e.owner.post(rule+"/remove", nil), rules+"?done=removed")
	wantBody(t, e.owner.get(e.account), "changed the sorting rule for “COFFEE”", "removed the sorting rule for “COFFEE”")

	if strings.Contains(e.owner.get(rules).Body.String(), "“COFFEE”") {
		t.Error("the removed rule is still listed")
	}
}
