// Package translationapp is the review screen at /translations, where a
// translator looks over the interface's Spanish or Portuguese after Claude
// has written it (docs/translations.md).
//
// Afterwards, not first: every translation here is already on the pages.
// The screen is a list a person works through, one language and one queue
// at a time -- Claude's drafts nobody has looked at, the ones sent back, the
// approved, and the strings nobody has translated yet -- with the English,
// the words, who wrote them, the pages they are on, and three things to do:
// keep the words (changed first, if they need it), send them back to Claude
// with a note, or say the whole page looks right.
//
// It works without JavaScript, as every page here does: each string is its
// own small form, and "all of these look right" is one more form carrying
// what the page showed.
//
// Nobody but the site administrator and the translators they name sees it;
// to anybody else it is a 404, as any page that is not theirs is.
package translationapp

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates
var templates embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = templates

// Path is the screen, which the account page links to.
const Path = "/translations"

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Translations is the part of translationbus this app uses.
type Translations interface {
	Languages(ctx context.Context, who translationbus.Translator) ([]types.Lang, error)
	Counts(ctx context.Context, who translationbus.Translator, lang types.Lang) (translationbus.Counts, error)
	Queue(ctx context.Context, who translationbus.Translator, lang types.Lang, q translationbus.Queue, offset int) ([]translationbus.Translation, int, error)
	Keep(ctx context.Context, now time.Time, who translationbus.Translator, lang types.Lang, src translationbus.Source, shown, text string) error
	SendBack(ctx context.Context, now time.Time, who translationbus.Translator, lang types.Lang, src translationbus.Source, shown, note string) error
	ApproveAll(ctx context.Context, now time.Time, who translationbus.Translator, lang types.Lang, shown []translationbus.Shown) (int, error)
}

// Users names the people who wrote a translation.
type Users interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
}

// Config is what this app needs.
type Config struct {
	Log          *slog.Logger
	Translations Translations
	Users        Users
	Render       Renderer

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type app struct {
	cfg Config
}

// Routes mounts this app behind guard, mid.Require.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	mux.Handle("GET "+Path, guard(http.HandlerFunc(a.page)))
	mux.Handle("POST "+Path+"/review", guard(http.HandlerFunc(a.review)))
	mux.Handle("POST "+Path+"/approve-all", guard(http.HandlerFunc(a.approveAll)))
}

// --- where the reviewer is --------------------------------------------------------

// place is which language, which queue and how far down: in the query on
// the way in, and in each form's hidden fields so that an action comes back
// to the same place.
//
// The language is "in", never "lang": ?lang= anywhere on the site is the
// reader choosing the language the pages are in (mid.Lang), which is not
// the same thing as the language somebody is reviewing.
type place struct {
	Lang  types.Lang
	Queue translationbus.Queue
	From  int
}

func (p place) URL() string {
	q := url.Values{"in": {string(p.Lang)}, "show": {string(p.Queue)}}
	if p.From > 0 {
		q.Set("from", strconv.Itoa(p.From))
	}

	return Path + "?" + q.Encode()
}

// placeFrom reads a place from v, against the languages the reviewer has.
// A language they do not have is false, which the handlers answer with a
// 404: the same as a page that is not there.
func placeFrom(v url.Values, langs []types.Lang) (place, bool) {
	p := place{Lang: types.Lang(v.Get("in")), Queue: translationbus.Queue(v.Get("show"))}

	if p.Lang == "" && len(langs) > 0 {
		p.Lang = langs[0]
	}

	if !slices.Contains(langs, p.Lang) {
		return place{}, false
	}

	if !slices.Contains(translationbus.Queues, p.Queue) {
		p.Queue = translationbus.ToCheck
	}

	if n, err := strconv.Atoi(v.Get("from")); err == nil && n > 0 {
		p.From = n
	}

	return p, true
}

