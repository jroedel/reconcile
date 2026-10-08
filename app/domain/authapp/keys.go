package authapp

import (
	"context"
	"errors"
	"net/http"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// This file is the screen where a translator makes and revokes their own API
// keys, for a program -- their Claude, a script -- to fill the translations
// through /api/v1 (docs/translations.md). Lifted from stewards.
//
// Only somebody who may translate sees it: a key reaches nothing but the
// translations, so for anybody else it would be a secret that opens nothing,
// and a screen offering one would be a question nobody can answer well. To
// them the address is a 404, as any page that is not theirs is.
//
// Each person sees only their own keys, including the ones a program was
// given through OAuth (oauthapp), which is where they end one: a key acts as
// whoever made it, so nobody else has a reason to hold or revoke it.
//
// A new key is shown once, on the page the form returns, and never again;
// that page is not a redirect, because the key must not be in a URL, and it
// is marked no-store so the back button does not bring it back from a cache.

// KeysPath is the screen, which the API's refusals and oauthapp name.
const KeysPath = "/account/keys"

// Translators says who may translate, which is who may hold a key.
type Translators interface {
	MayTranslateAny(ctx context.Context, who translationbus.Translator) (bool, error)
}

func mountKeys(mux *http.ServeMux, a app, guard web.Middleware) {
	mux.Handle("GET "+KeysPath, guard(http.HandlerFunc(a.keys)))
	mux.Handle("POST "+KeysPath, guard(http.HandlerFunc(a.makeKey)))
	mux.Handle("POST "+KeysPath+"/{id}/revoke", guard(http.HandlerFunc(a.revokeKey)))
}

type keyRow struct {
	ID, Name                  string
	Created, Expires, LastUse string // "" for never used
	Program                   bool   // given to a program through OAuth
}

type keysView struct {
	Keys   []keyRow
	NewKey string // shown once
	Index  string // the API's index, to say where to start
	Max    int

	// Name is what was typed, kept when it is refused. Done and Problem are
	// codes the template words.
	Name    string
	Done    string
	Problem string
}

// translates reports whether the signed-in person may hold a key. Nobody may
// when there is nothing to ask.
func (a app) translates(ctx context.Context, u userbus.User) (bool, error) {
	if a.cfg.Translators == nil {
		return false, nil
	}

	return a.cfg.Translators.MayTranslateAny(ctx, translationbus.Translator{ID: u.ID, SiteAdmin: u.SiteAdmin})
}

// translator is the person behind a keys route, or false with the answer
// already written: a sign-in, a 404, or a 500.
func (a app) translator(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := signedIn(w, r)
	if !ok {
		return userbus.User{}, false
	}

	may, err := a.translates(r.Context(), u)

	switch {
	case err != nil:
		a.keysFailed(w, r, "whether somebody translates", err)

		return userbus.User{}, false
	case !may:
		http.NotFound(w, r)

		return userbus.User{}, false
	}

	return u, true
}

func (a app) keys(w http.ResponseWriter, r *http.Request) {
	u, ok := a.translator(w, r)
	if !ok {
		return
	}

	a.showKeys(w, r, http.StatusOK, u, keysView{Done: r.URL.Query().Get("done")})
}

func (a app) makeKey(w http.ResponseWriter, r *http.Request) {
	u, ok := a.translator(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		a.showKeys(w, r, http.StatusBadRequest, u, keysView{Problem: "unreadable"})

		return
	}

	name := r.PostFormValue("name")

	_, key, err := a.cfg.Users.CreateAPIKey(r.Context(), a.cfg.Now(), u.ID, name)

	switch {
	case errors.Is(err, userbus.ErrKeyName):
		a.showKeys(w, r, http.StatusUnprocessableEntity, u, keysView{Name: name, Problem: "key-name"})

		return
	case errors.Is(err, userbus.ErrTooManyKeys):
		a.showKeys(w, r, http.StatusUnprocessableEntity, u, keysView{Name: name, Problem: "too-many-keys"})

		return
	case err != nil:
		a.keysFailed(w, r, "making an API key", err)

		return
	}

	w.Header().Set("Cache-Control", "no-store")
	a.showKeys(w, r, http.StatusOK, u, keysView{NewKey: key})
}

func (a app) revokeKey(w http.ResponseWriter, r *http.Request) {
	u, ok := a.translator(w, r)
	if !ok {
		return
	}

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	switch err := a.cfg.Users.RevokeAPIKey(r.Context(), u.ID, id); {
	case err == nil, errors.Is(err, userbus.ErrNotFound):
		// Already gone, or never this person's: either way there is no
		// key of theirs by that id now, which is what was asked.
		http.Redirect(w, r, KeysPath+"?done=revoked", http.StatusSeeOther)
	default:
		a.keysFailed(w, r, "revoking an API key", err)
	}
}

func (a app) showKeys(w http.ResponseWriter, r *http.Request, status int, u userbus.User, v keysView) {
	keys, err := a.cfg.Users.APIKeys(r.Context(), a.cfg.Now(), u.ID)
	if err != nil {
		a.keysFailed(w, r, "listing API keys", err)

		return
	}

	const day = "2006-01-02"

	for _, k := range keys {
		row := keyRow{
			ID: k.ID.String(), Name: k.Name, Program: k.Client != "",
			Created: k.CreatedAt.UTC().Format(day), Expires: k.ExpiresAt.UTC().Format(day),
		}
		if !k.LastUsedAt.IsZero() {
			row.LastUse = k.LastUsedAt.UTC().Format(day)
		}

		v.Keys = append(v.Keys, row)
	}

	v.Index = a.cfg.BaseURL + "/api/v1"
	v.Max = userbus.MaxAPIKeys

	a.cfg.Render.Render(w, r, status, "account-keys", v)
}

func (a app) keysFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.Error(what+" failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account-keys", keysView{Problem: "server"})
}
