// Package adminapp is the site administrator's page: the list of users, with
// the power to stop one signing in, the translators, and the names of the
// organizations and accounts that exist. Nothing about anybody's money --
// that takes a grant, like anybody else's (tenancybus).
//
// Translators are named here because naming one is the site's decision, not
// an organization's: a translator's words are on every page of the site, and
// their key reaches the translation API (docs/translations.md).
package adminapp

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates
var templates embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = templates

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Users is the part of userbus this app uses.
type Users interface {
	All(ctx context.Context) ([]userbus.User, error)
	SetEnabled(ctx context.Context, now time.Time, userID types.ID, enabled bool) (userbus.User, error)
	UserIDByEmail(ctx context.Context, email types.Email) (types.ID, bool, error)
}

// Translators is the part of translationbus this app uses.
type Translators interface {
	Translators(ctx context.Context, who translationbus.Translator) ([]translationbus.Grant, error)
	AddTranslator(ctx context.Context, now time.Time, who translationbus.Translator, userID types.ID, lang types.Lang) error
	RemoveTranslator(ctx context.Context, who translationbus.Translator, userID types.ID, lang types.Lang) error
}

// Names is the part of tenancybus this app uses.
type Names interface {
	Names(ctx context.Context) ([]tenancybus.Org, []tenancybus.Account, error)
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Users  Users
	Names  Names
	Render Renderer

	Translators Translators

	Now func() time.Time
}

type app struct {
	cfg Config
}

// Routes mounts this app. guard is mid.Require; on top of it, anybody who is
// not the site administrator gets the 404 a missing page gets, so the page
// does not announce itself.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	admin := func(h http.HandlerFunc) http.Handler {
		return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := mid.UserFrom(r.Context()); !ok || !u.SiteAdmin {
				http.NotFound(w, r)

				return
			}

			h(w, r)
		}))
	}

	mux.Handle("GET /admin", admin(a.page))
	mux.Handle("POST /admin/users/{id}/enabled", admin(a.setEnabled))
	mux.Handle("POST /admin/translators", admin(a.addTranslator))
	mux.Handle("POST /admin/translators/{id}/{lang}/remove", admin(a.removeTranslator))
}

type view struct {
	Users       []userbus.User
	Orgs        []tenancybus.Org
	Accounts    []tenancybus.Account
	Translators []translatorRow
	Langs       []types.Lang
	Me          types.ID
	Problem     string

	// Email is what was typed into the translator form, kept when it is
	// refused.
	Email string
}

type translatorRow struct {
	UserID types.ID
	Who    string
	Lang   types.Lang
}

func (a app) page(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, view{})
}

func (a app) render(w http.ResponseWriter, r *http.Request, status int, v view) {
	me, _ := mid.UserFrom(r.Context())

	users, err := a.cfg.Users.All(r.Context())
	if err != nil {
		a.failed(w, r, err)

		return
	}

	orgs, accounts, err := a.cfg.Names.Names(r.Context())
	if err != nil {
		a.failed(w, r, err)

		return
	}

	grants, err := a.cfg.Translators.Translators(r.Context(), translator(me))
	if err != nil {
		a.failed(w, r, err)

		return
	}

	named := map[types.ID]string{}
	for _, u := range users {
		named[u.ID] = u.Named()
	}

	for _, g := range grants {
		v.Translators = append(v.Translators, translatorRow{UserID: g.UserID, Who: named[g.UserID], Lang: g.Lang})
	}

	v.Users, v.Orgs, v.Accounts, v.Me, v.Langs = users, orgs, accounts, me.ID, types.Translated

	a.cfg.Render.Render(w, r, status, "admin", v)
}

func translator(u userbus.User) translationbus.Translator {
	return translationbus.Translator{ID: u.ID, SiteAdmin: u.SiteAdmin}
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	a.cfg.Log.Error("the administration page failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong at our end. Please try again in a few minutes.", http.StatusInternalServerError)
}

func (a app) setEnabled(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.UserFrom(r.Context())

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)

		return
	}

	// Not yourself: a site administrator who disables their own account
	// cannot sign in to undo it, and the bootstrap secret is spent.
	if id == me.ID {
		a.render(w, r, http.StatusUnprocessableEntity, view{Problem: "self"})

		return
	}

	if _, err := a.cfg.Users.SetEnabled(r.Context(), a.cfg.Now(), id, r.PostFormValue("enabled") == "1"); err != nil {
		if errors.Is(err, userbus.ErrNotFound) {
			http.NotFound(w, r)

			return
		}

		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// addTranslator makes somebody who has signed up a translator of a
// language. Only somebody who has: a translator is a person the
// administrator knows, and an address with nobody behind it would be a
// grant waiting for whoever signs up with it first.
func (a app) addTranslator(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.UserFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)

		return
	}

	typed := r.PostFormValue("email")

	email, err := types.ParseEmail(typed)
	if err != nil {
		a.render(w, r, http.StatusUnprocessableEntity, view{Problem: "translator-email", Email: typed})

		return
	}

	id, found, err := a.cfg.Users.UserIDByEmail(r.Context(), email)

	switch {
	case err != nil:
		a.failed(w, r, err)

		return
	case !found:
		a.render(w, r, http.StatusUnprocessableEntity, view{Problem: "translator-unknown", Email: typed})

		return
	}

	err = a.cfg.Translators.AddTranslator(r.Context(), a.cfg.Now(), translator(me), id, types.Lang(r.PostFormValue("lang")))

	switch {
	case errors.Is(err, translationbus.ErrInvalid):
		a.render(w, r, http.StatusUnprocessableEntity, view{Problem: "translator-lang", Email: typed})

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	a.cfg.Log.Info("translator named", "user_id", id.String(), "lang", r.PostFormValue("lang"), "by", me.ID.String())

	http.Redirect(w, r, "/admin#translators", http.StatusSeeOther)
}

func (a app) removeTranslator(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.UserFrom(r.Context())

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	lang := types.Lang(r.PathValue("lang"))

	if err := a.cfg.Translators.RemoveTranslator(r.Context(), translator(me), id, lang); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Log.Info("translator removed", "user_id", id.String(), "lang", string(lang), "by", me.ID.String())

	http.Redirect(w, r, "/admin#translators", http.StatusSeeOther)
}
