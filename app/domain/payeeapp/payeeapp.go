// Package payeeapp is the pages for lists of payees (docs/payees.md): an
// organization's, and an account's with no organization; each payee's page,
// with its spellings and payments; and joining, splitting off and the
// suggestions of which payees may be one.
//
// An account in an organization has no list of its own; its address here
// sends the reader on to the organization's, as categories do.
package payeeapp

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
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates text
var files embed.FS

// Templates is this app's pages and texts, for page.NewRenderer.
var Templates fs.FS = files

// Renderer is the part of page.Renderer this app uses: pages, and the
// texts a spreadsheet's header row is written in.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
	Text(lang types.Lang, name string, data any) (string, error)
}

// Questions is the questions about a thing, for its page (questionbus).
type Questions interface {
	Summary(ctx context.Context, actor types.ID, about questionbus.About) (questionbus.Summary, error)
}

// Config is what this app needs.
type Config struct {
	Log      *slog.Logger
	Ledger   *ledgerbus.Business
	Tenancy  *tenancybus.Business
	Receipts Receipts
	Render   Renderer

	// Questions is the questions about a payee, for its page. Nil shows
	// none.
	Questions Questions

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type app struct {
	cfg Config
}

// paymentsShown is how many of a payee's payments its page lists, newest
// first.
const paymentsShown = 50

// Routes mounts this app behind guard, which is mid.Require.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, guard(h)) }

	handle("GET /orgs/{id}/payees", a.list(types.ScopeOrg))
	handle("POST /orgs/{id}/payees", a.create(types.ScopeOrg))
	handle("GET /accounts/{id}/payees", a.list(types.ScopeAccount))
	handle("POST /accounts/{id}/payees", a.create(types.ScopeAccount))
	handle("GET /payees/{id}", a.payee)
	handle("POST /payees/{id}/rename", a.rename)
	handle("POST /payees/{id}/archive", a.archive)
	handle("POST /payees/{id}/join", a.join)
	handle("POST /payees/{id}/split", a.split)
	handle("POST /payees/{id}/not-same", a.notSame)
	handle("POST /payees/{id}/marks", a.marks)

	handle("GET /orgs/{id}/payees/{year}", a.yearPage(types.ScopeOrg))
	handle("GET /accounts/{id}/payees/{year}", a.yearPage(types.ScopeAccount))
	handle("GET /orgs/{id}/payees/{year}/download", a.yearDownload(types.ScopeOrg))
	handle("GET /accounts/{id}/payees/{year}/download", a.yearDownload(types.ScopeAccount))
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
		a.cfg.Render.Render(w, r, http.StatusNotFound, "payees-missing", nil)

		return types.ID{}, false
	}

	return id, true
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ledgerbus.ErrNotFound):
		a.cfg.Render.Render(w, r, http.StatusNotFound, "payees-missing", nil)
	case errors.Is(err, ledgerbus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "payees-refused", nil)
	default:
		a.cfg.Log.Error("a request about payees failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "payees-refused", "server")
	}
}

// problem is the code a page words for a change refused for what was
// asked, or "" for anything else.
func problem(err error) string {
	switch {
	case errors.Is(err, ledgerbus.ErrPayeeName):
		return "name"
	case errors.Is(err, ledgerbus.ErrPayeeTaken):
		return "taken"
	case errors.Is(err, ledgerbus.ErrJoinSelf):
		return "self"
	case errors.Is(err, ledgerbus.ErrJoinLists):
		return "lists"
	case errors.Is(err, ledgerbus.ErrJoinArchived):
		return "archived"
	case errors.Is(err, ledgerbus.ErrSplitName):
		return "split-name"
	case errors.Is(err, ledgerbus.ErrPayeeNote):
		return "note"
	}

	return ""
}

// pathOf is the list page of an owner.
func pathOf(owner types.Scope) string {
	if owner.Kind == types.ScopeOrg {
		return "/orgs/" + owner.ID.String() + "/payees"
	}

	return "/accounts/" + owner.ID.String() + "/payees"
}

// ownerOf is the scope a list path names and its name. An account in an
// organization is sent to the organization's list, or the same page of
// it: its year, or the year's download.
func (a app) ownerOf(w http.ResponseWriter, r *http.Request, me userbus.User, kind types.ScopeKind) (types.Scope, string, bool) {
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
		rest := strings.TrimPrefix(r.URL.Path, pathOf(acct.Scope()))
		http.Redirect(w, r, pathOf(types.OrgScope(acct.OrgID))+rest, http.StatusSeeOther)

		return types.Scope{}, "", false
	}

	return acct.Scope(), acct.Name, true
}

