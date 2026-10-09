// Package budgetapp is the budget pages (docs/budgets.md): a project's
// budget beside what happened, for anyone who may read the project, and
// the form its owners set it with.
package budgetapp

import (
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
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

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Budgets *budgetbus.Business
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

	mux.Handle("GET /projects/{id}/budget", guard(http.HandlerFunc(a.project)))
	mux.Handle("POST /projects/{id}/budget", guard(http.HandlerFunc(a.setProject)))
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
	Path   string // the project's page

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

func (a app) setProject(w http.ResponseWriter, r *http.Request) {
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

	typed := map[string]string{"currency": r.PostForm.Get("currency")}

	var entries []budgetbus.Entry

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
			a.projectPage(w, r, http.StatusUnprocessableEntity, budgetView{Typed: typed, Problem: "amount", About: name})

			return
		}

		entries = append(entries, budgetbus.Entry{CategoryID: category, Kind: categorybus.Kind(kind), Amount: amount})
	}

	err := a.cfg.Budgets.SetProject(r.Context(), a.cfg.Now(), me.ID, id, typed["currency"], entries)
	if invalid, ok := errors.AsType[budgetbus.Invalid](err); ok {
		a.projectPage(w, r, http.StatusUnprocessableEntity, budgetView{Typed: typed, Problem: invalid.Field, About: field(invalid.Kind, invalid.CategoryID)})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/projects/"+id.String()+"/budget?done=set", http.StatusSeeOther)
}

// typedAmount reads an amount as a person types it, with a decimal comma
// or point and thousands either way. A copy of ledgerapp's few lines,
// because an app does not import another.
func typedAmount(s string) (money.Amount, error) {
	last := strings.LastIndexAny(s, ".,")
	comma := last >= 0 && s[last] == ',' && len(strings.TrimRight(s[last+1:], " ")) <= 2

	return csvsource.ParseAmount(s, comma)
}
