package muxer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

var emailLink = regexp.MustCompile(`https://\S+`)

// The weekly email, sent (docs/accountant.md, 5), with the clock moved by
// hand: at each person's own day and hour, in their own time zone; the
// first only looking; nothing when there is nothing, and nothing twice;
// each person's lines by their role, and their own doings left out; every
// link one they may open; and stopped by its link only once the button is
// pressed, or by a mail program's own button. Everything invented.
func TestSendingTheWeeklyEmail(t *testing.T) {
	t.Parallel()

	var site *Config

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { site = c })
	run := Weekly(*site)
	e := newEstate(t, h, sent)

	wantRedirect(t, e.owner.post(e.org+"/categories", url.Values{"name": {"Diocesan collection"}, "kind": {"passthrough"}}), e.org+"/categories?done=added")
	imported(t, e.owner, e.account, "checking-july.csv", july)
	txs := transactionsOf(t, e.owner, e.account)
	choices := options(e.owner.get(txs["CORNER GROCERY"]).Body.String())

	passOn := func(tx, project string) {
		t.Helper()
		wantRedirect(t, e.owner.post(tx, url.Values{"action": {"save"}, "amount-0": {""}, "category-0": {choices["Diocesan collection"]}, "project-0": {project}, "memo-0": {""}}),
			e.account+"/transactions?month=2026-07&done=sorted")
	}

	people := map[string]*browser{"owner@example.org": e.owner}

	for _, p := range []struct{ email, role string }{{"accountant@example.org", "accountant"}, {"viewer@example.org", "viewer"}} {
		wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {p.email}, "role": {p.role}}), e.org+"?done=granted")
		people[p.email] = signUp(t, h, sent, p.email)
	}

	// Each at this hour of today where they are: the owner in São Paulo,
	// the others in UTC.
	now := time.Now().Truncate(time.Minute)
	sp, _ := time.LoadLocation("America/Sao_Paulo")

	for _, b := range people {
		zone, loc := "UTC", time.UTC
		if b == e.owner {
			zone, loc = "America/Sao_Paulo", sp
		}

		local := now.In(loc)
		wantRedirect(t, b.post("/account/weekly", url.Values{"enabled": {"1"}, "weekday": {strconv.Itoa(int(local.Weekday()))}, "hour": {strconv.Itoa(local.Hour())}, "tz": {zone}}),
			"/account?done=weekly#weekly")
	}

	weeklies := func() []mail.Message {
		var out []mail.Message
		for _, m := range sent.Sent {
			if strings.HasPrefix(m.Subject, "What is new this week") {
				out = append(out, m)
			}
		}

		return out
	}

	send := func(at time.Time) []mail.Message {
		t.Helper()

		before := len(weeklies())
		if err := run(t.Context(), at); err != nil {
			t.Fatal(err)
		}

		return weeklies()[before:]
	}

	// Already to do before the first email, which only looks at it.
	passOn(txs["COFFEE CART"], "")

	coffeeID := strings.TrimPrefix(txs["COFFEE CART"], "/transactions/")
	question := asked(t, people["accountant@example.org"], "transaction", coffeeID, "Whose coffee was this?")

	if got := send(now.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("an hour before anybody's hour: %v", got)
	}

	// The first: the owner hears of the accountant's question, and of no
	// list; the accountant of nothing, since the question is their own;
	// the viewer of nothing.
	first := send(now)
	if len(first) != 1 || first[0].To != "owner@example.org" {
		t.Fatalf("the first emails: %+v", first)
	}

	date := now.In(sp).Format("2006-01-02")
	wantText(t, first[0].Subject, "What is new this week, to "+date)
	wantText(t, first[0].Text,
		"Your first weekly email: what is new this week where you have a role.\n\nSt. Joseph Parish\n1 question is waiting for an answer:\n"+base+question+"\n",
		"You get this email every "+now.In(sp).Weekday().String()+" at "+now.In(sp).Format("15")+":00 (America/Sao_Paulo).",
		"Change the day or the hour: "+base+"/account#weekly", "Stop it: "+base+"/digest/stop?t=")

	if strings.Contains(first[0].Text, "/accountant?context=") {
		t.Errorf("the first email speaks of the lists:\n%s", first[0].Text)
	}

	firstStop := stopLink(t, first[0])
	if !slices.Contains(first[0].Headers, [2]string{"List-Unsubscribe", "<" + firstStop + ">"}) ||
		!slices.Contains(first[0].Headers, [2]string{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"}) {
		t.Errorf("the headers: %v", first[0].Headers)
	}

	if got := send(now.Add(10 * time.Minute)); len(got) != 0 {
		t.Errorf("sent twice: %v", got)
	}

	// The week: the owner answers the question, passes the groceries on
	// for the project and the electricity on for nobody.
	wantRedirect(t, e.owner.post(question+"/reply", url.Values{"body": {"Mine, on the way to the retreat."}}), question+"?done=replied")
	passOn(txs["CORNER GROCERY"], choices["World Youth Day"])
	passOn(txs["ELECTRIC CO"], "")

	week := send(now.AddDate(0, 0, 7))
	byTo := map[string]mail.Message{}
	for _, m := range week {
		byTo[m.To] = m
	}

	if len(week) != 2 || byTo["viewer@example.org"].To != "" {
		t.Fatalf("a week on: %+v", week)
	}

	owner, accountant := byTo["owner@example.org"].Text, byTo["accountant@example.org"].Text

	// The coffee, its question answered, has what the accountant needs:
	// the groceries and it are the accountant's news, the electricity the
	// owner's.
	wantText(t, owner, "What is new where you have a role, since your last weekly email.\n\nSt. Joseph Parish\n",
		"1 transaction newly needs more context (1 in all):\n"+base+e.org+"/accountant?context=needed\n")
	wantText(t, accountant, "2 transactions now have what the accountant needs:\n"+base+e.org+"/accountant?context=added\n",
		"1 of your questions was answered:\n"+base+question+"\n", "every "+now.UTC().Weekday().String()+" at "+now.UTC().Format("15")+":00 (UTC).")

	for _, not := range []string{"what the accountant needs", "question"} {
		if strings.Contains(owner, not) {
			t.Errorf("the owner hears %q:\n%s", not, owner)
		}
	}

	if strings.Contains(accountant, "needs more context") {
		t.Errorf("the accountant hears the bookkeeper's line:\n%s", accountant)
	}

	// Every link in an email opens for whoever it was sent to.
	for _, m := range week {
		for _, link := range emailLink.FindAllString(m.Text, -1) {
			if strings.Contains(link, "/digest/stop") {
				continue
			}

			path, _, _ := strings.Cut(strings.TrimPrefix(link, base), "#")
			if rec := people[m.To].get(path); rec.Code != http.StatusOK {
				t.Errorf("%s cannot open %s: %d", m.To, link, rec.Code)
			}
		}
	}

	// Stopping it: the link opens a page and changes nothing; the first
	// email's link no longer works; the latest one's does, from a mail
	// program's button too, which sends no page's headers.
	stop := strings.TrimPrefix(stopLink(t, byTo["owner@example.org"]), base)
	anybody := newBrowser(t, h)

	wantBody(t, anybody.get(stop), "You will get no more weekly emails of what is new.", `<form method="post" action="/digest/stop">`, `<button type="submit">Stop the weekly email</button>`)
	wantBody(t, e.owner.get("/account"), "The next one, if there is anything new for you, comes on")

	if rec := anybody.post(strings.TrimPrefix(firstStop, base), nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "This link no longer stops the weekly email.") {
		t.Errorf("the first email's link: %d", rec.Code)
	}

	oneClick := httptest.NewRequest(http.MethodPost, stop, strings.NewReader("List-Unsubscribe=One-Click"))
	oneClick.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, oneClick)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/digest/stop?done=1" {
		t.Fatalf("a mail program's button: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}

	wantBody(t, anybody.get("/digest/stop?done=1"), "The weekly email is stopped.")
	wantBody(t, e.owner.get("/account"), "You get no weekly email.")

	if rec := anybody.post(stop, url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("stopped twice: %d", rec.Code)
	}

	// Stopped, the owner hears nothing more, though there is something.
	passOn(txs["PARISH OFFERTORY"], "")

	for _, m := range send(now.AddDate(0, 0, 14)) {
		if m.To == "owner@example.org" {
			t.Errorf("stopped, and sent:\n%s", m.Text)
		}
	}
}

// stopLink is the link an email gives to stop it.
func stopLink(t *testing.T, m mail.Message) string {
	t.Helper()

	for _, link := range emailLink.FindAllString(m.Text, -1) {
		if strings.Contains(link, "/digest/stop?t=") {
			return link
		}
	}

	t.Fatalf("no link to stop it in\n%s", m.Text)

	return ""
}

// wantText fails for each of wants that text does not contain.
func wantText(t *testing.T, text string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}