// ownerName is the name of a list's owner, for a payee's page, read after
// the payee's list has been asked of: whoever may read the list may read
// its owner.
func (a app) ownerName(r *http.Request, me userbus.User, owner types.Scope) string {
	if owner.Kind == types.ScopeOrg {
		if o, _, err := a.cfg.Tenancy.Org(r.Context(), me.ID, owner.ID); err == nil {
			return o.Name
		}

		return ""
	}

	if acct, _, err := a.cfg.Tenancy.Account(r.Context(), me.ID, owner.ID); err == nil {
		return acct.Name
	}

	return ""
}

// --- a list ----------------------------------------------------------------

type listView struct {
	Path        string
	OwnerName   string
	OwnerPath   string
	CanBookkeep bool

	Payees      []ledgerbus.Payee
	Archived    []ledgerbus.Payee
	Suggestions []ledgerbus.JoinSuggestion

	// ThisYear is the calendar year now, and LastYear the one before, for
	// the payments by payee.
	ThisYear, LastYear int

	// Typed into the add form, kept when it is refused.
	Name    string
	Problem string
	Done    string
}

func (a app) list(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		owner, name, ok := a.ownerOf(w, r, me, kind)
		if !ok {
			return
		}

		q := r.URL.Query()
		a.listPage(w, r, me, owner, name, http.StatusOK, listView{Done: q.Get("done"), Problem: q.Get("problem")})
	}
}

func (a app) listPage(w http.ResponseWriter, r *http.Request, me userbus.User, owner types.Scope, name string, status int, view listView) {
	list, access, err := a.cfg.Ledger.Payees(r.Context(), me.ID, owner)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view.Path = pathOf(owner)
	view.OwnerName = name
	view.OwnerPath = strings.TrimSuffix(view.Path, "/payees")
	view.CanBookkeep = access.Can(tenancybus.Bookkeep)
	view.ThisYear = a.cfg.Now().Year()
	view.LastYear = view.ThisYear - 1

	for _, p := range list {
		if p.Archived() {
			view.Archived = append(view.Archived, p)
		} else {
			view.Payees = append(view.Payees, p)
		}
	}

	if view.Suggestions, err = a.cfg.Ledger.PayeeSuggestions(r.Context(), me.ID, owner); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, status, "payees", view)
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

		owner, ownerName, ok := a.ownerOf(w, r, me, kind)
		if !ok {
			return
		}

		name := r.PostForm.Get("name")

		p, err := a.cfg.Ledger.CreatePayee(r.Context(), a.cfg.Now(), me.ID, owner, name)
		if code := problem(err); code != "" {
			a.listPage(w, r, me, owner, ownerName, http.StatusUnprocessableEntity, listView{Name: name, Problem: code})

			return
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		http.Redirect(w, r, "/payees/"+p.ID.String()+"?done=added", http.StatusSeeOther)
	}
}

// --- a payee ---------------------------------------------------------------

type payeeView struct {
	Payee       ledgerbus.Payee
	Questions   questionbus.Summary
	ListPath    string
	OwnerPath   string
	OwnerName   string
	CanBookkeep bool

	// Payments is its most recent transactions, More whether it has
	// others.
	Payments []ledgerbus.PayeePayment
	More     bool

	// Others is the rest of the list, to join into this one or split a
	// spelling off into.
	Others []ledgerbus.Payee

	// Brought is the spellings a program brought into it through a key,
	// each marked until a person saves the payee (Payee.BroughtVia).
	Brought map[string]bool

	Problem string
	Done    string
}

// PageScope is whose list the payee is on, for the top bar.
func (v payeeView) PageScope() types.Scope { return v.Payee.Owner }

