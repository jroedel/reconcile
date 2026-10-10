// Package oauthapp is how a program signs in as a person through OAuth: the
// discovery document, the page where a person agrees, and the token endpoint
// that hands the program its key. Lifted from stewards.
//
// It exists for Claude on claude.ai and its phone app (a "custom connector"),
// which can only reach a server that signs in this way: there is no file
// there to keep a key in. What the program is given is an ordinary API key
// (userbus/oauth.go), with the scopes the person agreed to, so what it then
// reaches is exactly what a key from /account/keys for the same purposes
// reaches (docs/books-api.md, "Scopes").
//
// # Who may ask, and who may agree
//
// Programs are known by a Client ID Metadata Document (foundation/oauth): the
// client_id is an HTTPS URL, and the document there says where the code may
// be sent. Only documents on Anthropic's hosts are read. Any program could
// publish a document, and a person who is shown "Let some-app.example work
// as you?" by a link in a message is the phishing this page would otherwise
// be. Claude is the program this was built for; another is a decision for
// the site, and a line in trustedHosts.
//
// Anybody signed in may agree. What the key may do is what they may: read
// their books, as far as their roles reach, and -- for the administrator
// and translators -- translate. The page says which before they agree.
//
// # The two kinds of refusal
//
// RFC 6749 is particular about this, and it is a security rule rather than a
// style. A request whose program or redirect cannot be trusted is answered
// here, on our own page, and never sent anywhere: redirecting with an error
// to an unchecked redirect_uri is an open redirect with our name on it. Every
// other refusal -- a missing PKCE challenge, the person saying no -- goes
// back to the program at its checked redirect, which is how it finds out.
package oauthapp

import (
	"cmp"
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
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/oauth"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates
var files embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = files

// The paths. The discovery document's is fixed by RFC 8414 for an issuer
// with no path, which this site's origin is.
const (
	MetadataPath  = "/.well-known/oauth-authorization-server"
	AuthorizePath = "/oauth/authorize"
	TokenPath     = "/oauth/token"
)

// trustedHosts are the hosts whose metadata documents are read, with their
// subdomains. Claude's are on claude.ai today; the other two are
// Anthropic's own, so that Claude moving its document between them is not a
// failed connection to debug.
var trustedHosts = []string{"claude.ai", "claude.com", "anthropic.com"}

// Users is the slice of userbus this app uses.
type Users interface {
	GrantAccess(ctx context.Context, now time.Time, userID types.ID, clientID, name, redirect, challenge string, scopes []userbus.Scope) (string, error)
	RedeemGrant(ctx context.Context, now time.Time, presented, clientID, redirect, verifier string) (userbus.APIKey, string, error)
}

// Translators says who may translate, which is whether a program connected
// as them may translate too.
type Translators interface {
	MayTranslateAny(ctx context.Context, who translationbus.Translator) (bool, error)
}

// Clients reads a program's metadata document.
type Clients interface {
	Fetch(ctx context.Context, clientID string) (oauth.Client, error)
}

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Config is what this app needs.
type Config struct {
	Log         *slog.Logger
	Render      Renderer
	Users       Users
	Translators Translators
	Clients     Clients

	// BaseURL is the public origin, which is the issuer: the discovery
	// document and the iss on every answer say it, and a program checks
	// that they agree.
	BaseURL string

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type app struct {
	cfg  Config
	meta oauth.ServerMetadata
}

// Routes mounts the app. The page where a person agrees is behind guard, so
// that somebody not signed in is sent to sign in and brought back; the
// discovery document and the token endpoint are for programs, and are not.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg, meta: oauth.NewServerMetadata(cfg.BaseURL, AuthorizePath, TokenPath)}

	mux.HandleFunc("GET "+MetadataPath, a.metadata)
	mux.Handle("GET "+AuthorizePath, guard(http.HandlerFunc(a.ask)))
	mux.Handle("POST "+AuthorizePath, guard(http.HandlerFunc(a.answer)))
	mux.HandleFunc("POST "+TokenPath, a.token)
}

func (a app) metadata(w http.ResponseWriter, _ *http.Request) {
	web.WriteJSON(w, http.StatusOK, a.meta)
}

// --- the request ------------------------------------------------------------------

// request is an authorization request, from the query on the way in and from
// the form's hidden fields on the way out. Both are read the same way and
// checked the same way: the hidden fields are only the query carried across
// one page, and a person's browser can change them as easily.
type request struct {
	ClientID, Redirect, State, Challenge, Method, ResponseType string
}

func requestFrom(v url.Values) request {
	return request{
		ClientID:     v.Get("client_id"),
		Redirect:     v.Get("redirect_uri"),
		State:        v.Get("state"),
		Challenge:    v.Get("code_challenge"),
		Method:       v.Get("code_challenge_method"),
		ResponseType: v.Get("response_type"),
	}
}

// client reads and checks the program and its redirect: the checks whose
// failure is answered on our own page, as a problem the page words.
func (a app) client(ctx context.Context, req request) (oauth.Client, string, error) {
	if !trusted(req.ClientID) {
		a.cfg.Log.Warn("an OAuth request from a program that is not trusted", "client_id", req.ClientID)

		return oauth.Client{}, "not-claude", errUntrusted
	}

	c, err := a.cfg.Clients.Fetch(ctx, req.ClientID)
	if err != nil {
		a.cfg.Log.Warn("an OAuth program's metadata document could not be used", "client_id", req.ClientID, "error", err)

		return oauth.Client{}, "cannot-read-client", err
	}

	if !c.RedirectAllowed(req.Redirect) {
		a.cfg.Log.Warn("an OAuth request asked to go back somewhere its program does not list", "client_id", req.ClientID, "redirect_uri", req.Redirect)

		return oauth.Client{}, "bad-redirect", errBadRedirect
	}

	return c, "", nil
}

var (
	errUntrusted   = errors.New("the client is not on a trusted host")
	errBadRedirect = errors.New("the redirect is not one the client lists")
)

func trusted(clientID string) bool {
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" {
		return false
	}

	host := u.Hostname()
	for _, t := range trustedHosts {
		if host == t || strings.HasSuffix(host, "."+t) {
			return true
		}
	}

	return false
}

