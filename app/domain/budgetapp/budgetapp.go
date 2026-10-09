// Package budgetapp is the budget pages (docs/budgets.md): a project's
// budget, and an organization's budget year, each beside what happened,
// for anyone who may read it, with the form its owners set it with.
package budgetapp

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
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

// Years is where an organization's budget year is moved (tenancybus).
type Years interface {
	SetFiscalStart(ctx context.Context, now time.Time, actor, id types.ID, month int) (tenancybus.Org, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Budgets *budgetbus.Business
	Years   Years
	Render  Renderer

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

	handle("GET /projects/{id}/budget", a.project)
	handle("POST /projects/{id}/budget", a.setProject)
	handle("GET /orgs/{id}/budget", a.org)
	handle("POST /orgs/{id}/budget", a.setOrg)
	handle("POST /orgs/{id}/budget/copy", a.copyYear)
	handle("POST /orgs/{id}/budget/year-start", a.yearStart)
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
		a.cfg.Render.Render(w, r, http.StatusNotFound, "budget-missing", nil)

		return types.ID{}, false
	}

	return id, true
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, budgetbus.ErrNotFound):
		a.cfg.Render.Render(w, r, http.StatusNotFound, "budget-missing", nil)
	case errors.Is(err, budgetbus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "budget-refused", nil)
	default:
		a.cfg.Log.Error("a request about a budget failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "budget-refused", "server")
	}
}

// field is a form field's name for a line: "income-<id>", "expense-<id>",
// or "income-total" and "expense-total" for a kind's total.
func field(kind categorybus.Kind, category types.ID) string {
	if category.Zero() {
		return string(kind) + "-total"
	}

	return string(kind) + "-" + category.String()
}

type budgetView struct {
	Budget budgetbus.Comparison
	Path   string // the project's or the organization's page
	Action string // where the form is sent

	// An organization's year: the years either side, by name, and the
	// months its year may start in.
	Before, After         string
	BeforeYear, AfterYear int
	Months                []int

	// Form is the owner's form, when the reader is one.
	Form    budgetbus.Form
	CanSet  bool
	Typed   map[string]string // by field, what the form shows
	Problem string
	About   string // the line the problem is about, by name
	Done    string
}

// Field is a line's field name, for the template: a kind, and a
// category's ID or "" for the kind's total. Strings, because a template
// passes what it has.
func (budgetView) Field(kind, category string) string {
	id, _ := types.ParseID(category)

	return field(categorybus.Kind(kind), id)
}

func (a app) project(w http.ResponseWriter, r *http.Request) {
	a.projectPage(w, r, http.StatusOK, budgetView{Done: r.URL.Query().Get("done")})
}

func (a app) projectPage(w http.ResponseWriter, r *http.Request, status int, view budgetView) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	c, err := a.cfg.Budgets.Project(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Budget, view.Path, view.CanSet = c, "/projects/"+id.String(), c.CanSet()
	view.Action = view.Path + "/budget"

	if view.CanSet {
		if view.Form, err = a.cfg.Budgets.ProjectForm(r.Context(), me.ID, id); err != nil {
			a.failed(w, r, err)

			return
		}

		if view.Typed == nil {
			view.Typed = stored(c)
		}
	}

	a.cfg.Render.Render(w, r, status, "budget", view)
}

// stored is the form's fields for the budget as it is.
func stored(c budgetbus.Comparison) map[string]string {
	out := map[string]string{"currency": c.Currency}

	for _, side := range []budgetbus.Side{c.Income, c.Expenses} {
		if side.HasTyped {
			out[field(side.Kind, types.ID{})] = side.Typed.String()
		}

		for _, r := range side.Rows {
			out[field(side.Kind, r.CategoryID)] = r.Budget.String()
		}
	}

	return out
}

// posted reads the budget form: what was typed, by field, and the entries
// it makes. bad is the field whose amount could not be read, if one could
// not.
func posted(r *http.Request) (typed map[string]string, entries []budgetbus.Entry, bad string) {
	typed = map[string]string{"currency": r.PostForm.Get("currency")}

	for name := range r.PostForm {
		kind, rest, found := strings.Cut(name, "-")
		if !found || (kind != string(categorybus.Income) && kind != string(categorybus.Expense)) {
			continue
		}

		var category types.ID

		if rest != "total" {
			var err error
			if category, err = types.ParseID(rest); err != nil {
				continue
			}
		}

		text := strings.TrimSpace(r.PostForm.Get(name))
		typed[name] = text

		if text == "" {
			continue
		}

		amount, err := typedAmount(text)
		if err != nil {
			bad = name

			continue
		}

		entries = append(entries, budgetbus.Entry{CategoryID: category, Kind: categorybus.Kind(kind), Amount: amount})
	}

	return typed, entries, bad
}

// parsed reads the form, or answers that it could not be read.
func parsed(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return false
	}

	return true
}

