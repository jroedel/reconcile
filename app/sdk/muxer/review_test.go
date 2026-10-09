package muxer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The administrator names a translator of one language: they get the review
// screen and a key for that language only, and lose both when removed.
func TestATranslatorIsNamedForOneLanguage(t *testing.T) {
	s := newTranslatingSite(t)
	ana := signUp(t, s.h, s.sent, "ana@example.org")

	if rec := ana.get("/translations"); rec.Code != http.StatusNotFound {
		t.Fatalf("before: %d", rec.Code)
	}

	// Somebody who has not signed up cannot be named yet.
	rec := s.admin.post("/admin/translators", url.Values{"email": {"nobody@example.org"}, "lang": {"pt"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown address: %d", rec.Code)
	}

	wantBody(t, rec, "Nobody has signed up with that address")

	if rec := s.admin.post("/admin/translators", url.Values{"email": {"ana@example.org"}, "lang": {"en"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a translator of English: %d", rec.Code)
	}

	// And nobody but the administrator names anybody.
	if rec := ana.post("/admin/translators", url.Values{"email": {"ana@example.org"}, "lang": {"pt"}}); rec.Code != http.StatusNotFound {
		t.Errorf("naming herself: %d", rec.Code)
	}

	wantRedirect(t, s.admin.post("/admin/translators", url.Values{"email": {"ana@example.org"}, "lang": {"pt"}}), "/admin#translators")
	wantBody(t, s.admin.get("/admin"), "ana@example.org", "Português", `href="/account/keys"`)

	wantBody(t, ana.get("/account"), `href="/translations"`, `href="/account/keys"`)
	wantBody(t, ana.get("/translations"), "Not translated yet", `href="/account/keys"`)

	if rec := ana.get("/translations?in=es"); rec.Code != http.StatusNotFound {
		t.Errorf("her Spanish: %d", rec.Code)
	}

	key := makeKey(t, ana, "laptop")

	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=pt", key, ""); rec.Code != http.StatusOK {
		t.Errorf("her key, Portuguese: %d", rec.Code)
	}

	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", key, ""); rec.Code != http.StatusForbidden {
		t.Errorf("her key, Spanish: %d", rec.Code)
	}

	// Removed: the screen, the keys screen and the key are gone with it.
	page := s.admin.get("/admin").Body.String()

	i := strings.Index(page, `action="/admin/translators/`)
	if i < 0 {
		t.Fatal("no remove button")
	}

	remove := page[i+len(`action="`):]
	remove = remove[:strings.Index(remove, `"`)]

	wantRedirect(t, s.admin.post(remove, nil), "/admin#translators")

	for _, path := range []string{"/translations", "/account/keys"} {
		if rec := ana.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("after, %s: %d", path, rec.Code)
		}
	}

	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=pt", key, ""); rec.Code != http.StatusForbidden {
		t.Errorf("after, her key: %d", rec.Code)
	}
}

// review posts one string's form as the page writes it.
func review(b *browser, do, en, shown, text, note string) *httptest.ResponseRecorder {
	return b.post("/translations/review", url.Values{
		"in": {"es"}, "show": {"check"}, "context": {""}, "en": {en},
		"shown": {shown}, "text": {text}, "note": {note}, "do": {do},
	})
}

// Looking over what Claude wrote, on the screen: keep, change, send back,
// and all at once, each moving the string to its queue.
func TestLookingOverTranslationsOnTheScreen(t *testing.T) {
	s := newTranslatingSite(t)
	key := makeKey(t, s.admin, "laptop")

	rec := s.api(http.MethodPut, "/api/v1/translations", key, `{"lang": "es", "translations": [
		{"context": "", "en": "Your account", "text": "Tu cuenta"},
		{"context": "", "en": "Save", "text": "Guardar"},
		{"context": "", "en": "Sign out", "text": "Salir"},
		{"context": "", "en": "Revoke", "text": "Revocar"}
	]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("Claude's batch: %d %s", rec.Code, rec.Body)
	}

	wantBody(t, s.admin.get("/translations?in=es"), "To check (4)", "Tu cuenta", "written by Claude", "account.html")

	// Kept as it is.
	wantRedirect(t, review(s.admin, "keep", "Your account", "Tu cuenta", "Tu cuenta", ""), "/translations?in=es&show=check&done=kept")

	// Changed by hand; a dropped placeholder would be refused, and what
	// was typed stays in the box.
	wantRedirect(t, review(s.admin, "keep", "Sign out", "Salir", "Cerrar sesión", ""), "/translations?in=es&show=check&done=kept")

	// Words that changed while the page was open.
	rec = review(s.admin, "keep", "Save", "Guardar cambios", "Guardar cambios", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("stale words: %d", rec.Code)
	}

	wantBody(t, rec, "These words changed while you were reading them")

	// Sent back with no note, then with one.
	rec = review(s.admin, "send-back", "Revoke", "Revocar", "Revocar", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no note: %d", rec.Code)
	}

	wantRedirect(t, review(s.admin, "send-back", "Revoke", "Revocar", "Revocar", "This revokes a key: say it plainly."), "/translations?in=es&show=check&done=sent-back")

	var pending struct {
		Pending []struct {
			EN, Note, Current string
		} `json:"pending"`
	}

	rec = s.api(http.MethodGet, "/api/v1/translations/pending?lang=es&limit=1", key, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil || len(pending.Pending) != 1 || pending.Pending[0].EN != "Revoke" || pending.Pending[0].Current != "Revocar" || pending.Pending[0].Note == "" {
		t.Errorf("Claude's next list: %v %s", err, rec.Body)
	}

	// The rest at once: the one left to check.
	wantRedirect(t, s.admin.post("/translations/approve-all", url.Values{
		"in": {"es"}, "show": {"check"},
		"context": {""}, "en": {"Save"}, "text": {"Guardar"},
	}), "/translations?in=es&show=check&done=approved&n=1")

	wantBody(t, s.admin.get("/translations?in=es&show=approved"), "Approved (3)", "Cerrar sesión", "written by admin@example.org")
	wantBody(t, s.admin.get("/translations?in=es&show=sent-back"), "Sent back (1)", "This revokes a key: say it plainly.")

	// And Claude may not undo a person's words.
	rec = s.api(http.MethodPut, "/api/v1/translations", key, `{"lang": "es", "translations": [{"context": "", "en": "Sign out", "text": "Salir"}]}`)
	wantBody(t, rec, `"outcome": "refused"`)

	// Somebody who does not translate has no screen.
	stranger := signUp(t, s.h, s.sent, "stranger@example.org")

	if rec := stranger.get("/translations"); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's screen: %d", rec.Code)
	}

	if rec := review(stranger, "keep", "Save", "Guardar", "Lo que sea", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger reviewing: %d", rec.Code)
	}
}