func who(r *http.Request) translationbus.Translator {
	u, _ := mid.UserFrom(r.Context())

	return translationbus.Translator{ID: u.ID, SiteAdmin: u.SiteAdmin}
}

// languages is the reviewer's languages, or false with the answer written:
// a 404 for somebody with none.
func (a app) languages(w http.ResponseWriter, r *http.Request) ([]types.Lang, bool) {
	langs, err := a.cfg.Translations.Languages(r.Context(), who(r))

	switch {
	case err != nil:
		a.failed(w, r, "reading somebody's languages", err)

		return nil, false
	case len(langs) == 0:
		http.NotFound(w, r)

		return nil, false
	}

	return langs, true
}

// --- the page ---------------------------------------------------------------------

type item struct {
	translationbus.Translation

	// By is who wrote the words: "" for Claude or for nobody yet, the
	// person's name otherwise. On is the pages the string is written in.
	By, On string

	// Typed, TypedNote and Problem are what the reviewer sent for this one
	// and why it was refused, so that what they wrote is not lost. Typed
	// is the words on the pages otherwise.
	Typed, TypedNote, Problem string
	Failed                    bool
}

type queueTab struct {
	Queue translationbus.Queue
	Count int
}

type view struct {
	Place  place
	Langs  []types.Lang
	Tabs   []queueTab
	Items  []item
	Total  int
	Next   string // the next page's address, or ""
	Before string // the previous page's

	// Done is what just happened, Count how many it was. Problem is a
	// refusal that belongs to no one string.
	Done    string
	Count   int
	Problem string
}

// failure is a refused action on one string, shown at that string.
type failure struct {
	src                  translationbus.Source
	typed, note, problem string
}

func (a app) page(w http.ResponseWriter, r *http.Request) {
	langs, ok := a.languages(w, r)
	if !ok {
		return
	}

	p, ok := placeFrom(r.URL.Query(), langs)
	if !ok {
		http.NotFound(w, r)

		return
	}

	n, _ := strconv.Atoi(r.URL.Query().Get("n"))

	a.show(w, r, http.StatusOK, langs, p, view{Done: r.URL.Query().Get("done"), Count: n}, nil)
}

func (a app) show(w http.ResponseWriter, r *http.Request, status int, langs []types.Lang, p place, v view, failed *failure) {
	ctx := r.Context()

	counts, err := a.cfg.Translations.Counts(ctx, who(r), p.Lang)
	if err != nil {
		a.failed(w, r, "counting translations", err)

		return
	}

	list, total, err := a.cfg.Translations.Queue(ctx, who(r), p.Lang, p.Queue, p.From)
	if err != nil {
		a.failed(w, r, "listing translations", err)

		return
	}

	v.Place, v.Langs, v.Total = p, langs, total
	v.Tabs = []queueTab{
		{translationbus.ToCheck, counts.ToCheck},
		{translationbus.SentBack, counts.SentBack},
		{translationbus.Done, counts.Done},
		{translationbus.Untouched, counts.Untouched},
	}

	if next := p.From + translationbus.ReviewPage; next < total {
		v.Next = place{Lang: p.Lang, Queue: p.Queue, From: next}.URL()
	}

	if p.From > 0 {
		v.Before = place{Lang: p.Lang, Queue: p.Queue, From: max(p.From-translationbus.ReviewPage, 0)}.URL()
	}

	names := map[types.ID]string{}

	for _, t := range list {
		it := item{Translation: t, Typed: t.Text, On: strings.Join(t.Pages, ", ")}

		if t.Origin == translationbus.ByPerson && !t.UpdatedBy.Zero() {
			it.By = a.name(ctx, names, t.UpdatedBy)
		}

		if failed != nil && failed.src == t.Source {
			it.Failed, it.Typed, it.TypedNote, it.Problem = true, failed.typed, failed.note, failed.problem
		}

		v.Items = append(v.Items, it)
	}

	a.cfg.Render.Render(w, r, status, "translations", v)
}