// refusal is what a request that passed client() lacks, as the OAuth error
// to send back, or "" for nothing.
func (req request) refusal() (code, description string) {
	switch {
	case req.ResponseType != "code":
		return "unsupported_response_type", "only the authorization code flow is offered"
	case req.Method != "S256" || !oauth.ValidChallenge(req.Challenge):
		return "invalid_request", "a PKCE code_challenge made with S256 is required"
	}

	return "", ""
}

// back sends the person to the program with params, and the state and
// issuer every answer carries.
func (a app) back(w http.ResponseWriter, r *http.Request, req request, params url.Values) {
	params.Set("state", req.State)
	params.Set("iss", a.meta.Issuer)

	to, err := oauth.RedirectWith(req.Redirect, params)
	if err != nil {
		// Checked against the program's document already, so this is a
		// document listing something that is not a URL.
		a.refuse(w, r, http.StatusBadRequest, "bad-redirect")

		return
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// --- the page ---------------------------------------------------------------------

type connectView struct {
	Request request

	Name    string // what the document calls itself
	Host    string // who is asking, as the client_id says
	KeyName string // what /account/keys will list
	BackTo  string // where Allow sends the person
	Email   string

	// Translates is whether the key will translate as well as read.
	Translates bool

	// Problem is a code the page words.
	Problem string
}

// scopesFor is what a program connected as this person may do: keep their
// books, and translate if they may. The consent's box narrows keeping to
// reading (answer); claude.ai sends no scope worth trusting, so what the
// person chose on the page is the key's scopes.
func (a app) scopesFor(w http.ResponseWriter, r *http.Request) (userbus.User, []userbus.Scope, bool) {
	me, _ := mid.UserFrom(r.Context())

	ok, err := a.cfg.Translators.MayTranslateAny(r.Context(), translationbus.Translator{ID: me.ID, SiteAdmin: me.SiteAdmin})
	if err != nil {
		a.cfg.Log.Error("whether somebody translates could not be read", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.refuse(w, r, http.StatusInternalServerError, "server")

		return userbus.User{}, nil, false
	}

	scopes := []userbus.Scope{userbus.BooksWrite}
	if ok {
		scopes = append(scopes, userbus.Translate)
	}

	return me, scopes, true
}

// ask is the page where a person agrees, or not.
func (a app) ask(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.URL.Query())

	c, problem, err := a.client(r.Context(), req)
	if err != nil {
		a.refuse(w, r, http.StatusBadRequest, problem)

		return
	}

	if code, desc := req.refusal(); code != "" {
		a.back(w, r, req, url.Values{"error": {code}, "error_description": {desc}})

		return
	}

	_, scopes, ok := a.scopesFor(w, r)
	if !ok {
		return
	}

	a.show(w, r, http.StatusOK, c, req, scopes, "")
}

// answer is the person's yes or no.
func (a app) answer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.refuse(w, r, http.StatusBadRequest, "cannot-read")

		return
	}

	req := requestFrom(r.PostForm)

	c, problem, err := a.client(r.Context(), req)
	if err != nil {
		a.refuse(w, r, http.StatusBadRequest, problem)

		return
	}

	if code, desc := req.refusal(); code != "" {
		a.back(w, r, req, url.Values{"error": {code}, "error_description": {desc}})

		return
	}

	me, scopes, ok := a.scopesFor(w, r)
	if !ok {
		return
	}

	if r.PostForm.Get("answer") != "allow" {
		a.cfg.Log.Info("somebody said no to an OAuth program", "user_id", me.ID.String(), "client_id", req.ClientID)
		a.back(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"the person said no"}})

		return
	}

	if r.PostForm.Get("reading") == "only" {
		for i, s := range scopes {
			if s == userbus.BooksWrite {
				scopes[i] = userbus.BooksRead
			}
		}
	}

	code, err := a.cfg.Users.GrantAccess(r.Context(), a.cfg.Now(), me.ID, req.ClientID, keyName(c), req.Redirect, req.Challenge, scopes)

	switch {
	case errors.Is(err, userbus.ErrTooManyGrants):
		a.show(w, r, http.StatusTooManyRequests, c, req, scopes, "too-many")

		return
	case errors.Is(err, userbus.ErrClient), errors.Is(err, userbus.ErrChallenge):
		a.back(w, r, req, url.Values{"error": {"invalid_request"}})

		return
	case err != nil:
		a.cfg.Log.Error("an OAuth grant could not be made", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.back(w, r, req, url.Values{"error": {"server_error"}})

		return
	}

	a.back(w, r, req, url.Values{"code": {code}})
}

