package authapp

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// This file is the screen where a person makes and revokes their own API
// keys, for a program -- their Claude, a script -- to work with the site
// through /api/v1 (docs/translations.md, docs/books-api.md). Lifted from
// stewards.
//
// Anybody signed in may hold a key, and says what it is for when they make
// it: a purpose, in plain words, which is a set of scopes (userbus.Scope).
// Only the purposes there is something for are offered: a script that sends
// statements to the inbox, for everybody, and translating, for those who
// may translate (translationbus.MayTranslate). Reading and keeping the books
// join them when the API can do those (docs/books-api.md, build order).
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

// Translators says who may translate, which is who may hold a translating
// key.
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

	// Purpose is the key's scopes as a purpose the template words; "" for
	// a set no purpose is (a program's, given through OAuth).
	Purpose string
}

// The purposes a key may be made for, each a set of scopes: the form's
// choice, and what the list says of each key.
var purposes = map[string][]userbus.Scope{
	"upload":    {userbus.Upload},
	"translate": {userbus.Translate},
}

// purposeOf is the purpose a key's scopes are, or "".
func purposeOf(scopes []userbus.Scope) string {
	for name, set := range purposes {
		if slices.Equal(set, scopes) {
			return name
		}
	}

	return ""
}

type keysView struct {
	Keys   []keyRow
	NewKey string // shown once
	Index  string // the API's index, to say where to start
	Max    int

	// Translates is whether the person may make a translating key.
	Translates bool

	// NewPurpose is the purpose of the key just made, for what to say
	// beside it; Name and Purpose are what was chosen, kept when it is
	// refused. Done and Problem are codes the template words.
	NewPurpose string
	Name       string
	Purpose    string
	Done       string
	Problem    string
}

// translates reports whether the signed-in person may hold a key. Nobody may
// when there is nothing to ask.
func (a app) translates(ctx context.Context, u userbus.User) (bool, error) {
	if a.cfg.Translators == nil {
		return false, nil
	}

	return a.cfg.Translators.MayTranslateAny(ctx, translationbus.Translator{ID: u.ID, SiteAdmin: u.SiteAdmin})
}

func (a app) keys(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	a.showKeys(w, r, http.StatusOK, u, keysView{Done: r.URL.Query().Get("done")})
}

func (a app) makeKey(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		a.showKeys(w, r, http.StatusBadRequest, u, keysView{Problem: "unreadable"})

		return
	}

	name, purpose := r.PostFormValue("name"), r.PostFormValue("purpose")
	kept := keysView{Name: name, Purpose: purpose}

	scopes, known := purposes[purpose]
	if purpose == "translate" {
		may, err := a.translates(r.Context(), u)
		if err != nil {
			a.keysFailed(w, r, "whether somebody translates", err)

			return
		}

		known = may
	}

	if !known {
		kept.Problem = "key-purpose"
		a.showKeys(w, r, http.StatusUnprocessableEntity, u, kept)

		return
	}

	_, key, err := a.cfg.Users.CreateAPIKey(r.Context(), a.cfg.Now(), u.ID, name, scopes)

	switch {
	case errors.Is(err, userbus.ErrKeyName):
		kept.Problem = "key-name"
		a.showKeys(w, r, http.StatusUnprocessableEntity, u, kept)

		return
	case errors.Is(err, userbus.ErrTooManyKeys):
		kept.Problem = "too-many-keys"
		a.showKeys(w, r, http.StatusUnprocessableEntity, u, kept)

		return
	case err != nil:
		a.keysFailed(w, r, "making an API key", err)

		return
	}

	w.Header().Set("Cache-Control", "no-store")
	a.showKeys(w, r, http.StatusOK, u, keysView{NewKey: key, NewPurpose: purpose})
}

func (a app) revokeKey(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
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
			ID: k.ID.String(), Name: k.Name, Program: k.Client != "", Purpose: purposeOf(k.Scopes),
			Created: k.CreatedAt.UTC().Format(day), Expires: k.ExpiresAt.UTC().Format(day),
		}
		if !k.LastUsedAt.IsZero() {
			row.LastUse = k.LastUsedAt.UTC().Format(day)
		}

		v.Keys = append(v.Keys, row)
	}

	v.Index = a.cfg.BaseURL + "/api/v1"
	v.Max = userbus.MaxAPIKeys

	if v.Translates, err = a.translates(r.Context(), u); err != nil {
		a.keysFailed(w, r, "whether somebody translates", err)

		return
	}

	a.cfg.Render.Render(w, r, status, "account-keys", v)
}

func (a app) keysFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.Error(what+" failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account-keys", keysView{Problem: "server"})
}