// saved is the tail of saving a budget: back to its page saying so, or the
// page again with what was wrong.
func (a app) saved(w http.ResponseWriter, r *http.Request, err error, typed map[string]string, again func(int, budgetView), to string) {
	if invalid, ok := errors.AsType[budgetbus.Invalid](err); ok {
		again(http.StatusUnprocessableEntity, budgetView{Typed: typed, Problem: invalid.Field, About: field(invalid.Kind, invalid.CategoryID)})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (a app) setProject(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	again := func(status int, view budgetView) { a.projectPage(w, r, status, view) }

	typed, entries, bad := posted(r)
	if bad != "" {
		again(http.StatusUnprocessableEntity, budgetView{Typed: typed, Problem: "amount", About: bad})

		return
	}

	err := a.cfg.Budgets.SetProject(r.Context(), a.cfg.Now(), me.ID, id, typed["currency"], entries)
	a.saved(w, r, err, typed, again, "/projects/"+id.String()+"/budget?done=set")
}

// --- an organization's year ---------------------------------------------------

// yearOf is the year a request asks for: zero, the year today is in, when it
// names none. ok is false for one that is not a number.
func yearOf(r *http.Request) (int, bool) {
	s := r.URL.Query().Get("year")
	if s == "" {
		return 0, true
	}

	y, err := strconv.Atoi(s)

	return y, err == nil
}

func (a app) org(w http.ResponseWriter, r *http.Request) {
	a.orgPage(w, r, http.StatusOK, budgetView{Done: r.URL.Query().Get("done")})
}

func (a app) orgPage(w http.ResponseWriter, r *http.Request, status int, view budgetView) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	year, ok := yearOf(r)
	if !ok {
		a.failed(w, r, budgetbus.ErrNotFound)

		return
	}

	f, err := a.cfg.Budgets.OrgYear(r.Context(), a.cfg.Now(), me.ID, id, year)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	c := f.Comparison
	start := c.Org.FiscalStart

	view.Budget, view.Form, view.CanSet = c, f, c.CanSet()
	view.Path = "/orgs/" + id.String()
	view.Action = view.Path + "/budget?year=" + strconv.Itoa(c.Year)
	view.Before, view.After = budgetbus.Label(c.Year-1, start), budgetbus.Label(c.Year+1, start)
	view.BeforeYear, view.AfterYear = c.Year-1, c.Year+1
	view.Months = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

	if view.CanSet && view.Typed == nil {
		view.Typed = stored(c)
	}

	a.cfg.Render.Render(w, r, status, "org-budget", view)
}

func (a app) setOrg(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	year, ok := yearOf(r)
	if !ok || year == 0 {
		a.failed(w, r, budgetbus.ErrNotFound)

		return
	}

	again := func(status int, view budgetView) { a.orgPage(w, r, status, view) }

	typed, entries, bad := posted(r)
	if bad != "" {
		again(http.StatusUnprocessableEntity, budgetView{Typed: typed, Problem: "amount", About: bad})

		return
	}

	err := a.cfg.Budgets.SetOrgYear(r.Context(), a.cfg.Now(), me.ID, id, year, typed["currency"], entries)
	a.saved(w, r, err, typed, again, "/orgs/"+id.String()+"/budget?year="+strconv.Itoa(year)+"&done=set")
}

// copyYear is "Copy last year's budget".
func (a app) copyYear(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	year, ok := yearOf(r)
	if !ok || year == 0 {
		a.failed(w, r, budgetbus.ErrNotFound)

		return
	}

	back := "/orgs/" + id.String() + "/budget?year=" + strconv.Itoa(year)

	_, err := a.cfg.Budgets.CopyYear(r.Context(), a.cfg.Now(), me.ID, id, year)
	if errors.Is(err, budgetbus.ErrNothingToCopy) {
		http.Redirect(w, r, back+"&done=nothing-to-copy", http.StatusSeeOther)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, back+"&done=copied", http.StatusSeeOther)
}

// yearStart moves the month the organization's budget year starts in.
func (a app) yearStart(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	month, _ := strconv.Atoi(r.PostForm.Get("month"))

	_, err := a.cfg.Years.SetFiscalStart(r.Context(), a.cfg.Now(), me.ID, id, month)
	if _, invalid := errors.AsType[tenancybus.Invalid](err); invalid {
		http.Redirect(w, r, "/orgs/"+id.String()+"/budget?done=no-month", http.StatusSeeOther)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/orgs/"+id.String()+"/budget?done=year-start", http.StatusSeeOther)
}

// typedAmount reads an amount as a person types it, with a decimal comma
// or point and thousands either way. A copy of ledgerapp's few lines,
// because an app does not import another.
func typedAmount(s string) (money.Amount, error) {
	last := strings.LastIndexAny(s, ".,")
	comma := last >= 0 && s[last] == ',' && len(strings.TrimRight(s[last+1:], " ")) <= 2

	return csvsource.ParseAmount(s, comma)
}
