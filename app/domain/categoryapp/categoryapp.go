// Package categoryapp is the pages for category lists: an organization's,
// and a personal account's.
//
// An account in an organization has no list of its own; its address here
// sends the reader on to the organization's.
package categoryapp

import (
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates
var files embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = files

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Config is what this app needs.
type Config struct {
	Log        *slog.Logger
	Categories *categorybus.Business
	Tenancy    *tenancybus.Business
	Render     Renderer

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type app struct {
	cfg Config
}

// Routes mounts this app behind guard, which is mid.Require.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, guard(h)) }

	handle("GET /orgs/{id}/categories", a.list(types.ScopeOrg))
	handle("POST /orgs/{id}/categories", a.create(types.ScopeOrg))
	handle("GET /accounts/{id}/categories", a.list(types.ScopeAccount))
	handle("POST /accounts/{id}/categories", a.create(types.ScopeAccount))
	handle("POST /categories/{id}/rename", a.rename)
	handle("POST /categories/{id}/archive", a.archive)
}

func actor(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
	}

	return u, ok
}

func (a app) pathID(w http.ResponseWriter, r *http.Request) (types.ID, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		a.cfg.Render.Render(w, r, http.StatusNotFound, "categories-missing", nil)

		return types.ID{}, false
	}

	return id, true
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, categorybus.ErrNotFound):
		a.cfg.Render.Render(w, r, http.StatusNotFound, "categories-missing", nil)
	case errors.Is(err, categorybus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "categories-refused", nil)
	default:
		a.cfg.Log.Error("a request about categories failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "categories-refused", "server")
	}
}

// problem is the code a page words for an error with what was typed.
func problem(err error) string {
	if _, ok := errors.AsType[tenancybus.Invalid](err); ok {
		return "name"
	}

	if errors.Is(err, categorybus.ErrDuplicate) {
		return "duplicate"
	}

	return ""
}

// pathOf is the list page of an owner.
func pathOf(owner types.Scope) string {
	if owner.Kind == types.ScopeOrg {
		return "/orgs/" + owner.ID.String() + "/categories"
	}

	return "/accounts/" + owner.ID.String() + "/categories"
}

type listView struct {
	Path        string
	OwnerName   string
	OwnerPath   string
	CanBookkeep bool

	Categories []categorybus.Category
	Archived   []categorybus.Category

	// Typed into the add form, kept when it is refused.
	Name    string
	Problem string
	Done    string
}

// owner is the scope a list path names, its name and its page. An account
// in an organization is sent to the organization's list.
func (a app) owner(w http.ResponseWriter, r *http.Request, me userbus.User, kind types.ScopeKind) (types.Scope, string, bool) {
	id, ok := a.pathID(w, r)
	if !ok {
		return types.Scope{}, "", false
	}

	if kind == types.ScopeOrg {
		o, _, err := a.cfg.Tenancy.Org(r.Context(), me.ID, id)
		if err != nil {
			a.failed(w, r, err)

			return types.Scope{}, "", false
		}

		return o.Scope(), o.Name, true
	}

	acct, _, err := a.cfg.Tenancy.Account(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return types.Scope{}, "", false
	}

	if !acct.OrgID.Zero() {
		http.Redirect(w, r, pathOf(types.OrgScope(acct.OrgID)), http.StatusSeeOther)

		return types.Scope{}, "", false
	}

	return acct.Scope(), acct.Name, true
}

func (a app) list(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		owner, name, ok := a.owner(w, r, me, kind)
		if !ok {
			return
		}

		q := r.URL.Query()
		a.page(w, r, me, owner, name, http.StatusOK, listView{Done: q.Get("done"), Problem: q.Get("problem")})
	}
}

func (a app) page(w http.ResponseWriter, r *http.Request, me userbus.User, owner types.Scope, name string, status int, view listView) {
	list, access, err := a.cfg.Categories.List(r.Context(), me.ID, owner)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Path = pathOf(owner)
	view.OwnerName = name
	view.OwnerPath = strings.TrimSuffix(view.Path, "/categories")
	view.CanBookkeep = access.Can(tenancybus.Bookkeep)

	for _, c := range list {
		if c.Archived() {
			view.Archived = append(view.Archived, c)
		} else {
			view.Categories = append(view.Categories, c)
		}
	}

	a.cfg.Render.Render(w, r, status, "categories", view)
}

func (a app) create(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

			return
		}

		owner, ownerName, ok := a.owner(w, r, me, kind)
		if !ok {
			return
		}

		name := r.PostForm.Get("name")

		_, err := a.cfg.Categories.Create(r.Context(), a.cfg.Now(), me.ID, owner, name)
		if code := problem(err); code != "" {
			a.page(w, r, me, owner, ownerName, http.StatusUnprocessableEntity, listView{Name: name, Problem: code})

			return
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		http.Redirect(w, r, pathOf(owner)+"?done=added", http.StatusSeeOther)
	}
}

func (a app) rename(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (categorybus.Category, string, error) {
		c, err := a.cfg.Categories.Rename(r.Context(), a.cfg.Now(), me.ID, id, r.PostForm.Get("name"))

		return c, "renamed", err
	})
}

func (a app) archive(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (categorybus.Category, string, error) {
		archived := r.PostForm.Get("archived") == "1"

		c, err := a.cfg.Categories.SetArchived(r.Context(), a.cfg.Now(), me.ID, id, archived)

		done := "restored"
		if archived {
			done = "archived"
		}

		return c, done, err
	})
}

// change is the tail every change to one category shares: back to its list,
// saying what happened, or saying what was wrong with what was typed.
func (a app) change(w http.ResponseWriter, r *http.Request, do func(userbus.User, types.ID) (categorybus.Category, string, error)) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	c, done, err := do(me, id)

	if code := problem(err); code != "" && !c.ID.Zero() {
		// Back to the list it is on, which the actor could read or the
		// change would have been ErrNotFound.
		http.Redirect(w, r, pathOf(c.Owner)+"?problem="+url.QueryEscape(code), http.StatusSeeOther)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, pathOf(c.Owner)+"?done="+done, http.StatusSeeOther)
}
