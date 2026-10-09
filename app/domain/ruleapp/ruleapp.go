// Package ruleapp is an account's sorting rules page (docs/sorting.md):
// each rule with what it sorts into, how much it sorted and why it sorts
// nothing if it does not; making, changing and removing one; and "Sort what
// is not sorted yet".
//
// The rules themselves are rulebus's, and what they did and would do is
// the ledger's (ledgerbus.Rulebook), which is where the transactions are.
// A rule is also made from a transaction's own page, by the "always sort
// charges like this" box; that is ledgerapp's.
package ruleapp

import (
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
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
	Rules      *rulebus.Business
	Ledger     *ledgerbus.Business
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

	handle("GET /accounts/{id}/rules", a.list)
	handle("POST /accounts/{id}/rules", a.create)
	handle("POST /accounts/{id}/rules/apply", a.apply)
	handle("POST /rules/{id}", a.change)
	handle("POST /rules/{id}/remove", a.remove)
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
		a.cfg.Render.Render(w, r, http.StatusNotFound, "rules-missing", nil)

		return types.ID{}, false
	}

	return id, true
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, rulebus.ErrNotFound):
		a.cfg.Render.Render(w, r, http.StatusNotFound, "rules-missing", nil)
	case errors.Is(err, rulebus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "rules-refused", nil)
	default:
		a.cfg.Log.Error("a request about sorting rules failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "rules-refused", "server")
	}
}

// parsed reads the form, or answers that it could not be read.
func parsed(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return false
	}

	return true
}

func pathOf(account types.ID) string { return "/accounts/" + account.String() + "/rules" }

// ruleForm is a rule as typed.
type ruleForm struct {
	Match      string
	Direction  rulebus.Direction
	CategoryID string
	ProjectID  string
}

func formOf(r *http.Request) ruleForm {
	return ruleForm{
		Match:      strings.TrimSpace(r.PostForm.Get("match")),
		Direction:  rulebus.Direction(r.PostForm.Get("direction")),
		CategoryID: r.PostForm.Get("category"),
		ProjectID:  r.PostForm.Get("project"),
	}
}

// fields is the form for rulebus. An ID that does not parse is no choice
// at all, and the business says so if that matters.
func (f ruleForm) fields() rulebus.Fields {
	category, _ := types.ParseID(f.CategoryID)
	project, _ := types.ParseID(f.ProjectID)

	return rulebus.Fields{Match: f.Match, Direction: f.Direction, CategoryID: category, ProjectID: project}
}

// problem is the code a page words for an error with what was typed.
func problem(err error) string {
	if invalid, ok := errors.AsType[rulebus.Invalid](err); ok {
		return invalid.Field
	}

	if errors.Is(err, rulebus.ErrDuplicate) {
		return "duplicate"
	}

	return ""
}

type listView struct {
	Book ledgerbus.Rulebook
	Path string // the account's page

	// The choices for a rule: the account's list a kind at a time, without
	// the archived, and the projects the reader keeps the books of.
	Groups     []categorybus.Group
	Projects   []tenancybus.Project
	Directions []rulebus.Direction

	// Names for what the rules point at, archived and out of reach
	// included, so that a rule with a problem still says what it was.
	CategoryNames map[types.ID]string
	ProjectNames  map[types.ID]string

	Form    ruleForm
	Problem string
	Done    string
	Sorted  int
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))

	a.page(w, r, me.ID, id, http.StatusOK, listView{
		Form:    ruleForm{Direction: rulebus.Out},
		Problem: q.Get("problem"),
		Done:    q.Get("done"),
		Sorted:  n,
	})
}

func (a app) page(w http.ResponseWriter, r *http.Request, me, accountID types.ID, status int, view listView) {
	ctx := r.Context()

	book, err := a.cfg.Ledger.Rulebook(ctx, a.cfg.Now(), me, accountID)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Book = book
	view.Path = "/accounts/" + accountID.String()
	view.Directions = rulebus.Directions

	cats, err := a.cfg.Categories.ForAccount(ctx, book.Account)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.CategoryNames = make(map[types.ID]string, len(cats))

	var open []categorybus.Category

	for _, c := range cats {
		view.CategoryNames[c.ID] = c.Name

		if !c.Archived() {
			open = append(open, c)
		}
	}

	view.Groups = categorybus.Grouped(open)

	mine, err := a.cfg.Tenancy.ProjectsFor(ctx, me, tenancybus.Bookkeep)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	for _, p := range mine {
		if !p.Archived() {
			view.Projects = append(view.Projects, p)
		}
	}

	// The names of the projects the rules point at: the reader keeps the
	// account's books, and where its money goes is part of it
	// (tenancybus.ProjectNames).
	var pointed []types.ID

	for _, u := range book.Rules {
		if !u.Rule.ProjectID.Zero() {
			pointed = append(pointed, u.Rule.ProjectID)
		}
	}

	if view.ProjectNames, err = a.cfg.Tenancy.ProjectNames(ctx, pointed); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, status, "rules", view)
}

func (a app) create(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	f := formOf(r)

	_, err := a.cfg.Rules.Save(r.Context(), a.cfg.Now(), me.ID, id, f.fields())
	if code := problem(err); code != "" {
		a.page(w, r, me.ID, id, http.StatusUnprocessableEntity, listView{Form: f, Problem: code})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, pathOf(id)+"?done=made", http.StatusSeeOther)
}

func (a app) change(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	rule, err := a.cfg.Rules.Change(r.Context(), a.cfg.Now(), me.ID, id, formOf(r).fields())
	if code := problem(err); code != "" && !rule.AccountID.Zero() {
		// Back to the rules it is among, which the actor keeps the books
		// of, or the change would have been refused before it was checked.
		http.Redirect(w, r, pathOf(rule.AccountID)+"?problem="+url.QueryEscape(code), http.StatusSeeOther)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, pathOf(rule.AccountID)+"?done=changed", http.StatusSeeOther)
}

func (a app) remove(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	rule, err := a.cfg.Rules.Remove(r.Context(), a.cfg.Now(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, pathOf(rule.AccountID)+"?done=removed", http.StatusSeeOther)
}

// apply is "Sort what is not sorted yet".
func (a app) apply(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	n, err := a.cfg.Ledger.SortUnsorted(r.Context(), a.cfg.Now(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, pathOf(id)+"?done=applied&n="+strconv.Itoa(n), http.StatusSeeOther)
}
