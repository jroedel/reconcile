package apiapp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/question/questionbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// Questions through a key (docs/accountant.md, 6): a person's Claude reads
// the questions about the books with them, asks one for them, replies,
// and marks their own answered, as they would on a question's page. The
// business layer decides who may, as for the pages: reading takes a role
// that reads the thing, writing one that may note on it (an accountant,
// a contributor, a bookkeeper, an owner), and marking answered is the
// asker's alone. What is written through a key is marked with its name,
// on the question's page and in list_changes.
//
// And the person's own weekly email settings, read only: changing them,
// or the time zone they are read in, is the person's, on their profile.

// Questions is the slice of questionbus the API reads and writes through.
type Questions interface {
	Ask(ctx context.Context, now time.Time, actor types.ID, about questionbus.About, text string) (questionbus.Question, error)
	Question(ctx context.Context, actor, id types.ID) (questionbus.Thread, error)
	Summary(ctx context.Context, actor types.ID, about questionbus.About) (questionbus.Summary, error)
	InOrg(ctx context.Context, actor, orgID types.ID, state questionbus.State, askedBy types.ID) ([]questionbus.Listed, error)
	Reply(ctx context.Context, now time.Time, actor, id types.ID, text string) (questionbus.Reply, error)
	MarkAnswered(ctx context.Context, now time.Time, actor, id types.ID) error
}

// People names who wrote in a thread, and reads the key's own person's
// weekly email settings.
type People interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
	Weekly(ctx context.Context, userID types.ID) (userbus.Weekly, error)
}

func (a app) questionEndpoints() []Endpoint {
	if !a.books.complete() {
		return nil
	}

	read, write := userbus.BooksRead, userbus.BooksWrite
	kinds := []string{string(questionbus.OnTransaction), string(questionbus.OnStatement), string(questionbus.OnProject), string(questionbus.OnPayee)}
	out := "{id, about: {kind, id, what, url}, state, text, asked_by, asked_by_you, asked_at, answered_at, through, replies: [{by, by_you, at, text, through}], url}"

	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/questions", Scope: read, Tool: "list_questions",
			Summary: "The questions about one thing -- a transaction, a statement, a project or a payee -- with about and id; or an organization's, with organization, newest first. Each has its replies and the link to its page, which a person can paste into an email.",
			Query: []Field{
				{Name: "about", Type: "string", Values: kinds, Description: "What the thing is, with id."},
				{Name: "id", Type: "string", Description: "The thing's id, with about."},
				{Name: "organization", Type: "string", Description: "An organization's id, for every question about anything in it, instead of about and id."},
				{Name: "state", Type: "string", Values: []string{string(questionbus.Open), string(questionbus.Answered)}, Description: "Only those still waiting for their answer, or only those answered; both when left out."},
				{Name: "mine", Type: "boolean", Description: "With organization: only the questions you asked."},
			},
			Returns: "{questions: [" + out + "], can_ask (for one thing: whether you may ask about it)}", handler: a.listQuestions,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/questions/{question}", Scope: read, Tool: "get_question",
			Summary: "One question with its replies, by the id at the end of its link.",
			Returns: "{question: " + out + ", can_reply, can_mark_answered}", handler: a.getQuestion,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/questions", Scope: write, Tool: "ask_question",
			Summary: "Ask a question about a transaction, a statement, a project or a payee, as the person, for whoever keeps or reads its books to answer on the site. Ask only what the person asked you to ask, in their words; it is not sent anybody by email, and the weekly email tells of it.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "about", Type: "string", Required: true, Values: kinds, Description: "What the thing is."},
				{Name: "id", Type: "string", Required: true, Description: "The thing's id."},
				{Name: "text", Type: "string", Required: true, Description: "The question, plain text, at most 2,000 characters."},
			}},
			Returns: "{question: " + out + "}: give the person its url", handler: a.askQuestion,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/questions/{question}/replies", Scope: write, Tool: "reply_to_question",
			Summary: "Reply to a question, as the person. A reply by anybody but the one who asked answers it.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "text", Type: "string", Required: true, Description: "The reply, plain text, at most 2,000 characters."},
			}},
			Returns: "{question: " + out + "}", handler: a.replyToQuestion,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/questions/{question}/answered", Scope: write, Tool: "mark_question_answered",
			Summary: "Mark a question the person asked as answered. Only the one who asked may; anybody else answers it by replying.",
			Returns: "{question: " + out + "}", handler: a.markQuestionAnswered,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/me/weekly-email", Scope: read, Tool: "get_weekly_email_settings",
			Summary: "Whether you get the weekly email of what is new, on which day and at what hour of your time zone, and when the next one comes. Changing them is yours, on your profile, at url.",
			Returns: "{enabled, weekday (Monday to Sunday), hour (0 to 23), time_zone (UTC when none is set), next (RFC 3339, when enabled), url}", handler: a.weeklyEmail,
		},
	}
}

