// Package mcpapp is the API as a Model Context Protocol server at /mcp: how
// Claude on claude.ai, its desktop app and its phone app fill the
// interface's translations (docs/translations.md). It adds nothing the API
// does not do. Each tool is one endpoint of /api/v1, made from the API's own
// index and answered by sending the request to the API, in this process,
// with the key the tool call came with. Lifted from stewards.
//
// # Why through the API rather than beside it
//
// The rules a program is held to -- a translator's languages, placeholders
// kept, a person's approval never overwritten -- are the translation rules
// the API applies, and the index is built from the route table so that it
// cannot describe an endpoint that does not exist. Tools written by hand here
// would be a second description of the same endpoints, and a second place for
// a rule to be missed. Instead a tool is a row of the index, its input schema
// is the row's fields, and its call is an HTTP request to the row's path, so
// a new endpoint is a new tool with no change here, and a refusal is the
// API's own sentence. The index is read as any program reads it, over HTTP,
// because an app never imports another app.
//
// # What it adds
//
// Two things the API cannot give a chat. Sign-in: Claude on claude.ai has no
// file to keep a key in, so /mcp answers an unsigned request with the 401 that
// sends it through OAuth (oauthapp), which gives it a key. And the rules: the
// reconcile-api skill tells Claude Code how to translate well, and a chat on
// claude.ai has no skill, so the same guidance is sent as the server's
// instructions (guide.md).
//
// The protocol itself is the official Go SDK's: JSON-RPC, the streamable HTTP
// transport and the negotiation of protocol versions, which the standard
// library has no answer for and which move with the specification every few
// months. Keeping up with that is the SDK's job, not this repository's.
package mcpapp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The paths. The protected resource document is served at both places RFC
// 9728 allows: Claude tries the one with /mcp on the end first.
const (
	Path         = "/mcp"
	resourcePath = "/.well-known/oauth-protected-resource"
)

// indexPath is the API's index, which the tools are made from.
const indexPath = "/api/v1"

//go:embed guide.md
var guide string

// Keys is the slice of userbus this app uses: whether a key is somebody's.
type Keys interface {
	AuthenticateAPIKey(ctx context.Context, now time.Time, presented string) (userbus.User, error)
}

// Config is what this app needs.
type Config struct {
	Log  *slog.Logger
	Keys Keys

	// Site is the whole application, to send each tool's request to. A
	// function rather than a handler because this app is mounted inside
	// it: the muxer builds the site after mounting this, and the first
	// tool call is after both.
	Site func() http.Handler

	// BaseURL is the public origin: the resource's identity in the OAuth
	// documents.
	BaseURL string
}

type app struct {
	cfg      Config
	resource string // the URL of /mcp, which is what a token is for

	once   sync.Once
	server *mcp.Server
	err    error
}

// App is the MCP server and the document that says where to sign in to it.
type App struct{ a *app }

// New builds the app.
func New(cfg Config) App {
	return App{a: &app{cfg: cfg, resource: strings.TrimSuffix(cfg.BaseURL, "/") + Path}}
}

// Routes mounts the protected resource document, which is an ordinary GET
// for the site's own mux.
func (x App) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+resourcePath, x.a.resourceMetadata)
	mux.HandleFunc("GET "+resourcePath+Path, x.a.resourceMetadata)
}

// Handler is /mcp itself, for the muxer to mount on a branch of its own:
// its body is JSON, like the API's, and it is asked by a key rather than by
// a session cookie.
func (x App) Handler() http.Handler {
	a := x.a

	// Stateless, and every answer plain JSON rather than an event stream:
	// nothing here needs a session or streams, and an event stream through
	// Apache's proxy is a buffering question nobody needs to answer.
	//
	// The localhost protection is off because every request arrives that
	// way. It refuses a request that reached a loopback address with a
	// public Host header -- the DNS rebinding a server on somebody's own
	// computer is open to -- and that describes every request Apache
	// forwards to this one. What protects /mcp is that it needs a key.
	h := mcp.NewStreamableHTTPHandler(a.serverFor, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		JSONResponse:               true,
		DisableLocalhostProtection: true,
		Logger:                     a.cfg.Log,
	})

	return a.requireKey(h)
}

// ------------------------------------------------------------------ signing in

