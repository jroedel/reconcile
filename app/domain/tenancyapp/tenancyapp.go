// Package tenancyapp is the pages for organizations, accounts and projects:
// making them, changing them, putting them away, and deciding who else may
// see them.
//
// Every route is behind sign-in, and every handler asks tenancybus, which
// answers ErrNotFound for anything the reader holds no role on. That is
// rendered as the same 404 as an address that never existed, so a page
// never confirms that somebody else's organization is there.
package tenancyapp

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates mail text
var files embed.FS

// Templates is this app's pages and messages, for page.NewRenderer.
var Templates fs.FS = files

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
	Mail(lang types.Lang, name string, data any) (subject, body string, err error)
	Text(lang types.Lang, name string, data any) (string, error)
}

// Starter is the part of categorybus this app uses: giving a new list the
// categories it begins with.
type Starter interface {
	Start(ctx context.Context, now time.Time, actor types.ID, owner types.Scope, starters []categorybus.Starter) error
}

// HolderOption is the part of ledgerbus this app uses: whether an
// account's statements arrive one file per holder (docs/clearing.md, 3).
// The option is the ledger's, and is changed on the account's
// transactions page, where its holders are; the settings link there,
// because an owner looks for an account's options among its settings
// (issue #81).
type HolderOption interface {
	ByHolder(ctx context.Context, actor, accountID types.ID) (bool, error)
}

// Users is the part of userbus this app uses: names for the people lists
// and the history.
type Users interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Tenancy *tenancybus.Business
	History *eventbus.Business
	Users   Users
	Render  Renderer

	// Categories starts the category list of a new organization or
	// personal account. Nil starts none.
	Categories Starter

	// Holders says whether an account's statements arrive one file per
	// holder, for the account's settings to say so beside its other
	// settings. Nil says nothing.
	Holders HolderOption

	// Mail may be nil: an invitation is then logged as not sent, and the
	// grant is made all the same -- the person can still sign in.
	Mail    mail.Sender
	BaseURL string

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// historyShown is how many lines of history a page shows.
const historyShown = 20

type app struct {
	cfg Config
}

// Routes mounts this app. guard is mid.Require: there is nothing here for
// somebody who is not signed in.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, guard(h)) }

	handle("GET /orgs/new", a.newOrg)
	handle("POST /orgs", a.createOrg)
	handle("GET /orgs/{id}", a.org)
	handle("POST /orgs/{id}/rename", a.renameOrg)
	handle("POST /orgs/{id}/archive", a.archiveOrg)

	handle("GET /accounts/new", a.newAccount)
	handle("POST /accounts", a.createAccount)
	handle("GET /accounts/{id}", a.account)
	handle("POST /accounts/{id}/edit", a.editAccount)
	handle("POST /accounts/{id}/archive", a.archiveAccount)

	handle("GET /projects/new", a.newProject)
	handle("POST /projects", a.createProject)
	handle("GET /projects/{id}", a.project)
	handle("POST /projects/{id}/edit", a.editProject)
	handle("POST /projects/{id}/archive", a.archiveProject)

	// Who may see it, the same three ways on each kind of scope.
	for _, k := range kinds {
		handle("POST /"+k.path+"/{id}/people", a.addPerson(k))
		handle("POST /"+k.path+"/{id}/people/{grant}/role", a.changeRole(k))
		handle("POST /"+k.path+"/{id}/people/{grant}/remove", a.removePerson(k))
	}
}

// kind ties a scope kind to its path and its page.
type kind struct {
	scope types.ScopeKind
	path  string
}

var kinds = []kind{
	{types.ScopeOrg, "orgs"},
	{types.ScopeAccount, "accounts"},
	{types.ScopeProject, "projects"},
}

// --- the shared tail --------------------------------------------------------

// actor is the signed-in user. The guard makes it always there; answered
// rather than assumed, so a route mounted without it fails closed.
func actor(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
	}

	return u, ok
}

// pathID is the {id} in the path, or a 404 for one that is not an ID.
func (a app) pathID(w http.ResponseWriter, r *http.Request, name string) (types.ID, bool) {
	id, err := types.ParseID(r.PathValue(name))
	if err != nil {
		a.missing(w, r)

		return types.ID{}, false
	}

	return id, true
}

func (a app) missing(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusNotFound, "missing", nil)
}

// failed answers an error from tenancybus that is not a problem with what
// was typed: nothing there, not allowed, or broken.
func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, tenancybus.ErrNotFound):
		a.missing(w, r)
	case errors.Is(err, tenancybus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "refused", nil)
	default:
		a.cfg.Log.Error("a request about an organization failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "refused", "server")
	}
}

