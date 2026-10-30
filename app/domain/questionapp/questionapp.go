// Package questionapp is the pages of questions about the books
// (docs/accountant.md, 4): a question's own page, with its thread and the
// link that opens it, and the forms that ask, reply and mark answered.
//
// A thing's page shows the questions about it through the "questions"
// partial every app shares (page/assets/app/base.html), from the summary
// questionbus gives; the forms in it post here. So this app is the only
// one that changes a question, and no app imports another.
package questionapp

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
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

// Users names who wrote in a thread.
type Users interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
}

// Access is the roles a writer holds where the question's history goes,
// for the words beside their name, and the names of the projects a part
// is in.
type Access interface {
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
	ProjectNames(ctx context.Context, ids []types.ID) (map[types.ID]string, error)
}

// Config is what this app needs.
type Config struct {
	Log       *slog.Logger
	Questions *questionbus.Business
	Users     Users
	Access    Access
	Render    Renderer

	// BaseURL is the public origin, for the link a person pastes into an
	// email.
	BaseURL string

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type app struct {
	cfg Config
}

// Routes mounts this app behind guard, which is mid.Require: a question's
// link opens only after signing in, and comes back to the question.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, guard(h)) }

	handle("GET /questions/{id}", a.question)
	handle("POST /questions", a.ask)
	handle("POST /questions/{id}/reply", a.reply)
	handle("POST /questions/{id}/answered", a.answered)
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
		a.cfg.Render.Render(w, r, http.StatusNotFound, "question-missing", nil)

		return types.ID{}, false
	}

	return id, true
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, questionbus.ErrNotFound):
		a.cfg.Render.Render(w, r, http.StatusNotFound, "question-missing", nil)
	case errors.Is(err, questionbus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "question-refused", nil)
	default:
		a.cfg.Log.Error("a request about a question failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "question-refused", "server")
	}
}

// who is somebody who wrote in a thread: their name, and the role they
// hold where the question's history goes, if any still.
type who struct {
	Name string
	Role tenancybus.Role
}

type replyView struct {
	questionbus.Reply
	Author who
}

// aboutView is what the question is about, as its page says it: where it
// is, and for a transaction the amount the reader may see -- the parts in
// their projects, Part, when they may not open the transaction itself.
type aboutView struct {
	Path     string
	Amount   money.Amount
	Part     bool
	Projects []string
}

type threadView struct {
	questionbus.Thread

	Where   aboutView
	Asker   who
	Replies []replyView

	// Link is the question's own address, whole, to paste into an email.
	Link string

	Done    string
	Problem string
	Draft   string
}

func (a app) question(w http.ResponseWriter, r *http.Request) {
	a.questionPage(w, r, http.StatusOK, "", "")
}

func (a app) questionPage(w http.ResponseWriter, r *http.Request, status int, problem, draft string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	ctx := r.Context()

	th, err := a.cfg.Questions.Question(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := threadView{
		Thread:  th,
		Link:    strings.TrimSuffix(a.cfg.BaseURL, "/") + "/questions/" + th.ID.String(),
		Done:    r.URL.Query().Get("done"),
		Problem: problem,
		Draft:   draft,
	}

	if view.Where, err = a.about(ctx, th); err != nil {
		a.failed(w, r, err)

		return
	}

	names := map[types.ID]who{}
	writer := func(id types.ID) (who, error) {
		if w, ok := names[id]; ok {
			return w, nil
		}

		var w who

		u, err := a.cfg.Users.ByID(ctx, id)
		if err != nil {
			return who{}, err
		}

		w.Name = u.Named()

		access, err := a.cfg.Access.AccessTo(ctx, id, th.Home)
		if err != nil {
			return who{}, err
		}

		if len(access.Roles) > 0 {
			w.Role = access.Roles[0]
		}

		names[id] = w

		return w, nil
	}

	if view.Asker, err = writer(th.AskedBy); err != nil {
		a.failed(w, r, err)

		return
	}

	for _, reply := range th.Replies {
		author, err := writer(reply.AuthorID)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		view.Replies = append(view.Replies, replyView{Reply: reply, Author: author})
	}

	a.cfg.Render.Render(w, r, status, "question", view)
}

// about is where the thing a question is about is, and for a transaction
// the money the reader may see of it.
func (a app) about(ctx context.Context, th questionbus.Thread) (aboutView, error) {
	switch th.About.Kind {
	case questionbus.OnStatement:
		return aboutView{Path: "/statements/" + th.About.ID.String()}, nil
	case questionbus.OnProject:
		return aboutView{Path: "/projects/" + th.About.ID.String()}, nil
	case questionbus.OnPayee:
		return aboutView{Path: "/payees/" + th.About.ID.String()}, nil
	}

	t := th.Transaction
	if th.Whole {
		return aboutView{Path: "/transactions/" + t.ID.String(), Amount: t.Amount}, nil
	}

	view := aboutView{Part: true}

	var ids []types.ID

	for _, s := range t.Splits {
		view.Amount += s.Amount
		ids = append(ids, s.ProjectID)
	}

	named, err := a.cfg.Access.ProjectNames(ctx, ids)
	if err != nil {
		return aboutView{}, err
	}

	for _, id := range ids {
		if n := named[id]; n != "" {
			view.Projects = append(view.Projects, n)
		}
	}

	return view, nil
}

// ask asks a question from the form on a thing's page, and opens it.
func (a app) ask(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	about := questionbus.About{Kind: questionbus.Kind(r.PostFormValue("kind"))}

	id, err := types.ParseID(r.PostFormValue("id"))
	if err != nil || !about.Kind.Valid() {
		a.cfg.Render.Render(w, r, http.StatusNotFound, "question-missing", nil)

		return
	}

	about.ID = id
	text := r.PostFormValue("body")

	q, err := a.cfg.Questions.Ask(r.Context(), a.cfg.Now(), me.ID, about, text)

	switch {
	case errors.Is(err, questionbus.ErrBody):
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "question-ask", askView{About: about, Draft: text})

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/questions/"+q.ID.String()+"?done=asked", http.StatusSeeOther)
}

// askView is the form to ask again, when what was sent was empty or too
// long: the browser stops both before sending, so this is for one that
// does not.
type askView struct {
	About questionbus.About
	Draft string
}

func (a app) reply(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	text := r.PostFormValue("body")

	_, err := a.cfg.Questions.Reply(r.Context(), a.cfg.Now(), me.ID, id, text)

	switch {
	case errors.Is(err, questionbus.ErrBody):
		a.questionPage(w, r, http.StatusUnprocessableEntity, "body", text)

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/questions/"+id.String()+"?done=replied", http.StatusSeeOther)
}

func (a app) answered(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	if err := a.cfg.Questions.MarkAnswered(r.Context(), a.cfg.Now(), me.ID, id); err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/questions/"+id.String()+"?done=answered", http.StatusSeeOther)
}
