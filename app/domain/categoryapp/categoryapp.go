// Package categoryapp is the pages for category lists: an organization's,
// and an account's on its own.
//
// An account in an organization has no list of its own; its address here
// sends the reader on to the organization's.
package categoryapp

import (
	"context"
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
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
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

	// Ledger adds up what should come back to zero. Nil shows nothing.
	Ledger Settler

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Settler is the part of ledgerbus this app uses.
type Settler interface {
	Settling(ctx context.Context, actor types.ID, owner types.Scope) (ledgerbus.Settling, error)
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
	handle("POST /categories/{id}/kind", a.setKind)
	handle("POST /categories/{id}/group", a.setGroup)

	handle("POST /orgs/{id}/categories/groups", a.addGroup(types.ScopeOrg))
	handle("POST /accounts/{id}/categories/groups", a.addGroup(types.ScopeAccount))
	handle("POST /category-groups/{id}/rename", a.renameGroup)
	handle("POST /category-groups/{id}/archive", a.archiveGroup)
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
	if invalid, ok := errors.AsType[tenancybus.Invalid](err); ok {
		switch invalid.Field {
		case "category-kind":
			return "kind"
		case "group-name", "group-kind", "group-archived":
			return invalid.Field
		}

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

	// CanManage is the owner, who alone says what kind a category is.
	CanManage bool

	// Unsaid is the categories made before kinds, listed first so that
	// the owner says what each is; Groups the rest, a kind at a time.
	Unsaid   []categorybus.Category
	Groups   []group
	Archived []categorybus.Category
	Kinds    []categorybus.Kind

	// ArchivedHeadings is the groups off the choices and holding nothing
	// still in use; one that holds a category is shown over it instead.
	ArchivedHeadings []categorybus.Heading

	// Settling is the balances that should come back to zero: each
	// pass-through category's, and the transfers across the accounts.
	Settling ledgerbus.Settling

	// Typed into the add form, kept when it is refused.
	Name    string
	Kind    categorybus.Kind
	Problem string
	Done    string
}

// group is the categories of one kind: under the groups (headings) a list
// has for reading its reports, and under none.
type group struct {
	Kind       categorybus.Kind
	Categories []categorybus.Category // under no group
	Shelves    []shelf

	// Headings is the groups a category of the kind may be put under:
	// the ones not archived.
	Headings []categorybus.Heading
}

// Groupable reports whether the kind's categories can have groups worth
// reading: a report's rows are income and expenses.
func (g group) Groupable() bool {
	return g.Kind == categorybus.Income || g.Kind == categorybus.Expense
}

// shelf is a heading and the categories under it.
type shelf struct {
	Heading    categorybus.Heading
	Categories []categorybus.Category
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
	view.CanManage = access.Can(tenancybus.Manage)
	view.Kinds = categorybus.Kinds

	if view.Kind == "" {
		view.Kind = categorybus.Expense
	}

	headings, err := a.cfg.Categories.Headings(r.Context(), me.ID, owner)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	byKind := map[categorybus.Kind][]categorybus.Category{}
	under := map[types.ID][]categorybus.Category{}

	for _, c := range list {
		switch {
		case c.Archived():
			view.Archived = append(view.Archived, c)
		case c.Kind == categorybus.Unsaid:
			view.Unsaid = append(view.Unsaid, c)
		case !c.GroupID.Zero():
			under[c.GroupID] = append(under[c.GroupID], c)
		default:
			byKind[c.Kind] = append(byKind[c.Kind], c)
		}
	}

	for _, k := range categorybus.Kinds {
		g := group{Kind: k, Categories: byKind[k]}

		for _, h := range headings {
			if h.Kind != k {
				continue
			}

			if !h.Archived() {
				g.Headings = append(g.Headings, h)
			}

			switch {
			case len(under[h.ID]) > 0 || !h.Archived():
				g.Shelves = append(g.Shelves, shelf{Heading: h, Categories: under[h.ID]})
			default:
				view.ArchivedHeadings = append(view.ArchivedHeadings, h)
			}
		}

		if len(g.Categories) > 0 || len(g.Shelves) > 0 {
			view.Groups = append(view.Groups, g)
		}
	}

	if a.cfg.Ledger != nil {
		// Not worth failing the page for: its job is the list.
		if view.Settling, err = a.cfg.Ledger.Settling(r.Context(), me.ID, owner); err != nil {
			a.cfg.Log.Error("what should come back to zero could not be added up", "request_id", web.RequestIDFrom(r.Context()), "error", err)
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

		name, kind := r.PostForm.Get("name"), categorybus.Kind(r.PostForm.Get("kind"))

		_, err := a.cfg.Categories.Create(r.Context(), a.cfg.Now(), me.ID, owner, name, kind)
		if code := problem(err); code != "" {
			a.page(w, r, me, owner, ownerName, http.StatusUnprocessableEntity, listView{Name: name, Kind: kind, Problem: code})

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

func (a app) setKind(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (categorybus.Category, string, error) {
		c, err := a.cfg.Categories.SetKind(r.Context(), a.cfg.Now(), me.ID, id, categorybus.Kind(r.PostForm.Get("kind")))

		return c, "kind", err
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

// setGroup puts a category under a group of its list, or under none when
// the form's group is empty.
func (a app) setGroup(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (categorybus.Category, string, error) {
		var heading types.ID

		if s := r.PostForm.Get("group"); s != "" {
			var err error
			if heading, err = types.ParseID(s); err != nil {
				return categorybus.Category{}, "", categorybus.ErrNotFound
			}
		}

		c, err := a.cfg.Categories.SetHeading(r.Context(), a.cfg.Now(), me.ID, id, heading)

		done := "grouped"
		if heading.Zero() {
			done = "ungrouped"
		}

		return c, done, err
	})
}

// --- groups ------------------------------------------------------------------------

func (a app) addGroup(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

			return
		}

		owner, _, ok := a.owner(w, r, me, kind)
		if !ok {
			return
		}

		_, err := a.cfg.Categories.AddHeading(r.Context(), a.cfg.Now(), me.ID, owner, r.PostForm.Get("name"), categorybus.Kind(r.PostForm.Get("kind")))

		switch code := problem(err); {
		case errors.Is(err, categorybus.ErrDuplicate):
			http.Redirect(w, r, pathOf(owner)+"?problem=group-duplicate", http.StatusSeeOther)
		case code != "":
			http.Redirect(w, r, pathOf(owner)+"?problem="+url.QueryEscape(code), http.StatusSeeOther)
		case err != nil:
			a.failed(w, r, err)
		default:
			http.Redirect(w, r, pathOf(owner)+"?done=group-added", http.StatusSeeOther)
		}
	}
}

func (a app) renameGroup(w http.ResponseWriter, r *http.Request) {
	a.changeGroup(w, r, func(me userbus.User, id types.ID) (categorybus.Heading, string, error) {
		h, err := a.cfg.Categories.RenameHeading(r.Context(), a.cfg.Now(), me.ID, id, r.PostForm.Get("name"))

		return h, "group-renamed", err
	})
}

func (a app) archiveGroup(w http.ResponseWriter, r *http.Request) {
	a.changeGroup(w, r, func(me userbus.User, id types.ID) (categorybus.Heading, string, error) {
		archived := r.PostForm.Get("archived") == "1"

		h, err := a.cfg.Categories.SetHeadingArchived(r.Context(), a.cfg.Now(), me.ID, id, archived)

		done := "group-restored"
		if archived {
			done = "group-archived"
		}

		return h, done, err
	})
}

// changeGroup is change, for a group.
func (a app) changeGroup(w http.ResponseWriter, r *http.Request, do func(userbus.User, types.ID) (categorybus.Heading, string, error)) {
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

	h, done, err := do(me, id)

	switch code := problem(err); {
	case errors.Is(err, categorybus.ErrDuplicate) && !h.ID.Zero():
		http.Redirect(w, r, pathOf(h.Owner)+"?problem=group-duplicate", http.StatusSeeOther)
	case code != "" && !h.ID.Zero():
		http.Redirect(w, r, pathOf(h.Owner)+"?problem="+url.QueryEscape(code), http.StatusSeeOther)
	case err != nil:
		a.failed(w, r, err)
	default:
		http.Redirect(w, r, pathOf(h.Owner)+"?done="+done, http.StatusSeeOther)
	}
}