// resourceMetadata is the RFC 9728 document a client reads after a 401 to
// find where to sign in: this site, which is its own authorization server.
func (a *app) resourceMetadata(w http.ResponseWriter, _ *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]any{
		"resource":                 a.resource,
		"resource_name":            "Reconcile",
		"authorization_servers":    []string{strings.TrimSuffix(a.cfg.BaseURL, "/")},
		"bearer_methods_supported": []string{"header"},
	})
}

// requireKey refuses a request without a key, every method of
// the protocol included: a 401 with a pointer to the document above is what
// makes Claude sign in, and nothing here is any use without one.
func (a *app) requireKey(next http.Handler) http.Handler {
	challenge := fmt.Sprintf(`Bearer resource_metadata="%s"`, strings.TrimSuffix(a.cfg.BaseURL, "/")+resourcePath+Path)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, key, _ := strings.Cut(r.Header.Get("Authorization"), " ")

		if !strings.EqualFold(scheme, "Bearer") || key == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			web.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "error_description": "Sign in to Reconcile."})

			return
		}

		_, err := a.cfg.Keys.AuthenticateAPIKey(r.Context(), time.Now(), strings.TrimSpace(key))

		switch {
		case errors.Is(err, userbus.ErrDenied):
			// Revoked, expired, or never a key: the same answer, with
			// error="invalid_token", which tells Claude to sign in again
			// rather than to give up.
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			web.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "error_description": "That key has expired or been revoked. Connect again."})

			return
		case err != nil:
			a.cfg.Log.Error("an MCP request's key could not be checked", "request_id", web.RequestIDFrom(r.Context()), "error", err)
			web.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})

			return
		}

		next.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------ the server

// serverFor is the server, made once from the API's index on the first
// request: not at startup, because the index is read through the site, and
// the site is finished only after this app is mounted in it.
func (a *app) serverFor(r *http.Request) *mcp.Server {
	a.once.Do(func() { a.server, a.err = a.build(r.Context()) })

	if a.err != nil {
		// nil makes the SDK answer 400, and the log says why. Not
		// retried: an index that does not read once will not read the
		// next time either, and the fix is a deploy.
		a.cfg.Log.Error("the MCP server could not be made from the API's index", "error", a.err)

		return nil
	}

	return a.server
}

// index is as much of the API's index as tools are made from. Its own
// types rather than apiapp's, which an app may not import: it is read as
// any program reads it, as JSON.
type index struct {
	Rules     []string   `json:"rules"`
	Endpoints []endpoint `json:"endpoints"`
}

type endpoint struct {
	Method  string  `json:"method"`
	Path    string  `json:"path"`
	Summary string  `json:"summary"`
	Returns string  `json:"returns"`
	Tool    string  `json:"tool"`
	Query   []field `json:"query"`
	Body    *struct {
		Encoding string  `json:"encoding"`
		Fields   []field `json:"fields"`
	} `json:"body"`
}

type field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Values      []string `json:"values"`
	Description string   `json:"description"`
}

func (a *app) build(ctx context.Context) (*mcp.Server, error) {
	w := httptest.NewRecorder()
	a.cfg.Site().ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, indexPath, nil))

	if w.Code != http.StatusOK {
		return nil, fmt.Errorf("the index answered %d", w.Code)
	}

	var ix index
	if err := json.Unmarshal(w.Body.Bytes(), &ix); err != nil {
		return nil, fmt.Errorf("reading the index: %w", err)
	}

	instructions := guide + "\n## The API's own rules\n\n"
	for _, rule := range ix.Rules {
		instructions += "- " + rule + "\n"
	}

	s := mcp.NewServer(&mcp.Implementation{Name: "reconcile", Title: "Reconcile", Version: "v1"}, &mcp.ServerOptions{
		Instructions: instructions,
		Capabilities: &mcp.ServerCapabilities{}, // no logging
	})

	for _, e := range ix.Endpoints {
		if e.Tool == "" {
			continue
		}

		if e.Body != nil && e.Body.Encoding != "json" {
			return nil, fmt.Errorf("%s %s: a tool can only send JSON, not %s", e.Method, e.Path, e.Body.Encoding)
		}

		s.AddTool(toolFor(e), a.call(e))
	}

	return s, nil
}

