package muxer

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// made follows a creating POST to the page it made, and returns that page's
// path.
func made(t *testing.T, b *browser, path string, form url.Values) string {
	t.Helper()

	rec := b.post(path, form)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "?done=created") {
		t.Fatalf("POST %s: %d to %q\n%s", path, rec.Code, loc, rec.Body.String())
	}

	return strings.TrimSuffix(loc, "?done=created")
}

var removeLink = regexp.MustCompile(`action="(/[a-z]+/[0-9a-f]+/people/[0-9a-f]+)/remove"`)

// grants is the people forms' paths on a page, one per grant.
func grants(t *testing.T, body string) []string {
	t.Helper()

	var out []string
	for _, m := range removeLink.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}

	return out
}

// estate is one owner's organization with an account and a project in it,
// and a personal account and project beside it.
type estate struct {
	owner                  *browser
	org, account, project  string
	ownAccount, ownProject string
	orgGrant               string
}

func newEstate(t *testing.T, h http.Handler, sent *mail.Recorder) estate {
	t.Helper()

	owner := signUp(t, h, sent, "owner@example.org")
	e := estate{owner: owner}

	e.org = made(t, owner, "/orgs", url.Values{"name": {"St. Joseph Parish"}})
	orgID := strings.TrimPrefix(e.org, "/orgs/")

	e.account = made(t, owner, "/accounts", url.Values{"org": {orgID}, "name": {"Parish checking"}, "kind": {"checking"}, "last4": {"1234"}})
	e.project = made(t, owner, "/projects", url.Values{"org": {orgID}, "name": {"World Youth Day"}, "starts": {"2027-08-01"}})
	e.ownAccount = made(t, owner, "/accounts", url.Values{"name": {"My card"}, "kind": {"card"}})
	e.ownProject = made(t, owner, "/projects", url.Values{"name": {"Camino"}})

	g := grants(t, owner.get(e.org).Body.String())
	if len(g) != 1 {
		t.Fatalf("the new organization has %d grants, want its owner's", len(g))
	}

	e.orgGrant = g[0]

	return e
}

func TestMakingAnOrganizationAndWhatIsInIt(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	wantBody(t, e.owner.get(e.org+"?done=created"), "St. Joseph Parish", "Parish checking", "···1234", "World Youth Day", "Made.")
	wantBody(t, e.owner.get("/"), "St. Joseph Parish", "My card", "Camino")
	wantBody(t, e.owner.get(e.account), "Parish checking", `href="`+e.org+`"`)

	// The history says who made it.
	wantBody(t, e.owner.get(e.org), "made it, as St. Joseph Parish")

	// A name that is not one is said, and kept.
	rec := e.owner.post("/orgs", url.Values{"name": {"   "}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an empty name: %d", rec.Code)
	}

	wantBody(t, e.owner.post("/accounts", url.Values{"name": {"x"}, "kind": {"card"}, "last4": {"4111111111111111"}}),
		"Type only the last four digits", `value="4111111111111111"`)
}

// Everything somebody else owns answers like an address that does not
// exist, through every route, and nothing changes.
func TestNobodyReachesWhatTheyWereNotGiven(t *testing.T) {
	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { c.Bootstrap = secret })
	e := newEstate(t, h, sent)

	stranger := signUp(t, h, sent, "stranger@example.org")

	// The site administrator is a stranger here too.
	admin := newBrowser(t, h)
	admin.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}})

	orgID := strings.TrimPrefix(e.org, "/orgs/")

	gets := []string{
		e.org, e.account, e.project, e.ownAccount, e.ownProject,
		"/accounts/new?org=" + orgID, "/projects/new?org=" + orgID,
		e.org + "/categories", e.ownAccount + "/categories", e.account + "/categories",
		e.project + "/book", e.ownProject + "/book",
		e.account + "/transactions", e.ownAccount + "/transactions",
		e.account + "/months", e.ownAccount + "/months",
		e.account + "/rules", e.ownAccount + "/rules",
		e.account + "/export?from=2026-07&to=2026-07", e.ownAccount + "/export?from=2026-07&to=2026-07",
		e.project + "/export", e.ownProject + "/export",
		e.account + "/receipts", e.project + "/receipts", e.ownAccount + "/receipts", e.ownProject + "/receipts",
	}

	type write struct {
		path string
		form url.Values
	}

	writes := []write{
		{e.org + "/rename", url.Values{"name": {"Taken"}}},
		{e.org + "/archive", url.Values{"archived": {"1"}}},
		{e.org + "/people", url.Values{"email": {"stranger@example.org"}, "role": {"owner"}}},
		{e.orgGrant + "/role", url.Values{"role": {"viewer"}}},
		{e.orgGrant + "/remove", nil},
		{"/accounts", url.Values{"org": {orgID}, "name": {"Mine now"}, "kind": {"cash"}}},
		{"/projects", url.Values{"org": {orgID}, "name": {"Mine now"}}},
		{e.org + "/categories", url.Values{"name": {"Mine now"}, "kind": {"expense"}}},
		{e.ownAccount + "/categories", url.Values{"name": {"Mine now"}, "kind": {"expense"}}},
	}

	for _, account := range []string{e.account, e.ownAccount} {
		writes = append(writes,
			write{account + "/rules", url.Values{"match": {"Mine now"}, "direction": {"out"}}},
			write{account + "/rules/apply", nil},
		)
	}

	for _, scope := range []string{e.account, e.project, e.ownAccount, e.ownProject} {
		writes = append(writes,
			write{scope + "/edit", url.Values{"name": {"Taken"}, "kind": {"cash"}}},
			write{scope + "/archive", url.Values{"archived": {"1"}}},
			write{scope + "/people", url.Values{"email": {"stranger@example.org"}, "role": {"owner"}}},
		)
	}

	before := e.owner.get(e.org).Body.String()

	for name, b := range map[string]*browser{"a stranger": stranger, "the site administrator": admin} {
		for _, path := range gets {
			if rec := b.get(path); rec.Code != http.StatusNotFound {
				t.Errorf("%s: GET %s = %d, want 404", name, path, rec.Code)
			}
		}

		for _, w := range writes {
			if rec := b.post(w.path, w.form); rec.Code != http.StatusNotFound {
				t.Errorf("%s: POST %s = %d, want 404", name, w.path, rec.Code)
			}
		}

		if ov := b.get("/").Body.String(); strings.Contains(ov, "St. Joseph") || strings.Contains(ov, "My card") {
			t.Errorf("%s's front page shows the owner's things", name)
		}
	}

	// Signed out: sent to sign in, or refused.
	nobody := newBrowser(t, h)
	for _, path := range gets {
		if rec := nobody.get(path); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/sign-in") {
			t.Errorf("signed out: GET %s = %d", path, rec.Code)
		}
	}

	for _, w := range writes {
		if rec := nobody.post(w.path, w.form); rec.Code != http.StatusForbidden {
			t.Errorf("signed out: POST %s = %d, want 403", w.path, rec.Code)
		}
	}

	// And nothing changed -- not a name, not a person, not a line of
	// history.
	if after := e.owner.get(e.org).Body.String(); after != before {
		t.Error("the organization's page changed after the strangers were through")
	}

	wantBody(t, e.owner.get(e.ownAccount), "My card")
}

