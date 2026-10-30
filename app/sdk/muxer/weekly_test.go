package muxer

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// The weekly email's card on the profile (docs/accountant.md, 5): the
// defaults and when the next one comes; saved, given back; a zone this app
// does not know, said and kept; and off, said so. Everything invented.
func TestTheWeeklyEmailsSettings(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)
	b := signUp(t, h, sent, "treasurer@example.org")

	wantBody(t, b.get("/account"), `id="weekly"`, `name="enabled" value="1" checked`, `<option value="1" selected>Monday</option>`,
		`<option value="8" selected>08:00</option>`, "No time zone yet, so the hours are UTC.", "at 08:00 UTC.",
		`data-zone-offer="tz"`, `src="/account/static/tz.mjs"`)

	wantRedirect(t, b.post("/account/weekly", url.Values{"enabled": {"1"}, "weekday": {"5"}, "hour": {"17"}, "tz": {"America/Sao_Paulo"}}), "/account?done=weekly#weekly")

	saved := b.get("/account?done=weekly").Body.String()
	for _, want := range []string{"Saved.", `<option value="5" selected>Friday</option>`, `<option value="17" selected>17:00</option>`, `value="America/Sao_Paulo"`, "at 17:00, your time."} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved: no %q in\n%s", want, saved)
		}
	}

	if strings.Contains(saved, "No time zone yet") {
		t.Error("a zone chosen, and none said")
	}

	if rec := b.post("/account/weekly", url.Values{"enabled": {"1"}, "weekday": {"5"}, "hour": {"17"}, "tz": {"Mars/Olympus"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "That time zone is not one this app knows.") || !strings.Contains(rec.Body.String(), `value="Mars/Olympus"`) {
		t.Errorf("an unknown zone: %d\n%s", rec.Code, rec.Body)
	}

	if rec := b.post("/account/weekly", url.Values{"weekday": {"Friday"}, "hour": {"17"}, "tz": {"UTC"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Choose a day of the week and an hour of the day.") {
		t.Errorf("a day that is not a number: %d", rec.Code)
	}

	wantRedirect(t, b.post("/account/weekly", url.Values{"weekday": {"5"}, "hour": {"17"}, "tz": {"America/Sao_Paulo"}}), "/account?done=weekly#weekly")
	wantBody(t, b.get("/account"), "You get no weekly email.")

	// Only the person changes their own, and nobody signed out.
	if rec := newBrowser(t, h).post("/account/weekly", url.Values{"weekday": {"1"}, "hour": {"8"}, "tz": {"UTC"}}); rec.Code != http.StatusForbidden {
		t.Errorf("signed out: %d", rec.Code)
	}
}

// The sign-in forms carry the device's time zone, which is kept when the
// person has none, and never over one they have; and the script that
// fills it is served to anybody, its tests to nobody.
func TestSigningInKeepsTheDevicesTimeZone(t *testing.T) {
	t.Parallel()

	h, sent := newSite(t, sqldb.Infrastructure, nil)

	signIn := func(zone string) *browser {
		t.Helper()

		b := newBrowser(t, h)
		wantRedirect(t, b.post("/sign-in", url.Values{"email": {"treasurer@example.org"}}), "/sign-in/code")
		wantBody(t, b.get("/sign-in/code"), `name="tz" data-device-zone`, `src="/account/static/tz.mjs"`)

		code, _ := codeIn(t, sent)
		if rec := b.post("/sign-in/code", url.Values{"code": {code}, "tz": {zone}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("signing in: %d\n%s", rec.Code, rec.Body)
		}

		return b
	}

	signIn("Not/AZone")
	wantBody(t, signIn("Europe/Lisbon").get("/account"), `value="Europe/Lisbon"`)
	wantBody(t, signIn("Asia/Tokyo").get("/account"), `value="Europe/Lisbon"`)

	anybody := newBrowser(t, h)

	if rec := anybody.get("/account/static/tz.mjs"); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") ||
		!strings.Contains(rec.Body.String(), "data-device-zone") {
		t.Errorf("the script: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	for _, path := range []string{"/account/static/tz_browser_test.mjs", "/account/static/nothing.mjs", "/account/static/tz.go"} {
		if rec := anybody.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}