// pathParam is a {name} in an endpoint's path.
var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// toolFor describes one endpoint as a tool: its path's parameters, its
// query and its body's fields, all as top-level arguments. No endpoint has
// a body field named as one of its path's parameters, which a test holds.
func toolFor(e endpoint) *mcp.Tool {
	props := map[string]any{}
	var required []string

	for _, m := range pathParam.FindAllStringSubmatch(e.Path, -1) {
		props[m[1]] = map[string]any{"type": "string", "description": "The " + m[1] + " in " + e.Path + "."}
		required = append(required, m[1])
	}

	var fields []field
	fields = append(fields, e.Query...)
	if e.Body != nil {
		fields = append(fields, e.Body.Fields...)
	}

	for _, f := range fields {
		props[f.Name] = schemaFor(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}

	in := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		in["required"] = required
	}

	reads := e.Method == http.MethodGet

	return &mcp.Tool{
		Name:        e.Tool,
		Description: e.Summary + "\n\nReturns: " + e.Returns,
		InputSchema: in,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    reads,
			DestructiveHint: new(e.Method == http.MethodDelete),
			IdempotentHint:  e.Method != http.MethodPost,
			OpenWorldHint:   new(false),
		},
	}
}

// schemaFor turns the index's description of a field's type into JSON
// Schema. The index writes types for a person to read ("array of object
// {label, url}"), and these are all the shapes it uses; anything else is
// left open rather than guessed at, and its description says what it takes.
func schemaFor(f field) map[string]any {
	s := typeSchema(f.Type)
	s["description"] = f.Description

	if len(f.Values) > 0 {
		if s["type"] == "array" {
			s["items"].(map[string]any)["enum"] = f.Values
		} else {
			s["enum"] = f.Values
		}
	}

	return s
}

func typeSchema(t string) map[string]any {
	if item, ok := strings.CutPrefix(t, "array of "); ok {
		return map[string]any{"type": "array", "items": typeSchema(item)}
	}

	if inner, ok := strings.CutPrefix(t, "object {"); ok {
		props := map[string]any{}
		for name := range strings.SplitSeq(strings.TrimSuffix(inner, "}"), ",") {
			name = strings.TrimSpace(name)

			props[name] = map[string]any{"type": "string"}
		}

		return map[string]any{"type": "object", "properties": props}
	}

	switch t {
	case "string", "integer", "boolean", "number":
		return map[string]any{"type": t}
	}

	return map[string]any{}
}

// ------------------------------------------------------------------ calling

// call is a tool's handler: the endpoint's request, made from the
// arguments, sent to the site with the key the tool call came with.
func (a *app) call(e endpoint) mcp.ToolHandler {
	params := pathParam.FindAllStringSubmatch(e.Path, -1)

	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return failed("The arguments are not a JSON object: " + err.Error()), nil
			}
		}

		// The path, its parameters filled in and taken out of the
		// arguments, so what is left is the query or the body.
		path := e.Path
		for _, m := range params {
			v, _ := args[m[1]].(string)
			if v == "" {
				return failed(m[1] + " is required."), nil
			}

			path = strings.Replace(path, m[0], url.PathEscape(v), 1)
			delete(args, m[1])
		}

		var body io.Reader
		if e.Body != nil {
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}

			body = bytes.NewReader(raw)
		} else if len(e.Query) > 0 {
			q := url.Values{}
			for _, f := range e.Query {
				if v, ok := args[f.Name]; ok {
					q.Set(f.Name, fmt.Sprint(v))
				}
			}

			if len(q) > 0 {
				path += "?" + q.Encode()
			}
		}

		w := a.send(ctx, req, e.Method, path, body)

		return answer(w), nil
	}
}

// send is one request to the site, as the tool call's person.
func (a *app) send(ctx context.Context, req *mcp.CallToolRequest, method, path string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, method, path, body)
	if body != nil {
		r.Header.Set("Content-Type", web.JSON)
	}

	if req.Extra != nil && req.Extra.Header != nil {
		r.Header.Set("Authorization", req.Extra.Header.Get("Authorization"))
	}

	w := httptest.NewRecorder()
	a.cfg.Site().ServeHTTP(w, r)

	return w
}

// answer is the API's answer as a tool's result: its JSON as text, and a
// refusal as an error whose text is the API's own sentence, so Claude reads
// what to fix.
func answer(w *httptest.ResponseRecorder) *mcp.CallToolResult {
	text := strings.TrimSpace(w.Body.String())
	if w.Code >= 400 {
		return failed(fmt.Sprintf("%d %s: %s", w.Code, http.StatusText(w.Code), text))
	}

	if text == "" {
		text = fmt.Sprintf("%d %s", w.Code, http.StatusText(w.Code))
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func failed(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