// A viewer sees, and is refused when they try to change anything.
func TestAViewerSeesAndChangesNothing(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	wantRedirect(t, e.owner.post(e.org+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.org+"?done=granted")

	viewer := signUp(t, h, sent, "viewer@example.org")

	page := viewer.get(e.org).Body.String()
	if !strings.Contains(page, "Parish checking") || strings.Contains(page, "/rename") || strings.Contains(page, "Give somebody a role") {
		t.Errorf("the viewer's page:\n%s", page)
	}

	if rec := viewer.post(e.org+"/rename", url.Values{"name": {"Mine"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer renamed it: %d", rec.Code)
	}

	// Inherited: the account in it is readable, the personal one is not.
	if rec := viewer.get(e.account); rec.Code != http.StatusOK {
		t.Errorf("the organization's account: %d", rec.Code)
	}

	if rec := viewer.get(e.ownAccount); rec.Code != http.StatusNotFound {
		t.Errorf("the owner's personal account: %d", rec.Code)
	}
}

// An invitation goes by mail, says what and who, has no sign-in in it, and
// the role is waiting when they sign up.
func TestAnInvitationBecomesARole(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	e.owner.post("/account/profile", url.Values{"name": {"Ana Pérez"}})
	e.owner.post(e.project+"/people", url.Values{"email": {"pilgrim@example.org"}, "role": {"contributor"}})

	m, _ := sent.Last()
	if m.To != "pilgrim@example.org" || !strings.Contains(m.Subject, "Ana Pérez") || !strings.Contains(m.Subject, "World Youth Day") ||
		!strings.Contains(m.Text, "Contributor") || !strings.Contains(m.Text, base+"/sign-in?email=pilgrim%40example.org") {
		t.Errorf("the invitation: %q\n%s", m.Subject, m.Text)
	}

	wantBody(t, e.owner.get(e.project), "pilgrim@example.org", "invited, not signed in yet")

	pilgrim := signUp(t, h, sent, "pilgrim@example.org")
	wantBody(t, pilgrim.get("/"), "World Youth Day")

	if rec := pilgrim.get(e.account); rec.Code != http.StatusNotFound {
		t.Errorf("a role on the project reached the account: %d", rec.Code)
	}

	wantBody(t, e.owner.get(e.project), "signed in and took up the role given to pilgrim@example.org")
}

func TestTheLastOwnerIsToldWhy(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	rec := e.owner.post(e.orgGrant+"/role", url.Values{"role": {"viewer"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("the last owner stepped down: %d", rec.Code)
	}

	wantBody(t, rec, "Somebody else must be an owner first")
}

func TestTheAdministratorsPage(t *testing.T) {
	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	h, sent := newSite(t, sqldb.Infrastructure, func(c *Config) { c.Bootstrap = secret })
	e := newEstate(t, h, sent)

	if rec := e.owner.get("/admin"); rec.Code != http.StatusNotFound {
		t.Errorf("somebody who is not the administrator: %d", rec.Code)
	}

	admin := newBrowser(t, h)
	admin.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}})

	page := admin.get("/admin").Body.String()
	for _, want := range []string{"owner@example.org", "St. Joseph Parish", "Parish checking"} {
		if !strings.Contains(page, want) {
			t.Errorf("the administrator's page lacks %q", want)
		}
	}

	// Names, and never money or people's roles: the project and the
	// personal project are not on it at all.
	if strings.Contains(page, "World Youth Day") {
		t.Error("the administrator's page lists projects")
	}

	id := regexp.MustCompile(`action="/admin/users/([0-9a-f]+)/enabled"`).FindStringSubmatch(page)
	if id == nil {
		t.Fatal("no user can be disabled")
	}

	wantRedirect(t, admin.post("/admin/users/"+id[1]+"/enabled", url.Values{"enabled": {"0"}}), "/admin")

	if rec := e.owner.get("/"); strings.Contains(rec.Body.String(), "St. Joseph") {
		t.Error("a disabled user is still signed in")
	}
}