func (a app) show(w http.ResponseWriter, r *http.Request, status int, c oauth.Client, req request, scopes []userbus.Scope, problem string) {
	me, _ := mid.UserFrom(r.Context())

	back, _ := url.Parse(req.Redirect) // checked in client()

	// The form's answer is a redirect to the program, which form-action
	// must allow; see page.AllowFormTo.
	page.AllowFormTo(w.Header(), back.Scheme+"://"+back.Host)

	a.cfg.Render.Render(w, r, status, "connect", connectView{
		Request: req,
		Name:    cmp.Or(c.Name, c.Host()),
		Host:    c.Host(),
		KeyName: keyName(c),
		BackTo:  back.Host,
		Email:   me.Email.String(),
		Problem: problem,

		Translates: userbus.Allows(scopes, userbus.Translate),
	})
}

func (a app) refuse(w http.ResponseWriter, r *http.Request, status int, problem string) {
	a.cfg.Render.Render(w, r, status, "connect", connectView{Problem: problem})
}

// keyName is what /account/keys lists the program's key as: its own name,
// and the host its document is on, since the name is only what the document
// says about itself.
func keyName(c oauth.Client) string {
	if c.Name == "" {
		return c.Host()
	}

	return c.Name + " (" + c.Host() + ")"
}

// --- the token --------------------------------------------------------------------

// token trades a code for the key. A program's request, not a person's: the
// answers are the RFC's JSON, never a page.
func (a app) token(w http.ResponseWriter, r *http.Request) {
	// A token is a credential. Neither it nor a refusal may be cached by
	// anything between here and the program.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	if err := r.ParseForm(); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.InvalidRequest, Description: "the body could not be read as a form"})

		return
	}

	f := r.PostForm

	if f.Get("grant_type") != "authorization_code" {
		web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.UnsupportedGrantType, Description: "only authorization_code is offered"})

		return
	}

	for _, name := range []string{"code", "client_id", "redirect_uri", "code_verifier"} {
		if f.Get(name) == "" {
			web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.InvalidRequest, Description: name + " is required"})

			return
		}
	}

	now := a.cfg.Now()

	k, key, err := a.cfg.Users.RedeemGrant(r.Context(), now, f.Get("code"), f.Get("client_id"), f.Get("redirect_uri"), f.Get("code_verifier"))

	switch {
	case errors.Is(err, userbus.ErrDenied):
		web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.InvalidGrant, Description: "the code is not valid: it may have been used, have expired, or belong to another request"})

		return
	case err != nil:
		a.cfg.Log.Error("an OAuth code could not be traded for a key", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		web.WriteJSON(w, http.StatusInternalServerError, oauth.TokenError{Code: "server_error"})

		return
	}

	web.WriteJSON(w, http.StatusOK, oauth.Token{
		AccessToken: key,
		TokenType:   "Bearer",
		ExpiresIn:   int64(k.ExpiresAt.Sub(now) / time.Second),
	})
}