// problem is the code a page words for an error with what was typed, or ""
// when the error is not one.
func problem(err error) string {
	if invalid, ok := errors.AsType[tenancybus.Invalid](err); ok {
		return invalid.Field
	}

	switch {
	case errors.Is(err, tenancybus.ErrLastOwner):
		return "last-owner"
	case errors.Is(err, tenancybus.ErrAlreadyGranted):
		return "already-granted"
	}

	return ""
}

// form parses a posted form, answering 400 itself when it cannot.
func form(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return false
	}

	return true
}

// back sends somebody to a page after a change, saying what happened.
func back(w http.ResponseWriter, r *http.Request, to, done string) {
	if done != "" {
		to += "?done=" + url.QueryEscape(done)
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// --- the scope pages' shared parts ------------------------------------------

// scopeView is what every scope page has: who may see it, what happened to
// it, and what the reader may do.
type scopeView struct {
	Path   string
	Kind   types.ScopeKind
	Access tenancybus.Access

	CanManage   bool
	CanBookkeep bool
	CanReceipts bool

	People    []person
	Inherited []person
	History   []line
	Roles     []tenancybus.Role

	Problem string
	Done    string

	// Typed into the people form, kept when it is refused.
	Email string
	Role  tenancybus.Role
}

// person is one line of a people list.
type person struct {
	GrantID types.ID
	Name    string
	Email   string
	Role    tenancybus.Role
	Pending bool
	You     bool
}

// line is one line of history.
type line struct {
	At     string
	Who    string
	Action eventbus.Action
	Detail map[string]string

	// Via is the name of the API key the change came through, "" for one
	// made on a page (eventbus.Event.Via).
	Via string
}

// names looks people up once each, for a page that names the same few many
// times.
type names struct {
	users Users
	ctx   context.Context
	seen  map[types.ID]userbus.User
}

func (n *names) of(id types.ID) userbus.User {
	if u, ok := n.seen[id]; ok {
		return u
	}

	u, err := n.users.ByID(n.ctx, id)
	if err != nil {
		u = userbus.User{ID: id}
	}

	n.seen[id] = u

	return u
}

// scope fills the shared parts. A failure to read the history or the people
// is not a failure of the page: it is logged, and the page shows less.
func (a app) scope(r *http.Request, me userbus.User, scope types.Scope, path string, access tenancybus.Access) scopeView {
	ctx := r.Context()
	n := &names{users: a.cfg.Users, ctx: ctx, seen: map[types.ID]userbus.User{}}

	view := scopeView{
		Path:        path,
		Kind:        scope.Kind,
		Access:      access,
		CanManage:   access.Can(tenancybus.Manage),
		CanBookkeep: access.Can(tenancybus.Bookkeep),
		CanReceipts: access.Can(tenancybus.Receipts),
		Roles:       tenancybus.Roles,
		Done:        r.URL.Query().Get("done"),
		Role:        tenancybus.Viewer,
	}

	toPerson := func(g tenancybus.Grant) person {
		p := person{GrantID: g.ID, Role: g.Role, Pending: g.Pending(), Email: g.Email.String()}

		if !g.Pending() {
			u := n.of(g.UserID)
			p.Name, p.Email, p.You = u.Name, u.Email.String(), u.ID == me.ID
		}

		return p
	}

	direct, inherited, err := a.cfg.Tenancy.People(ctx, me.ID, scope)
	if err != nil {
		a.cfg.Log.Error("the people could not be read", "scope", scope.String(), "error", err)
	}

	for _, g := range direct {
		view.People = append(view.People, toPerson(g))
	}

	for _, g := range inherited {
		view.Inherited = append(view.Inherited, toPerson(g))
	}

	events, err := a.cfg.History.Recent(ctx, scope, historyShown)
	if err != nil {
		a.cfg.Log.Error("the history could not be read", "scope", scope.String(), "error", err)
	}

	for _, e := range events {
		l := line{At: e.At.UTC().Format("2006-01-02 15:04 UTC"), Action: e.Action, Detail: e.Detail, Via: e.Via}

		if !e.ActorID.Zero() {
			l.Who = n.of(e.ActorID).Named()
		}

		// A grant's line names its person by ID or address; show the name.
		if id, err := types.ParseID(e.Detail["who"]); err == nil {
			detail := map[string]string{}
			for k, v := range e.Detail {
				detail[k] = v
			}

			detail["who"] = n.of(id).Named()
			l.Detail = detail
		}

		view.History = append(view.History, l)
	}

	return view
}

// --- organizations ----------------------------------------------------------

type newOrgView struct {
	Name    string
	Problem string
}

func (a app) newOrg(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "org-new", newOrgView{})
}

func (a app) createOrg(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	name := r.PostFormValue("name")

	o, err := a.cfg.Tenancy.CreateOrg(r.Context(), a.cfg.Now(), me.ID, name)
	if code := problem(err); code != "" {
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "org-new", newOrgView{Name: name, Problem: code})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	a.startList(r, me.ID, o.Scope())

	back(w, r, "/orgs/"+o.ID.String(), "created")
}

// startList gives a new list its transfer and pass-through categories, named
// in the creator's language (text/starter-categories.txt). A failure is
// logged and not the person's problem: the organization is made, and the
// two can be added by hand.
func (a app) startList(r *http.Request, actor types.ID, owner types.Scope) {
	if a.cfg.Categories == nil {
		return
	}

	text, err := a.cfg.Render.Text(mid.LangFrom(r.Context()), "starter-categories", nil)
	if err == nil {
		var names []string

		for line := range strings.Lines(text) {
			if line = strings.TrimSpace(line); line != "" {
				names = append(names, line)
			}
		}

		if len(names) != 2 {
			err = fmt.Errorf("starter-categories.txt has %d names, not 2", len(names))
		} else {
			err = a.cfg.Categories.Start(r.Context(), a.cfg.Now(), actor, owner, []categorybus.Starter{
				{Name: names[0], Kind: categorybus.Transfer},
				{Name: names[1], Kind: categorybus.PassThrough},
			})
		}
	}

	if err != nil {
		a.cfg.Log.Error("a new list's first categories could not be made", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	}
}

type orgView struct {
	scopeView

	Org              tenancybus.Org
	Accounts         []tenancybus.Account
	ArchivedAccounts []tenancybus.Account
	Projects         []tenancybus.Project
	ArchivedProjects []tenancybus.Project

	// Name is what was typed into the rename form, kept when refused.
	Name string
}

func (a app) orgPage(w http.ResponseWriter, r *http.Request, id types.ID, status int, edit func(*orgView)) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	o, access, err := a.cfg.Tenancy.Org(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	accounts, projects, err := a.cfg.Tenancy.Contents(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := orgView{scopeView: a.scope(r, me, o.Scope(), "/orgs/"+o.ID.String(), access), Org: o, Name: o.Name}

	for _, acct := range accounts {
		if acct.Archived() {
			view.ArchivedAccounts = append(view.ArchivedAccounts, acct)
		} else {
			view.Accounts = append(view.Accounts, acct)
		}
	}

	for _, p := range projects {
		if p.Archived() {
			view.ArchivedProjects = append(view.ArchivedProjects, p)
		} else {
			view.Projects = append(view.Projects, p)
		}
	}

	if edit != nil {
		edit(&view)
	}

	a.cfg.Render.Render(w, r, status, "org", view)
}

func (a app) org(w http.ResponseWriter, r *http.Request) {
	if id, ok := a.pathID(w, r, "id"); ok {
		a.orgPage(w, r, id, http.StatusOK, nil)
	}
}

func (a app) renameOrg(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	name := r.PostFormValue("name")

	_, err := a.cfg.Tenancy.RenameOrg(r.Context(), a.cfg.Now(), me.ID, id, name)
	if code := problem(err); code != "" {
		a.orgPage(w, r, id, http.StatusUnprocessableEntity, func(v *orgView) { v.Problem, v.Name = code, name })

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/orgs/"+id.String(), "saved")
}

func (a app) archiveOrg(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	archived := r.PostFormValue("archived") == "1"

	if _, err := a.cfg.Tenancy.SetOrgArchived(r.Context(), a.cfg.Now(), me.ID, id, archived); err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/orgs/"+id.String(), archivedDone(archived))
}

func archivedDone(archived bool) string {
	if archived {
		return "archived"
	}

	return "restored"
}

// --- accounts ---------------------------------------------------------------

type accountFormView struct {
	Org     tenancybus.Org // zero for a personal account
	Fields  tenancybus.AccountFields
	Kinds   []tenancybus.AccountKind
	Problem string
}

// inOrg is the organization named by ?org= or the org field, checked to be
// one the reader can do p in; the zero Org for none.
func (a app) inOrg(w http.ResponseWriter, r *http.Request, me userbus.User, raw string, p tenancybus.Permission) (tenancybus.Org, bool) {
	if raw == "" {
		return tenancybus.Org{}, true
	}

	id, err := types.ParseID(raw)
	if err != nil {
		a.missing(w, r)

		return tenancybus.Org{}, false
	}

	o, access, err := a.cfg.Tenancy.Org(r.Context(), me.ID, id)
	if err == nil && !access.Can(p) {
		err = tenancybus.ErrForbidden
	}

	if err != nil {
		a.failed(w, r, err)

		return tenancybus.Org{}, false
	}

	return o, true
}

func (a app) newAccount(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	o, ok := a.inOrg(w, r, me, r.URL.Query().Get("org"), tenancybus.Manage)
	if !ok {
		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "account-new", accountFormView{
		Org: o, Kinds: tenancybus.AccountKinds,
		Fields: tenancybus.AccountFields{Kind: string(tenancybus.Checking), Currency: tenancybus.DefaultCurrency},
	})
}

func accountFields(r *http.Request) tenancybus.AccountFields {
	return tenancybus.AccountFields{
		Name:     r.PostFormValue("name"),
		Kind:     r.PostFormValue("kind"),
		Last4:    r.PostFormValue("last4"),
		Currency: r.PostFormValue("currency"),
		OpenedOn: r.PostFormValue("opened"),
	}
}

func (a app) createAccount(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	o, ok := a.inOrg(w, r, me, r.PostFormValue("org"), tenancybus.Manage)
	if !ok {
		return
	}

	f := accountFields(r)

	acct, err := a.cfg.Tenancy.CreateAccount(r.Context(), a.cfg.Now(), me.ID, o.ID, f)
	if code := problem(err); code != "" {
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "account-new", accountFormView{
			Org: o, Kinds: tenancybus.AccountKinds, Fields: f, Problem: code,
		})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	// A personal account keeps its own list; one in an organization uses
	// the organization's, started when the organization was.
	if acct.OrgID.Zero() {
		a.startList(r, me.ID, acct.Scope())
	}

	back(w, r, "/accounts/"+acct.ID.String(), "created")
}

type accountView struct {
	scopeView

	Account tenancybus.Account
	Org     tenancybus.Org // zero for a personal account, or one the reader cannot see
	Fields  tenancybus.AccountFields
	Kinds   []tenancybus.AccountKind

	// ByHolder is whether its statements arrive one file per holder, and
	// Holders whether that is known (Config.Holders).
	ByHolder, Holders bool
}

func (a app) accountPage(w http.ResponseWriter, r *http.Request, id types.ID, status int, edit func(*accountView)) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	acct, access, err := a.cfg.Tenancy.Account(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := accountView{
		scopeView: a.scope(r, me, acct.Scope(), "/accounts/"+acct.ID.String(), access),
		Account:   acct,
		Kinds:     tenancybus.AccountKinds,
		Fields: tenancybus.AccountFields{
			Name: acct.Name, Kind: string(acct.Kind), Last4: acct.Last4, Currency: acct.Currency, OpenedOn: acct.OpenedOn.String(),
		},
	}

	// The organization's name, for the way back to it -- when the reader
	// can see it. Somebody given one account sees the account, not where it
	// lives.
	if !acct.OrgID.Zero() {
		if o, _, err := a.cfg.Tenancy.Org(r.Context(), me.ID, acct.OrgID); err == nil {
			view.Org = o
		}
	}

	if a.cfg.Holders != nil && access.Can(tenancybus.Manage) {
		if view.ByHolder, err = a.cfg.Holders.ByHolder(r.Context(), me.ID, id); err != nil {
			a.failed(w, r, err)

			return
		}

		view.Holders = true
	}

	if edit != nil {
		edit(&view)
	}

	a.cfg.Render.Render(w, r, status, "account-page", view)
}

func (a app) account(w http.ResponseWriter, r *http.Request) {
	if id, ok := a.pathID(w, r, "id"); ok {
		a.accountPage(w, r, id, http.StatusOK, nil)
	}
}

func (a app) editAccount(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	f := accountFields(r)

	_, err := a.cfg.Tenancy.EditAccount(r.Context(), a.cfg.Now(), me.ID, id, f)
	if code := problem(err); code != "" {
		a.accountPage(w, r, id, http.StatusUnprocessableEntity, func(v *accountView) { v.Problem, v.Fields = code, f })

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/accounts/"+id.String(), "saved")
}

func (a app) archiveAccount(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	archived := r.PostFormValue("archived") == "1"

	if _, err := a.cfg.Tenancy.SetAccountArchived(r.Context(), a.cfg.Now(), me.ID, id, archived); err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/accounts/"+id.String(), archivedDone(archived))
}

// --- projects ---------------------------------------------------------------

type projectFormView struct {
	Org     tenancybus.Org
	Fields  tenancybus.ProjectFields
	Problem string
}

func (a app) newProject(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	o, ok := a.inOrg(w, r, me, r.URL.Query().Get("org"), tenancybus.Bookkeep)
	if !ok {
		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "project-new", projectFormView{Org: o})
}

func projectFields(r *http.Request) tenancybus.ProjectFields {
	return tenancybus.ProjectFields{
		Name:     r.PostFormValue("name"),
		StartsOn: r.PostFormValue("starts"),
		EndsOn:   r.PostFormValue("ends"),
		Note:     r.PostFormValue("note"),
	}
}

func (a app) createProject(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	o, ok := a.inOrg(w, r, me, r.PostFormValue("org"), tenancybus.Bookkeep)
	if !ok {
		return
	}

	f := projectFields(r)

	p, err := a.cfg.Tenancy.CreateProject(r.Context(), a.cfg.Now(), me.ID, o.ID, f)
	if code := problem(err); code != "" {
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "project-new", projectFormView{Org: o, Fields: f, Problem: code})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/projects/"+p.ID.String(), "created")
}

type projectView struct {
	scopeView

	Project tenancybus.Project
	Org     tenancybus.Org
	Fields  tenancybus.ProjectFields
}

func (a app) projectPage(w http.ResponseWriter, r *http.Request, id types.ID, status int, edit func(*projectView)) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	p, access, err := a.cfg.Tenancy.Project(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := projectView{
		scopeView: a.scope(r, me, p.Scope(), "/projects/"+p.ID.String(), access),
		Project:   p,
		Fields: tenancybus.ProjectFields{
			Name: p.Name, StartsOn: p.StartsOn.String(), EndsOn: p.EndsOn.String(), Note: p.Note,
		},
	}

	if !p.OrgID.Zero() {
		if o, _, err := a.cfg.Tenancy.Org(r.Context(), me.ID, p.OrgID); err == nil {
			view.Org = o
		}
	}

	if edit != nil {
		edit(&view)
	}

	a.cfg.Render.Render(w, r, status, "project", view)
}

func (a app) project(w http.ResponseWriter, r *http.Request) {
	if id, ok := a.pathID(w, r, "id"); ok {
		a.projectPage(w, r, id, http.StatusOK, nil)
	}
}

func (a app) editProject(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	f := projectFields(r)

	_, err := a.cfg.Tenancy.EditProject(r.Context(), a.cfg.Now(), me.ID, id, f)
	if code := problem(err); code != "" {
		a.projectPage(w, r, id, http.StatusUnprocessableEntity, func(v *projectView) { v.Problem, v.Fields = code, f })

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/projects/"+id.String(), "saved")
}

func (a app) archiveProject(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	archived := r.PostFormValue("archived") == "1"

	if _, err := a.cfg.Tenancy.SetProjectArchived(r.Context(), a.cfg.Now(), me.ID, id, archived); err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/projects/"+id.String(), archivedDone(archived))
}

// --- people -----------------------------------------------------------------

// rerender shows a scope's page again with a problem from the people forms.
func (a app) rerender(w http.ResponseWriter, r *http.Request, k kind, id types.ID, code string, email string, role tenancybus.Role) {
	set := func(v *scopeView) { v.Problem, v.Email, v.Role = code, email, role }

	switch k.scope {
	case types.ScopeOrg:
		a.orgPage(w, r, id, http.StatusUnprocessableEntity, func(v *orgView) { set(&v.scopeView) })
	case types.ScopeAccount:
		a.accountPage(w, r, id, http.StatusUnprocessableEntity, func(v *accountView) { set(&v.scopeView) })
	case types.ScopeProject:
		a.projectPage(w, r, id, http.StatusUnprocessableEntity, func(v *projectView) { set(&v.scopeView) })
	}
}

func (a app) addPerson(k kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok || !form(w, r) {
			return
		}

		id, ok := a.pathID(w, r, "id")
		if !ok {
			return
		}

		scope := types.Scope{Kind: k.scope, ID: id}
		typed := r.PostFormValue("email")
		role := tenancybus.Role(r.PostFormValue("role"))

		addr, err := types.ParseEmail(typed)
		if err != nil {
			a.rerender(w, r, k, id, "email", typed, role)

			return
		}

		g, known, err := a.cfg.Tenancy.Grant(r.Context(), a.cfg.Now(), me.ID, scope, addr, role)
		if code := problem(err); code != "" {
			a.rerender(w, r, k, id, code, typed, role)

			return
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		a.invite(r, me, g, addr, known)

		back(w, r, "/"+k.path+"/"+id.String(), "granted")
	}
}

func (a app) changeRole(k kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok || !form(w, r) {
			return
		}

		id, ok := a.pathID(w, r, "id")
		if !ok {
			return
		}

		grant, ok := a.pathID(w, r, "grant")
		if !ok {
			return
		}

		_, err := a.cfg.Tenancy.ChangeRole(r.Context(), a.cfg.Now(), me.ID, types.Scope{Kind: k.scope, ID: id}, grant,
			tenancybus.Role(r.PostFormValue("role")))
		if code := problem(err); code != "" {
			a.rerender(w, r, k, id, code, "", tenancybus.Viewer)

			return
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		back(w, r, "/"+k.path+"/"+id.String(), "role-changed")
	}
}

func (a app) removePerson(k kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok || !form(w, r) {
			return
		}

		id, ok := a.pathID(w, r, "id")
		if !ok {
			return
		}

		grant, ok := a.pathID(w, r, "grant")
		if !ok {
			return
		}

		err := a.cfg.Tenancy.RemoveGrant(r.Context(), a.cfg.Now(), me.ID, types.Scope{Kind: k.scope, ID: id}, grant)
		if code := problem(err); code != "" {
			a.rerender(w, r, k, id, code, "", tenancybus.Viewer)

			return
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		// Somebody who removed their own access can no longer see the page
		// they were on.
		if access, err := a.cfg.Tenancy.AccessTo(r.Context(), me.ID, types.Scope{Kind: k.scope, ID: id}); err == nil && !access.Can(tenancybus.Read) {
			http.Redirect(w, r, "/", http.StatusSeeOther)

			return
		}

		back(w, r, "/"+k.path+"/"+id.String(), "removed")
	}
}

// inviteMail is what an invitation is written from.
type inviteMail struct {
	From    string
	What    string
	Role    tenancybus.Role
	Known   bool
	SignIn  string
	Address string
}

// invite tells somebody they were given a role. No link that signs them in:
// they sign in the ordinary way, at an address that fills theirs in, so
// that a forwarded invitation gives nobody else anything.
func (a app) invite(r *http.Request, from userbus.User, g tenancybus.Grant, to types.Email, known bool) {
	log := a.cfg.Log.With("request_id", web.RequestIDFrom(r.Context()), "grant_id", g.ID.String())

	if a.cfg.Mail == nil {
		log.Error("an invitation could not be sent because no mail relay is configured")

		return
	}

	what := ""

	switch g.Scope.Kind {
	case types.ScopeOrg:
		if o, _, err := a.cfg.Tenancy.Org(r.Context(), from.ID, g.Scope.ID); err == nil {
			what = o.Name
		}
	case types.ScopeAccount:
		if acct, _, err := a.cfg.Tenancy.Account(r.Context(), from.ID, g.Scope.ID); err == nil {
			what = acct.Name
		}
	case types.ScopeProject:
		if p, _, err := a.cfg.Tenancy.Project(r.Context(), from.ID, g.Scope.ID); err == nil {
			what = p.Name
		}
	}

	// In their language if they have said, and in the granter's otherwise:
	// most invitations go to somebody in the same parish.
	lang := mid.LangFrom(r.Context())

	if known {
		if u, err := a.cfg.Users.ByID(r.Context(), g.UserID); err == nil && u.Lang != "" {
			lang = u.Lang
		}
	}

	subject, body, err := a.cfg.Render.Mail(lang, "invitation", inviteMail{
		From:    from.Named(),
		What:    what,
		Role:    g.Role,
		Known:   known,
		SignIn:  a.cfg.BaseURL + "/sign-in?email=" + url.QueryEscape(to.String()),
		Address: to.String(),
	})
	if err != nil {
		log.Error("an invitation could not be written", "error", err)

		return
	}

	if err := a.cfg.Mail.Send(r.Context(), mail.Message{To: to.String(), Subject: subject, Text: body}); err != nil {
		log.Error("an invitation could not be sent", "error", err)
	}
}