type questionAbout struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	What string `json:"what,omitempty"`
	URL  string `json:"url"`
}

type replyOut struct {
	By      string `json:"by"`
	ByYou   bool   `json:"by_you,omitempty"`
	At      string `json:"at"`
	Text    string `json:"text"`
	Through string `json:"through,omitempty"`
}

type questionOut struct {
	ID         string        `json:"id"`
	About      questionAbout `json:"about"`
	State      string        `json:"state"`
	Text       string        `json:"text"`
	AskedBy    string        `json:"asked_by"`
	AskedByYou bool          `json:"asked_by_you,omitempty"`
	AskedAt    string        `json:"asked_at"`
	AnsweredAt string        `json:"answered_at,omitempty"`
	Through    string        `json:"through,omitempty"`
	Replies    []replyOut    `json:"replies"`
	URL        string        `json:"url"`
}

// aboutPaths is the page of each kind of thing.
var aboutPaths = map[questionbus.Kind]string{
	questionbus.OnTransaction: "/transactions/",
	questionbus.OnStatement:   "/statements/",
	questionbus.OnProject:     "/projects/",
	questionbus.OnPayee:       "/payees/",
}

// person is somebody's name, looked up once; the reader's failure is
// kept, as for an account.
func (rd *reader) person(id types.ID) string {
	if n, ok := rd.people[id]; ok {
		return n
	}

	u, err := rd.a.books.People.ByID(rd.ctx, id)
	if err != nil && rd.failed == nil {
		rd.failed = err
	}

	rd.people[id] = u.Named()

	return rd.people[id]
}

func (rd *reader) questionOut(q questionbus.Question, what string) questionOut {
	out := questionOut{
		ID:    q.ID.String(),
		About: questionAbout{Kind: string(q.About.Kind), ID: q.About.ID.String(), What: what, URL: rd.url(aboutPaths[q.About.Kind] + q.About.ID.String())},
		State: string(q.State), Text: q.Body,
		AskedBy: rd.person(q.AskedBy), AskedByYou: q.AskedBy == rd.me, AskedAt: q.AskedAt.UTC().Format(time.RFC3339),
		Through: q.Via, Replies: make([]replyOut, 0, len(q.Replies)),
		URL: rd.url("/questions/" + q.ID.String()),
	}

	if !q.AnsweredAt.IsZero() {
		out.AnsweredAt = q.AnsweredAt.UTC().Format(time.RFC3339)
	}

	for _, r := range q.Replies {
		out.Replies = append(out.Replies, replyOut{By: rd.person(r.AuthorID), ByYou: r.AuthorID == rd.me, At: r.At.UTC().Format(time.RFC3339), Text: r.Body, Through: r.Via})
	}

	return out
}

// questionRefused answers a refusal of the question domain's, and reports
// whether err was one.
func (a app) questionRefused(w http.ResponseWriter, r *http.Request, err error, forbidden string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, questionbus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("", "There is no such question or thing, or it is not one you may see."))
	case errors.Is(err, questionbus.ErrForbidden):
		web.WriteJSON(w, http.StatusForbidden, web.Problem("", forbidden))
	case errors.Is(err, questionbus.ErrBody):
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("text", "Write the text, in at most 2,000 characters."))
	default:
		a.fail(w, r, "a question through the API", err)
	}

	return true
}

// about reads what a question is about, answering 400 itself.
func (a app) about(w http.ResponseWriter, kind, id string) (questionbus.About, bool) {
	k := questionbus.Kind(kind)
	if !k.Valid() {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("about", "Say what the question is about: transaction, statement, project or payee."))

		return questionbus.About{}, false
	}

	tid, err := types.ParseID(id)
	if err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("id", "Give the id of the "+kind+", as another answer gave it."))

		return questionbus.About{}, false
	}

	return questionbus.About{Kind: k, ID: tid}, true
}

