// Package adminapp is the site administrator's page: the list of users, with
// the power to stop one signing in, and the names of the organizations and
// accounts that exist. Nothing about anybody's money -- that takes a grant,
// like anybody else's (tenancybus).
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
	Now    func() time.Time
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
}

type view struct {
	Users    []userbus.User
	Orgs     []tenancybus.Org
	Accounts []tenancybus.Account
	Me       types.ID
	Problem  string
}

func (a app) page(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "")
}

func (a app) render(w http.ResponseWriter, r *http.Request, status int, problem string) {
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

	a.cfg.Render.Render(w, r, status, "admin", view{Users: users, Orgs: orgs, Accounts: accounts, Me: me.ID, Problem: problem})
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
		a.render(w, r, http.StatusUnprocessableEntity, "self")

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