// name is somebody's name for "written by", read once a page.
func (a app) name(ctx context.Context, names map[types.ID]string, id types.ID) string {
	if n, ok := names[id]; ok {
		return n
	}

	n := "?"
	if u, err := a.cfg.Users.ByID(ctx, id); err == nil {
		n = u.Named()
	}

	names[id] = n

	return n
}

// --- acting -----------------------------------------------------------------------

// review is one string's form: keep the words in the box, or send them back
// with a note.
func (a app) review(w http.ResponseWriter, r *http.Request) {
	langs, ok := a.languages(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Go back, reload the page and try again.", http.StatusBadRequest)

		return
	}

	f := r.PostForm

	p, ok := placeFrom(f, langs)
	if !ok {
		http.NotFound(w, r)

		return
	}

	src := translationbus.Source{Context: f.Get("context"), EN: f.Get("en")}
	shown, now := f.Get("shown"), a.cfg.Now()

	var (
		err  error
		done string
	)

	switch f.Get("do") {
	case "send-back":
		err, done = a.cfg.Translations.SendBack(r.Context(), now, who(r), p.Lang, src, shown, f.Get("note")), "sent-back"
	default:
		err, done = a.cfg.Translations.Keep(r.Context(), now, who(r), p.Lang, src, shown, f.Get("text")), "kept"
	}

	refusal, refused := errors.AsType[translationbus.Refusal](err)

	switch {
	case refused:
		a.show(w, r, http.StatusUnprocessableEntity, langs, p, view{}, &failure{src: src, typed: f.Get("text"), note: f.Get("note"), problem: refusal.Problem})

		return
	case errors.Is(err, translationbus.ErrNotFound):
		// The string is gone: a newer binary no longer says it. Back to
		// the list, which no longer has it either.
		a.show(w, r, http.StatusNotFound, langs, p, view{Problem: "gone"}, nil)

		return
	case err != nil:
		a.failed(w, r, "reviewing a translation", err)

		return
	}

	http.Redirect(w, r, p.URL()+"&done="+done, http.StatusSeeOther)
}

// approveAll is "all of these look right", for the strings the page showed,
// as it showed them.
func (a app) approveAll(w http.ResponseWriter, r *http.Request) {
	langs, ok := a.languages(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Go back, reload the page and try again.", http.StatusBadRequest)

		return
	}

	f := r.PostForm

	p, ok := placeFrom(f, langs)
	if !ok {
		http.NotFound(w, r)

		return
	}

	// Three lists in the order the page wrote them, one entry a string:
	// a hidden input is sent even when its value is empty, so they stay
	// in step. Ones out of step are a form somebody wrote by hand.
	contexts, ens, texts := f["context"], f["en"], f["text"]
	if len(contexts) != len(ens) || len(ens) != len(texts) {
		http.Error(w, "That form could not be read. Go back, reload the page and try again.", http.StatusBadRequest)

		return
	}

	shown := make([]translationbus.Shown, len(ens))
	for i := range ens {
		shown[i] = translationbus.Shown{Source: translationbus.Source{Context: contexts[i], EN: ens[i]}, Text: texts[i]}
	}

	n, err := a.cfg.Translations.ApproveAll(r.Context(), a.cfg.Now(), who(r), p.Lang, shown)

	switch {
	case errors.Is(err, translationbus.ErrInvalid):
		http.Error(w, "That form could not be read. Go back, reload the page and try again.", http.StatusBadRequest)

		return
	case err != nil:
		a.failed(w, r, "approving translations", err)

		return
	}

	// Back to the top of the queue: what was approved has left it, so the
	// page that was second is now first.
	p.From = 0

	http.Redirect(w, r, p.URL()+"&done=approved&n="+strconv.Itoa(n), http.StatusSeeOther)
}

func (a app) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.Error(what+" failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong at our end. Please try again in a few minutes.", http.StatusInternalServerError)
}