func (a app) listQuestions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rd := a.reader(r)
	ctx := r.Context()

	state := questionbus.State(q.Get("state"))
	if state != "" && state != questionbus.Open && state != questionbus.Answered {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("state", "Give state as open or answered, or leave it out for both."))

		return
	}

	if org := q.Get("organization"); org != "" {
		orgID, ok := a.optionalID(w, org, "organization")
		if !ok {
			return
		}

		var askedBy types.ID
		if q.Get("mine") == "true" {
			askedBy = rd.me
		}

		listed, err := a.books.Questions.InOrg(ctx, rd.me, orgID, state, askedBy)
		if a.questionRefused(w, r, err, "") {
			return
		}

		out := make([]questionOut, 0, len(listed))
		for _, l := range listed {
			out = append(out, rd.questionOut(l.Question, l.What))
		}

		rd.done(w, r, map[string]any{"questions": out})

		return
	}

	if q.Get("about") == "" {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("about", "Give about and id for one thing's questions, or organization for an organization's."))

		return
	}

	about, ok := a.about(w, q.Get("about"), q.Get("id"))
	if !ok {
		return
	}

	s, err := a.books.Questions.Summary(ctx, rd.me, about)
	if a.questionRefused(w, r, err, "") {
		return
	}

	out := make([]questionOut, 0, len(s.Open)+len(s.Answered))
	for _, qn := range s.Open {
		if state != questionbus.Answered {
			out = append(out, rd.questionOut(qn, ""))
		}
	}

	for _, qn := range s.Answered {
		if state != questionbus.Open {
			out = append(out, rd.questionOut(qn, ""))
		}
	}

	rd.done(w, r, map[string]any{"questions": out, "can_ask": s.CanAsk})
}

// thread answers with a question as its reader may see it.
func (a app) thread(w http.ResponseWriter, r *http.Request, rd *reader, id types.ID, extra bool) {
	t, err := a.books.Questions.Question(r.Context(), rd.me, id)
	if a.questionRefused(w, r, err, "") {
		return
	}

	out := map[string]any{"question": rd.questionOut(t.Question, t.What)}
	if extra {
		out["can_reply"], out["can_mark_answered"] = t.CanReply, t.CanMark
	}

	rd.done(w, r, out)
}

func (a app) getQuestion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "question", "question")
	if !ok {
		return
	}

	a.thread(w, r, a.reader(r), id, true)
}

func (a app) askQuestion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		About string `json:"about"`
		ID    string `json:"id"`
		Text  string `json:"text"`
	}

	if !a.body(w, r, &in) {
		return
	}

	about, ok := a.about(w, in.About, in.ID)
	if !ok {
		return
	}

	rd := a.reader(r)

	q, err := a.books.Questions.Ask(r.Context(), time.Now(), rd.me, about, in.Text)
	if a.questionRefused(w, r, err, "Your role there does not allow asking questions; a viewer reads only. Nothing was asked; tell the person.") {
		return
	}

	a.thread(w, r, rd, q.ID, false)
}

func (a app) replyToQuestion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "question", "question")
	if !ok {
		return
	}

	var in struct {
		Text string `json:"text"`
	}

	if !a.body(w, r, &in) {
		return
	}

	rd := a.reader(r)

	_, err := a.books.Questions.Reply(r.Context(), time.Now(), rd.me, id, in.Text)
	if a.questionRefused(w, r, err, "Your role there does not allow replying; a viewer reads only. Nothing was sent; tell the person.") {
		return
	}

	a.thread(w, r, rd, id, false)
}

func (a app) markQuestionAnswered(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r, "question", "question")
	if !ok {
		return
	}

	rd := a.reader(r)

	err := a.books.Questions.MarkAnswered(r.Context(), time.Now(), rd.me, id)
	if a.questionRefused(w, r, err, "Only the person who asked a question may mark it answered; anybody else answers it by replying. Nothing was changed.") {
		return
	}

	a.thread(w, r, rd, id, false)
}

func (a app) weeklyEmail(w http.ResponseWriter, r *http.Request) {
	u, _ := mid.UserFrom(r.Context())

	wk, err := a.books.People.Weekly(r.Context(), u.ID)
	if err != nil {
		a.fail(w, r, "reading the weekly email's settings", err)

		return
	}

	zone := u.Zone
	if zone == "" {
		zone = "UTC"
	}

	out := map[string]any{
		"enabled": wk.Enabled, "weekday": wk.Weekday.String(), "hour": wk.Hour, "time_zone": zone,
		"url": a.base + "/account#weekly",
	}

	if wk.Enabled {
		out["next"] = wk.Next(time.Now(), userbus.Location(u.Zone)).Format(time.RFC3339)
	}

	web.WriteJSON(w, http.StatusOK, out)
}
