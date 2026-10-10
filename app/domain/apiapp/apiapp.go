// Package apiapp is the JSON API at /api/v1: how a program -- a person's
// own Claude, a script in their Google account -- works with the site
// without a screen. Today that is the interface's translations
// (docs/translations.md), the statement inbox, and reading and keeping
// the books (docs/books-api.md).
//
// # Scopes
//
// A key acts as the person who made it (mid.APIKey), for the scopes they
// chose (userbus.Scope), and every endpoint names the one it needs
// (Endpoint.Scope, enforced by mid.RequireScope). The scope is the coarse
// fence; the fine one is the business layer's, which asks tenancybus about
// the person every time, so a key never reaches further than its person
// does. Who may translate at all is the translation domain's rule
// (translationbus.MayTranslate).
//
// A key that may only upload reads nothing, not even the inbox it fills:
// it lives in a script, in somebody's Google account, which is exactly the
// kind of place a credential is read by somebody it was not meant for.
//
// # The index is the route table
//
// GET /api/v1 answers with every endpoint and the fields each takes. It is
// built from the same list that mounts the routes (endpoints, below), so an
// endpoint cannot exist without being in the index or be in the index
// without existing. The MCP server (mcpapp) makes its tools from it too. A
// program starts there rather than from documentation that may be older
// than the binary.
//
// Answers and refusals are sentences in English: they are read by a
// program, which is the one reader here, rather than by somebody choosing
// a language on a page.
package apiapp

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/web"
)

// Prefix is where the API lives. The version is in the path, so a v2 can sit
// beside it while a script written for v1 still works.
const Prefix = "/api/v1"

// Config is what this app needs.
type Config struct {
	Log          *slog.Logger
	Translations *translationbus.Business

	// Inbox is where statement files sent to the API wait (inbox.go).
	Inbox Inbox

	// Books is what the books endpoints read through (books.go).
	Books Books

	// BaseURL is the public origin, for the links in answers: a program
	// reading them may be anywhere.
	BaseURL string
}

type app struct {
	log          *slog.Logger
	translations *translationbus.Business
	inbox        Inbox
	books        Books
	base         string
}

// Routes mounts the API on its own mux. Each route that needs a key is
// behind mid.RequireScope with its scope; mid.APIKey, which reads the key,
// is the muxer's to put around the whole of it.
func Routes(mux *http.ServeMux, cfg Config) {
	a := app{log: cfg.Log, translations: cfg.Translations, inbox: cfg.Inbox, books: cfg.Books, base: cfg.BaseURL}

	for _, e := range a.Endpoints() {
		h := http.Handler(e.handler)
		if e.Scope != "" {
			h = mid.RequireScope(e.Scope)(h)
		}

		mux.Handle(e.Method+" "+e.Path, h)
	}

	// The index with a trailing slash too, since that is how a person types
	// it; and everything else under /api is a JSON 404 that says where the
	// index is, rather than the site's HTML one.
	mux.HandleFunc("GET "+Prefix+"/{$}", a.index)
	mux.HandleFunc("/api/", a.notFound)
}

// --- the index ------------------------------------------------------------------

// Field is one input an endpoint takes, as the index describes it.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Values      []string `json:"values,omitempty"`
	Description string   `json:"description"`
}

// Body is what an endpoint expects to be sent.
type Body struct {
	Encoding string  `json:"encoding"`
	Fields   []Field `json:"fields"`
}

// Endpoint is one route, as the index describes it and as Routes mounts it.
type Endpoint struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Summary string `json:"summary"`

	// Scope is what a key must be allowed to call it (userbus.Scope); ""
	// for the index, which needs no key.
	Scope userbus.Scope `json:"scope,omitempty"`

	Query   []Field `json:"query,omitempty"`
	Body    *Body   `json:"body,omitempty"`
	Returns string  `json:"returns"`

	// Tool is the endpoint's name as a tool on /mcp (mcpapp), which is how
	// Claude on claude.ai reaches it; "" for the index itself.
	Tool string `json:"tool,omitempty"`

	handler http.HandlerFunc
}

// Index is the answer to GET /api/v1.
type Index struct {
	API            string     `json:"api"`
	Version        string     `json:"version"`
	BaseURL        string     `json:"base_url"`
	Authentication string     `json:"authentication"`
	Rules          []string   `json:"rules"`
	Endpoints      []Endpoint `json:"endpoints"`
}

// Endpoints is every endpoint, in the order the index lists them.
func (a app) Endpoints() []Endpoint {
	return append([]Endpoint{{
		Method: http.MethodGet, Path: Prefix, Summary: "This index: every endpoint, what it takes, and the rules.",
		Returns: "This document.", handler: a.index,
	}}, slices.Concat(a.bookEndpoints(), a.keepEndpoints(), a.inboxEndpoints(), a.translationEndpoints())...)
}

func (a app) index(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, Index{
		API:     "Reconcile",
		Version: "v1",
		BaseURL: a.base,
		Authentication: "Send Authorization: Bearer <key> on every endpoint but this one. A person makes a key at " +
			a.base + "/account/keys, for what they say it is for; it acts as them, is shown once, and lasts 90 days, or a year for one that may only upload. " +
			"Each endpoint names the scope its key needs: upload, books:read, books:write (which includes books:read) or translate.",
		Rules: []string{
			"A key reaches only what its person may, and only for its scopes: something of somebody else's is answered 404, the same as something that does not exist.",
			"Reading the books changes nothing. Start with get_overview. Every statement, transaction, rule, receipt and project in an answer has a url: send the person there to see it, or to do what is theirs to do, such as reconciling a month.",
			"Amounts are strings with their sign, money in positive and money out negative, beside their currency. Dates are YYYY-MM-DD and months YYYY-MM.",
			"What the app counts -- a sum, a difference, whether a statement balances -- is the answer; do not work it out again from the lines.",
			"Keeping the books (books:write) changes what the person's role lets them change, and is marked: a transaction sorted and a rule written through a key say so on the site until the person saves them there, and every change is in the history with the key's name. Reconciling, reopening, removing a statement or receipt and accepting an explanation's difference are the person's alone, on the web; there is no endpoint for them.",
			"A translation is shown on every page as soon as it is written. A person looks it over afterwards, and one they approved or corrected is not changed through the API.",
			"Every {placeholder} in the English must be in the translation exactly as it is, untranslated; the app fills it in. Its place in the sentence may move.",
			"Translations are text, not markup: no < or > the English does not have.",
			"Spanish is Latin American, with tú; Portuguese is Brazilian, with você. Plain and short, as the English is. Use the glossary's words for the app's own terms.",
			"Sending a translation that is already there changes nothing, so a batch can safely be sent again.",
			`Every refusal of a whole request is {"error": {"field": "...", "problem": "..."}}, with a sentence saying what to fix.`,
		},
		Endpoints: a.Endpoints(),
	})
}

func (a app) notFound(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusNotFound, web.Problem("",
		fmt.Sprintf("There is no %s %s. GET %s lists every endpoint.", r.Method, r.URL.Path, Prefix)))
}

// number reads a whole number from the query, def when it is absent, and
// answers 400 itself when it is not one or is out of range.
func (a app) number(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, true
	}

	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, fmt.Sprintf("Give %s as a whole number from %d to %d.", name, lo, hi)))

		return 0, false
	}

	return n, true
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	web.WriteJSON(w, http.StatusInternalServerError, web.Problem("", "Something went wrong at our end. Try again in a few minutes."))
}
