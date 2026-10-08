// Package apiapp is the JSON API at /api/v1: how a program -- a person's
// own Claude -- fills the interface's translations without typing them into
// a screen (docs/translations.md).
//
// # What is here, and what never will be
//
// Translations, and nothing else. A key acts as the person who made it
// (mid.APIKey), and this app sells a stolen one cheaply on purpose: nothing
// under /api/v1 reads an organization, an account, a project, a statement, a
// receipt or an export, and nothing will without a plan of its own, with
// scopes. Who may use these endpoints at all is the translation domain's
// rule (translationbus.MayTranslate): the site administrator, and the
// translators they name on the admin page, each for their languages.
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
	"strconv"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/foundation/web"
)

// Prefix is where the API lives. The version is in the path, so a v2 can sit
// beside it while a script written for v1 still works.
const Prefix = "/api/v1"

// Config is what this app needs.
type Config struct {
	Log          *slog.Logger
	Translations *translationbus.Business

	// BaseURL is the public origin, for the links in answers: a program
	// reading them may be anywhere.
	BaseURL string
}

type app struct {
	log          *slog.Logger
	translations *translationbus.Business
	base         string
}

// Routes mounts the API on its own mux. Each route that needs a key is
// behind mid.RequireKey; mid.APIKey, which reads the key, is the muxer's to
// put around the whole of it.
func Routes(mux *http.ServeMux, cfg Config) {
	a := app{log: cfg.Log, translations: cfg.Translations, base: cfg.BaseURL}
	require := mid.RequireKey()

	for _, e := range a.Endpoints() {
		h := http.Handler(e.handler)
		if e.NeedsKey {
			h = require(h)
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
	Method   string  `json:"method"`
	Path     string  `json:"path"`
	Summary  string  `json:"summary"`
	NeedsKey bool    `json:"needs_key"`
	Query    []Field `json:"query,omitempty"`
	Body     *Body   `json:"body,omitempty"`
	Returns  string  `json:"returns"`

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
	}}, a.translationEndpoints()...)
}

func (a app) index(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, Index{
		API:     "Reconcile",
		Version: "v1",
		BaseURL: a.base,
		Authentication: "Send Authorization: Bearer <key> on every endpoint but this one. A translator makes a key at " +
			a.base + "/account/keys; it acts as them, is shown once, and lasts 90 days.",
		Rules: []string{
			"This API is the interface's translations and nothing else: no organization, account, statement, receipt or export is reachable here.",
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