func (a app) payee(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	p, access, err := a.cfg.Ledger.Payee(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	// A payee joined into another is that one now.
	if p.Joined() {
		http.Redirect(w, r, "/payees/"+p.JoinedInto.String(), http.StatusSeeOther)

		return
	}

	payments, err := a.cfg.Ledger.PayeePayments(r.Context(), me.ID, id, paymentsShown+1)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	list, _, err := a.cfg.Ledger.Payees(r.Context(), me.ID, p.Owner)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	q := r.URL.Query()
	view := payeeView{
		Payee:       p,
		ListPath:    pathOf(p.Owner),
		OwnerPath:   page.ScopePath(p.Owner),
		OwnerName:   a.ownerName(r, me, p.Owner),
		CanBookkeep: access.Can(tenancybus.Bookkeep),
		Payments:    payments[:min(len(payments), paymentsShown)],
		More:        len(payments) > paymentsShown,
		Brought:     map[string]bool{},
		Problem:     q.Get("problem"),
		Done:        q.Get("done"),
	}

	for _, s := range p.BroughtVia {
		view.Brought[s] = true
	}

	if a.cfg.Questions != nil {
		if view.Questions, err = a.cfg.Questions.Summary(r.Context(), me.ID, questionbus.About{Kind: questionbus.OnPayee, ID: p.ID}); err != nil {
			a.failed(w, r, err)

			return
		}
	}

	for _, o := range list {
		if o.ID != p.ID && !o.Archived() {
			view.Others = append(view.Others, o)
		}
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "payee", view)
}

// change is the tail every change from a payee's page shares: back to the
// payee it left the reader on, saying what happened, or saying what was
// wrong with what was asked.
func (a app) change(w http.ResponseWriter, r *http.Request, do func(userbus.User, types.ID) (ledgerbus.Payee, string, error)) {
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

	p, done, err := do(me, id)

	if code := problem(err); code != "" {
		// Back to the payee asked about, which the actor could read or
		// the change would have been ErrNotFound.
		http.Redirect(w, r, "/payees/"+id.String()+"?problem="+url.QueryEscape(code), http.StatusSeeOther)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	to := "/payees/" + p.ID.String()
	if back := r.PostForm.Get("back"); back == "list" {
		to = pathOf(p.Owner)
	}

	http.Redirect(w, r, to+"?done="+done, http.StatusSeeOther)
}

func (a app) rename(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (ledgerbus.Payee, string, error) {
		p, err := a.cfg.Ledger.RenamePayee(r.Context(), a.cfg.Now(), me.ID, id, r.PostForm.Get("name"))

		// The form sends the name it showed: saved as it was, it is a
		// person saying they have looked, not a new name.
		if strings.Join(strings.Fields(r.PostForm.Get("name")), " ") == r.PostForm.Get("was") {
			return p, "saved", err
		}

		return p, "renamed", err
	})
}

func (a app) archive(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (ledgerbus.Payee, string, error) {
		archived := r.PostForm.Get("archived") == "1"

		p, err := a.cfg.Ledger.SetPayeeArchived(r.Context(), a.cfg.Now(), me.ID, id, archived)

		done := "restored"
		if archived {
			done = "archived"
		}

		return p, done, err
	})
}

// formIDs is the IDs a form sent under a name, any that are not one
// answering ErrNotFound, as a payee nobody has would.
func formIDs(values []string) ([]types.ID, error) {
	var out []types.ID

	for _, v := range values {
		id, err := types.ParseID(v)
		if err != nil {
			return nil, ledgerbus.ErrNotFound
		}

		out = append(out, id)
	}

	return out, nil
}

// join puts the payees chosen, by "from", into the one whose page it is.
func (a app) join(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (ledgerbus.Payee, string, error) {
		from, err := formIDs(r.PostForm["from"])
		if err != nil {
			return ledgerbus.Payee{}, "", err
		}

		p, err := a.cfg.Ledger.JoinPayees(r.Context(), a.cfg.Now(), me.ID, id, from)

		return p, "joined", err
	})
}

// split takes a spelling off the payee whose page it is: into the payee
// chosen, or one of its own.
func (a app) split(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (ledgerbus.Payee, string, error) {
		var into types.ID

		if v := r.PostForm.Get("into"); v != "" {
			ids, err := formIDs([]string{v})
			if err != nil {
				return ledgerbus.Payee{}, "", err
			}

			into = ids[0]
		}

		p, err := a.cfg.Ledger.SplitPayee(r.Context(), a.cfg.Now(), me.ID, id, r.PostForm.Get("spelling"), into)

		return p, "split", err
	})
}

// notSame says the payee whose page it is and "other" are two people, so
// that the list stops suggesting them as one.
func (a app) notSame(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, func(me userbus.User, id types.ID) (ledgerbus.Payee, string, error) {
		others, err := formIDs([]string{r.PostForm.Get("other")})
		if err != nil {
			return ledgerbus.Payee{}, "", err
		}

		if err := a.cfg.Ledger.NotTheSame(r.Context(), a.cfg.Now(), me.ID, id, others[0]); err != nil {
			return ledgerbus.Payee{}, "", err
		}

		p, _, err := a.cfg.Ledger.Payee(r.Context(), me.ID, id)

		return p, "not-same", err
	})
}
