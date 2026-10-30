package muxer

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// What is told the accountant leaks nothing (#151): not an account for
// reference into the list of what needs more context, on the page or in
// the weekly email; not the organization's lists to somebody given only a
// project or only an account, who finds no page and is sent no line about
// them; and not one organization's questions or lists to another's owner,
// whose own email speaks only of their own, and who finds nothing behind
// the first one's links. Everything invented.
func TestTheAccountantsWorkLeaksNothing(t *testing.T) {
	t.Parallel()

	var site *Config

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { site = c })
	run := Weekly(*site)
	e := newEstate(t, h, sent)
	orgID := strings.TrimPrefix(e.org, "/orgs/")

	// The national account sits in the organization for reference, with
	// its own July in it.
	national := made(t, e.owner, "/accounts", url.Values{"org": {orgID}, "name": {"National checking"}, "kind": {"checking"}})
	wantRedirect(t, e.owner.post(national+"/reference", url.Values{"reference": {"1"}}), national+"?done=reference-on")
	imported(t, e.owner, national, "national-july.csv", july)
	imported(t, e.owner, e.account, "checking-july.csv", july)

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Diocesan collection"}, "kind": {"passthrough"}}), e.org+"/categories?done=added")

	txs, theirs := transactionsOf(t, e.owner, e.account), transactionsOf(t, e.owner, national)
	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	passOn := func(b *browser, account, tx, category, project string) {
		t.Helper()
		wantRedirect(t, b.post(tx, url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {category}, "project-0": {project}, "memo-0": {""}}),
			account+"/transactions?month=2026-07&done=sorted")
	}

	people := map[string]*browser{"owner@example.org": e.owner}

	for _, g := range []struct{ scope, email, role string }{
		{e.org, "accountant@example.org", "accountant"},
		{e.project, "pilgrim@example.org", "contributor"},
		{e.account, "clerk@example.org", "bookkeeper"},
	} {
		wantRedirect(t, e.owner.post(g.scope+"/people", url.Values{"email": {g.email}, "role": {g.role}}), g.scope+"?done=granted")
		people[g.email] = signUp(t, h, sent, g.email)
	}

	// The stranger keeps another parish's books, with a pass-through of
	// their own to explain.
	stranger := signUp(t, h, sent, "stranger@example.org")
	people["stranger@example.org"] = stranger
	otherOrg := made(t, stranger, "/orgs", url.Values{"name": {"Holy Cross"}})
	otherAccount := made(t, stranger, "/accounts", url.Values{"org": {strings.TrimPrefix(otherOrg, "/orgs/")}, "name": {"Holy Cross checking"}, "kind": {"checking"}})
	imported(t, stranger, otherAccount, "checking-july.csv", july)
	wantRedirect(t, stranger.post(otherOrg+"/categories", url.Values{"name": {"Second collection"}, "kind": {"passthrough"}}), otherOrg+"/categories?done=added")
	otherTxs := transactionsOf(t, stranger, otherAccount)
	otherChoices := options(stranger.get(otherTxs["CORNER GROCERY"]).Body.String())

	// Everybody hears at this hour today, in UTC.
	now := time.Now().UTC().Truncate(time.Minute)
	for _, b := range people {
		wantRedirect(t, b.post("/account/weekly", url.Values{"enabled": {"1"}, "weekday": {strconv.Itoa(int(now.Weekday()))}, "hour": {strconv.Itoa(now.Hour())}, "tz": {"UTC"}}),
			"/account?done=weekly#weekly")
	}

	// The groceries go to the project in part, which the pilgrim reaches;
	// the coffee is passed on and asked about, which the pilgrim does not.
	passOn(e.owner, e.account, txs["COFFEE CART"], choices["Diocesan collection"], "")
	passOn(e.owner, e.account, txs["CORNER GROCERY"], choices["Diocesan collection"], choices["World Youth Day"])

	accountant := people["accountant@example.org"]
	coffee := asked(t, accountant, "transaction", strings.TrimPrefix(txs["COFFEE CART"], "/transactions/"), "Whose coffee was this?")
	groceries := asked(t, accountant, "transaction", strings.TrimPrefix(txs["CORNER GROCERY"], "/transactions/"), "Which part was the retreat's?")

	var emails []mail.Message
	send := func(at time.Time) map[string]string {
		t.Helper()

		before := len(sent.Sent)
		if err := run(t.Context(), at); err != nil {
			t.Fatal(err)
		}

		out := map[string]string{}
		for _, m := range sent.Sent[before:] {
			if strings.HasPrefix(m.Subject, "What is new this week") {
				out[m.To] = m.Text
				emails = append(emails, m)
			}
		}

		return out
	}

	first := send(now)

	// The week: the national account's offertory is passed on, which
	// would be to do anywhere else; so is the electricity, which is; and
	// the stranger passes on a grocery of their own.
	passOn(e.owner, national, theirs["PARISH OFFERTORY"], choices["Diocesan collection"], "")
	passOn(e.owner, e.account, txs["ELECTRIC CO"], choices["Diocesan collection"], "")
	passOn(stranger, otherAccount, otherTxs["CORNER GROCERY"], otherChoices["Second collection"], "")

	week := send(now.AddDate(0, 0, 7))

	// The account for reference is in no count: the coffee and the
	// electricity, not the offertory.
	wantText(t, week["owner@example.org"], "1 transaction newly needs more context (2 in all):\n"+base+e.org+"/accountant?context=needed\n")
	wantBody(t, e.owner.get(e.org+"/accountant"), `aria-current="page">More context needed (2)`)

	if page := e.owner.get(e.org + "/accountant?context=needed&account=" + strings.TrimPrefix(national, "/accounts/")).Body.String(); strings.Contains(page, ">PARISH OFFERTORY<") {
		t.Errorf("the national account's offertory is to do:\n%s", page)
	}

	// Given a project or an account and nothing of the organization: no
	// page of its lists, and no word of them or of a question about
	// something out of reach. The groceries' question is theirs to open.
	for _, who := range []string{"pilgrim@example.org", "clerk@example.org"} {
		if rec := people[who].get(e.org + "/accountant"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: the accountant's page = %d, want 404", who, rec.Code)
		}

		for _, text := range []string{first[who], week[who]} {
			if strings.Contains(text, "/accountant") || strings.Contains(text, "St. Joseph") {
				t.Errorf("%s hears of the organization:\n%s", who, text)
			}
		}
	}

	if rec := people["pilgrim@example.org"].get(coffee); rec.Code != http.StatusNotFound {
		t.Errorf("the pilgrim: the coffee's question = %d, want 404", rec.Code)
	}

	if rec := people["pilgrim@example.org"].get(groceries); rec.Code != http.StatusOK {
		t.Errorf("the pilgrim: the groceries' question = %d", rec.Code)
	}

	// The stranger hears of their own parish alone.
	if text := week["stranger@example.org"]; !strings.Contains(text, "Holy Cross") || strings.Contains(text, "St. Joseph") {
		t.Errorf("the stranger's email:\n%s", text)
	}

	// Every link in every email opens for whoever it was sent to, and for
	// nobody without a role on what it names. A stop link is the one that
	// needs no sign-in, and it opens a page that names nobody.
	for _, m := range emails {
		for _, link := range emailLink.FindAllString(m.Text, -1) {
			path, _, _ := strings.Cut(strings.TrimPrefix(link, base), "#")

			if strings.HasPrefix(path, "/digest/stop") {
				page := newBrowser(t, h).get(path).Body.String()
				if strings.Contains(page, m.To) || strings.Contains(page, "St. Joseph") || strings.Contains(page, "Holy Cross") {
					t.Errorf("the stop page of %s's email names them:\n%s", m.To, page)
				}

				continue
			}

			if rec := people[m.To].get(path); rec.Code != http.StatusOK {
				t.Errorf("%s cannot open %s: %d", m.To, link, rec.Code)
			}

			if path == "/account" {
				continue
			}

			for who, b := range people {
				outsider := m.To == "stranger@example.org" != (who == "stranger@example.org")
				if outsider {
					if rec := b.get(path); rec.Code != http.StatusNotFound {
						t.Errorf("%s opens %s, from %s's email: %d", who, link, m.To, rec.Code)
					}
				}
			}
		}
	}
}
