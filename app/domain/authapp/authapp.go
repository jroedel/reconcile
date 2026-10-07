// Package authapp is the sign-in surface: asking for a code, typing it in,
// backup codes, the one-time bootstrap, and the account page. Adapted from
// mass-intentions.
//
// # A code, typed where it was asked for
//
// Signing in is an address, then six digits from an email typed into the page
// that asked for them. Not a link: a link in Mail opens in whichever browser
// the phone prefers, which on an iPhone is Safari even for somebody using the
// app from their Home Screen, so it signs in the wrong one. A code goes
// wherever it is typed. See userbus.Token for why it is bound to the browser
// that asked, which is the half held in the pending cookie (pending.go).
//
// # What the sign-in page will not tell you
//
// Asking for a code leads to the same page whatever happens: a new address, a
// known one, a disabled one, an address at its limit, and a mail relay that
// failed. Sign-up is open, so whether an address has an account here is
// nobody's business but its owner's, and the one way to keep it so is never
// to vary the answer. The recovery paths when mail is broken are the backup
// codes and the bootstrap secret, which is why both exist.
//
// # Words
//
// Every sentence is in a template, translated there. A handler names what
// went wrong with a short code -- view.Problem = "bad-email" -- and
// templates/partials/problem.html says it in the reader's language.
package authapp

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates mail
var files embed.FS

// Templates is this app's pages and messages, for page.NewRenderer.
var Templates fs.FS = files

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
	Mail(lang types.Lang, name string, data any) (subject, body string, err error)
}

// Paths other apps link to.
const (
	SignInPath = "/sign-in"
	home       = "/"
)

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Users  *userbus.Business
	Render Renderer

	// Mail may be nil: no relay configured. A code is then logged as not
	// sent, and the page says what it always says.
	Mail mail.Sender

	// BaseURL is the public origin, named in the messages so that somebody
	// can tell which site sent them. From configuration, never from the
	// request: a Host header is whatever the sender liked.
	BaseURL string

	// Bootstrap is the one-time secret from the config file. Empty means its
	// page is not mounted at all.
	Bootstrap string

	// TrustProxy says whether X-Forwarded-For carries the visitor's address,
	// which decides what the throttle counts (web.ClientIP).
	TrustProxy bool

	// Limit is how often one address may present a credential or ask for a
	// code. The zero value is [DefaultRate].
	Limit web.Rate

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// DefaultRate is what the sign-in routes are held to: five at once, then one
// every thirty seconds. Generous for somebody who mistypes their address,
// asks again and misreads a digit; ungenerous for anything working through a
// list of addresses or of six-digit numbers.
//
// Keyed by the visitor's network address and not by the email typed, which
// would mean reading the body in a middleware. What stands behind it for one
// address being sent a hundred codes from a hundred hosts is userbus: five
// live codes per address, and a ceiling for the whole service.
func DefaultRate() web.Rate {
	return web.Rate{Burst: 5, Every: 30 * time.Second}
}

type app struct {
	cfg Config
}

// Routes mounts this app. guard is mid.Require, for the account pages.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Limit.Zero() {
		cfg.Limit = DefaultRate()
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	// One allowance across every way of presenting a credential or asking
	// for mail to be sent, deliberately: a limit that let somebody exhaust
	// the backup codes and then start on the emailed codes would be several
	// limits and no limit.
	//
	// The GETs are not behind it. They render a form and read nothing, and
	// a throttle on the page somebody is signing in on locks out the person
	// who reloaded it.
	tries := web.Throttle(web.Throttling{
		Rate: cfg.Limit,
		Key: func(r *http.Request) string {
			return "signin|" + web.IPBucket(web.ClientIP(r, cfg.TrustProxy))
		},
		Log: cfg.Log,
		Now: cfg.Now,
	})

	limited := func(h http.HandlerFunc) http.Handler { return tries(h) }

	mux.HandleFunc("GET /sign-in", a.signInForm)
	mux.Handle("POST /sign-in", limited(a.requestCode))
	mux.HandleFunc("GET /sign-in/code", a.codeForm)
	mux.Handle("POST /sign-in/code", limited(a.redeemCode))
	mux.HandleFunc("GET /sign-in/backup", a.backupForm)
	mux.Handle("POST /sign-in/backup", limited(a.redeemBackup))

	// Only when there is a secret to compare against. An unmounted route is
	// a 404, which is a better answer than a form that can never succeed.
	if cfg.Bootstrap != "" {
		mux.HandleFunc("GET /sign-in/first", a.firstForm)
		mux.Handle("POST /sign-in/first", limited(a.redeemFirst))
	}

	// Not throttled: it is somebody ending a session they hold, and a
	// refusal there leaves open a session its owner asked to close.
	mux.HandleFunc("POST /sign-out", a.signOut)

	mux.Handle("GET /account", guard(http.HandlerFunc(a.account)))
	mux.Handle("POST /account/profile", guard(http.HandlerFunc(a.setProfile)))
	mux.Handle("POST /account/backup-codes", guard(http.HandlerFunc(a.issueCodes)))
	mux.Handle("POST /account/sign-out-everywhere", guard(http.HandlerFunc(a.signOutEverywhere)))

	// Asking to move sends mail to an address the user typed, so it is
	// throttled like asking for a sign-in code.
	mux.Handle("POST /account/email", guard(limited(a.requestEmailChange)))
	mux.Handle("POST /account/email/confirm", guard(limited(a.confirmEmailChange)))
	mux.Handle("POST /account/email/cancel", guard(http.HandlerFunc(a.cancelEmailChange)))
}

// --- asking for a code ---------------------------------------------------------

type signInView struct {
	Next      string
	Email     string
	Problem   string
	Bootstrap bool
}

func (a app) signInForm(w http.ResponseWriter, r *http.Request) {
	// Already signed in: there is nothing to do here.
	if _, ok := mid.UserFrom(r.Context()); ok {
		http.Redirect(w, r, next(r.URL.Query().Get("next")), http.StatusSeeOther)

		return
	}

	// An address in the query fills the field in: an invitation's link puts
	// one there, so that somebody does not have to work out which of their
	// addresses they were invited at. Parsed rather than echoed, and dropped
	// when it does not parse: a sign-in field filled with a sentence
	// somebody else wrote is still somebody else's sentence on our page.
	view := signInView{Next: mid.SafeNext(r.URL.Query().Get("next")), Bootstrap: a.cfg.Bootstrap != ""}

	if email, err := types.ParseEmail(r.URL.Query().Get("email")); err == nil {
		view.Email = email.String()
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "signin", view)
}

func (a app) requestCode(w http.ResponseWriter, r *http.Request) {
	view := signInView{Bootstrap: a.cfg.Bootstrap != ""}

	if err := r.ParseForm(); err != nil {
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "signin", view)

		return
	}

	view.Next = mid.SafeNext(r.PostFormValue("next"))
	view.Email = r.PostFormValue("email")

	email, err := types.ParseEmail(view.Email)
	if err != nil {
		// The one thing this page complains about, and it reveals nothing:
		// it is about the text, not about whether anybody holds it.
		view.Problem = "bad-email"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "signin", view)

		return
	}

	req, err := a.cfg.Users.RequestSignIn(r.Context(), a.cfg.Now(), email)
	if err != nil {
		a.cfg.Log.Error("a sign-in code could not be prepared", "request_id", web.RequestIDFrom(r.Context()), "error", err)

		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "signin", view)

		return
	}

	if req.Sendable() {
		a.send(r, mid.LangFrom(r.Context()), email, "signin-code", codeMail{Code: req.Code, Site: a.cfg.BaseURL, Email: email.String()})
	}

	// The cookie and the page are the same either way, including when the
	// send above failed. See the package comment.
	setPending(w, signInCookie, req.Pending, email)

	to := "/sign-in/code"
	if view.Next != "" {
		to += "?next=" + url.QueryEscape(view.Next)
	}

	// Redirected rather than rendered, so that reloading the code page does
	// not post the address again and send another code.
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// codeMail is what a message carrying a code is written from.
type codeMail struct {
	Code  string
	Site  string
	Email string
}

// send mails a message and treats a failure as something to record rather
// than something to report: the page has already decided what it says.
func (a app) send(r *http.Request, lang types.Lang, to types.Email, name string, data any) {
	log := a.cfg.Log.With("request_id", web.RequestIDFrom(r.Context()), "message", name)

	if a.cfg.Mail == nil {
		log.Error("a message could not be sent because no mail relay is configured")

		return
	}

	subject, body, err := a.cfg.Render.Mail(lang, name, data)
	if err != nil {
		log.Error("a message could not be written", "error", err)

		return
	}

	if err := a.cfg.Mail.Send(r.Context(), mail.Message{To: to.String(), Subject: subject, Text: body}); err != nil {
		// Loud, because this leaves somebody staring at a page telling them
		// to check an inbox nothing will arrive in, and the log is the only
		// place it can be said.
		log.Error("a message could not be sent", "error", err)
	}
}

// --- typing it in --------------------------------------------------------------

type codeView struct {
	Email   string
	Next    string
	Problem string
}

// codeForm is the page that takes the emailed code. Without a request
// outstanding there is nothing to type a code against, so it is the address
// form.
func (a app) codeForm(w http.ResponseWriter, r *http.Request) {
	pending, email := readPending(r, signInCookie)
	if pending == "" {
		http.Redirect(w, r, SignInPath, http.StatusSeeOther)

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "code", codeView{
		Email: email,
		Next:  mid.SafeNext(r.URL.Query().Get("next")),
	})
}

func (a app) redeemCode(w http.ResponseWriter, r *http.Request) {
	pending, email := readPending(r, signInCookie)
	view := codeView{Email: email}

	if err := r.ParseForm(); err != nil {
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "code", view)

		return
	}

	view.Next = mid.SafeNext(r.PostFormValue("next"))

	// The cookie has gone: fifteen minutes passed, or the code was typed in
	// a different browser from the one that asked. Said plainly, because
	// "that code did not work" about a code somebody is reading off their
	// screen makes them think they cannot read.
	if pending == "" {
		view.Problem = "stale"
		a.cfg.Render.Render(w, r, http.StatusUnauthorized, "code", view)

		return
	}

	typed := r.PostFormValue("code")

	// Checked here as well as in userbus so that the sentence can be about
	// the text. A code of the wrong length is a typo, not a guess, and costs
	// no try.
	if !userbus.LooksLikeACode(typed) {
		view.Problem = "code-shape"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "code", view)

		return
	}

	user, session, created, err := a.cfg.Users.SignIn(r.Context(), a.cfg.Now(), pending, typed)
	if err == nil {
		clearPending(w, signInCookie)
	}

	view.Problem = "code-wrong"

	// Somebody new lands on their account page, to give a name and keep
	// some backup codes, before anything else.
	if created && view.Next == "" {
		view.Next = "/account?welcome=1"
	}

	a.finish(w, r, user, session, err, "code", view, view.Next)
}

// --- backup codes --------------------------------------------------------------

type backupView struct {
	Email   string
	Next    string
	Problem string
}

func (a app) backupForm(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "backup", backupView{Next: mid.SafeNext(r.URL.Query().Get("next"))})
}

func (a app) redeemBackup(w http.ResponseWriter, r *http.Request) {
	var view backupView

	if err := r.ParseForm(); err != nil {
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "backup", view)

		return
	}

	view.Next = mid.SafeNext(r.PostFormValue("next"))
	view.Email = r.PostFormValue("email")

	email, err := types.ParseEmail(view.Email)
	if err != nil {
		view.Problem = "bad-email"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "backup", view)

		return
	}

	user, session, err := a.cfg.Users.SignInWithBackupCode(r.Context(), a.cfg.Now(), email, r.PostFormValue("code"))

	view.Problem = "backup-wrong"
	a.finish(w, r, user, session, err, "backup", view, view.Next)
}

// --- the first sign-in ---------------------------------------------------------

type firstView struct {
	Email   string
	Problem string
	Spent   bool
}

// firstForm offers the one-time secret, or says it has been used.
//
// The secret stays in the configuration file after it is spent, so this page
// goes on being mounted, and anybody who finds it afterwards should be told
// the door is shut rather than handed a form that refuses everything. Saying
// so reveals nothing a stranger could use: that the site has been set up is
// plain from its having users.
func (a app) firstForm(w http.ResponseWriter, r *http.Request) {
	spent, err := a.cfg.Users.BootstrapSpent(r.Context())
	if err != nil {
		// Not a reason to refuse the page: the claim it posts to is still
		// the only thing that decides.
		a.cfg.Log.Error("whether the bootstrap secret is spent could not be read",
			"request_id", web.RequestIDFrom(r.Context()), "error", err)
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "first", firstView{Spent: spent})
}

func (a app) redeemFirst(w http.ResponseWriter, r *http.Request) {
	var view firstView

	if err := r.ParseForm(); err != nil {
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "first", view)

		return
	}

	view.Email = r.PostFormValue("email")

	email, err := types.ParseEmail(view.Email)
	if err != nil {
		view.Problem = "bad-email"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "first", view)

		return
	}

	user, session, err := a.cfg.Users.Bootstrap(r.Context(), a.cfg.Now(), a.cfg.Bootstrap, r.PostFormValue("secret"), email)

	view.Problem = "first-wrong"

	// To the account page: the first thing the administrator needs is a set
	// of backup codes, because until mail works they are the only way back.
	a.finish(w, r, user, session, err, "first", view, "/account?welcome=1")
}

// --- the shared tail -----------------------------------------------------------

// finish sets the session and goes on, or re-renders the page that was tried
// with its refusal. The refusal is the caller's, because userbus says nothing
// but ErrDenied on purpose.
func (a app) finish(w http.ResponseWriter, r *http.Request, user userbus.User, session string, err error, page string, view any, to string) {
	switch {
	case errors.Is(err, userbus.ErrDenied):
		// 401 rather than 200, so that a refused attempt is visible in the
		// log and to anything watching for a burst of them.
		a.cfg.Render.Render(w, r, http.StatusUnauthorized, page, view)

		return
	case err != nil:
		a.cfg.Log.Error("a sign-in failed unexpectedly", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "failed", "server")

		return
	}

	mid.SetSession(w, session, a.cfg.Now().Add(userbus.SessionLife))

	a.cfg.Log.Info("signed in", "request_id", web.RequestIDFrom(r.Context()), "user_id", user.ID.String())

	http.Redirect(w, r, next(to), http.StatusSeeOther)
}

func (a app) signOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(mid.SessionCookie); err == nil {
		if err := a.cfg.Users.SignOut(r.Context(), c.Value); err != nil {
			a.cfg.Log.Error("a session could not be ended", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		}
	}

	// Cleared whatever happened above. Leaving the cookie after somebody
	// pressed sign out is the worse failure of the two.
	mid.ClearSession(w)

	http.Redirect(w, r, home, http.StatusSeeOther)
}

// --- the account page ----------------------------------------------------------

type accountView struct {
	Email string
	Name  string
	Lang  types.Lang
	Langs []types.Lang

	// Pending is an address the user has asked to move to and not yet
	// proved; Codes the backup codes just issued, shown this once; Left how
	// many unused ones they have.
	Pending string
	Codes   []string
	Left    int

	// Welcome is the first visit after signing up. Done is what just
	// happened, when it went well, and Problem what was wrong with what was
	// just posted, both as codes the template words.
	Welcome bool
	Done    string
	Problem string

	// NewEmail is what was typed into the change form, kept when it is
	// refused.
	NewEmail string
}

// accountFor builds the page. One function, so that the four places that
// render it cannot each leave out a different field.
func (a app) accountFor(ctx context.Context, u userbus.User) accountView {
	view := accountView{Email: u.Email.String(), Name: u.Name, Lang: u.Lang, Langs: types.Langs}

	// Neither of these is worth failing the page for: its job is to show the
	// account, and a missing line is worth less than the page.
	if addr, waiting, err := a.cfg.Users.PendingEmailChange(ctx, a.cfg.Now(), u.ID); err != nil {
		a.cfg.Log.Error("the pending address change could not be read", "user_id", u.ID.String(), "error", err)
	} else if waiting {
		view.Pending = addr.String()
	}

	if left, err := a.cfg.Users.BackupCodesLeft(ctx, u.ID); err != nil {
		a.cfg.Log.Error("the backup codes could not be counted", "user_id", u.ID.String(), "error", err)
	} else {
		view.Left = left
	}

	return view
}

// signedIn is the user behind a guarded route. Unreachable without one;
// answered rather than assumed, so that a route mounted without the guard
// fails by sending somebody to sign in rather than by acting on a zero user.
func signedIn(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, SignInPath, http.StatusSeeOther)
	}

	return u, ok
}

func (a app) account(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	view := a.accountFor(r.Context(), u)
	view.Welcome = r.URL.Query().Get("welcome") != ""
	view.Done = r.URL.Query().Get("done")

	a.cfg.Render.Render(w, r, http.StatusOK, "account", view)
}

func (a app) setProfile(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		view := a.accountFor(r.Context(), u)
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "account", view)

		return
	}

	// "" is "follow this device", and anything else that does not parse is
	// a form somebody wrote by hand: refused by userbus, below.
	lang := types.Lang(r.PostFormValue("lang"))

	if _, err := a.cfg.Users.SetProfile(r.Context(), a.cfg.Now(), u.ID, r.PostFormValue("name"), lang); err != nil {
		view := a.accountFor(r.Context(), u)

		if errors.Is(err, userbus.ErrInvalid) {
			view.Problem = "profile-invalid"
			a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "account", view)

			return
		}

		a.cfg.Log.Error("a profile could not be saved", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)
		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account", view)

		return
	}

	// This browser follows the choice at once: the cookie outranks the
	// account (mid.Lang), so it is set to match, or cleared for "follow this
	// device".
	mid.SetLang(w, lang)

	// Redirected rather than rendered, so that a refresh does not post the
	// form again.
	http.Redirect(w, r, "/account?done=profile", http.StatusSeeOther)
}

func (a app) issueCodes(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	codes, err := a.cfg.Users.IssueBackupCodes(r.Context(), a.cfg.Now(), u.ID)
	if err != nil {
		a.cfg.Log.Error("backup codes could not be issued", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)

		view := a.accountFor(r.Context(), u)
		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account", view)

		return
	}

	// Rendered rather than redirected to, because this is the only time
	// these are ever shown. A redirect would need them in a cookie or a
	// query string, and a one-time secret should go in neither.
	view := a.accountFor(r.Context(), u)
	view.Codes = codes

	a.cfg.Render.Render(w, r, http.StatusOK, "account", view)
}

func (a app) signOutEverywhere(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	if err := a.cfg.Users.SignOutEverywhere(r.Context(), u.ID); err != nil {
		a.cfg.Log.Error("sessions could not be ended", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)

		view := a.accountFor(r.Context(), u)
		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account", view)

		return
	}

	mid.ClearSession(w)

	http.Redirect(w, r, SignInPath, http.StatusSeeOther)
}

// --- moving to a new address ---------------------------------------------------

func (a app) requestEmailChange(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	view := a.accountFor(r.Context(), u)

	if err := r.ParseForm(); err != nil {
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "account", view)

		return
	}

	view.NewEmail = r.PostFormValue("email")

	addr, err := types.ParseEmail(view.NewEmail)
	if err != nil {
		view.Problem = "bad-email"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "account", view)

		return
	}

	req, err := a.cfg.Users.RequestEmailChange(r.Context(), a.cfg.Now(), u.ID, addr)

	switch {
	case errors.Is(err, userbus.ErrSameAddress):
		view.Problem = "same-address"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "account", view)

		return
	case errors.Is(err, userbus.ErrAddressTaken):
		view.Problem = "address-taken"
		a.cfg.Render.Render(w, r, http.StatusConflict, "account", view)

		return
	case err != nil:
		a.cfg.Log.Error("an address change could not be started", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)

		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account", view)

		return
	}

	a.send(r, mid.LangFrom(r.Context()), addr, "email-change-code",
		codeMail{Code: req.Code, Site: a.cfg.BaseURL, Email: addr.String()})

	setPending(w, emailChangeCookie, req.Pending, addr)

	http.Redirect(w, r, "/account#email", http.StatusSeeOther)
}

// changedMail is the notice sent to the address somebody has just left.
type changedMail struct {
	Old  string
	New  string
	Site string
}

func (a app) confirmEmailChange(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	view := a.accountFor(r.Context(), u)

	if err := r.ParseForm(); err != nil {
		view.Problem = "unreadable"
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "account", view)

		return
	}

	pending, _ := readPending(r, emailChangeCookie)
	if pending == "" {
		view.Problem = "email-stale"
		a.cfg.Render.Render(w, r, http.StatusUnauthorized, "account", view)

		return
	}

	typed := r.PostFormValue("code")
	if !userbus.LooksLikeACode(typed) {
		view.Problem = "code-shape"
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "account", view)

		return
	}

	moved, old, err := a.cfg.Users.ConfirmEmailChange(r.Context(), a.cfg.Now(), u.ID, pending, typed)

	switch {
	case errors.Is(err, userbus.ErrDenied):
		view.Problem = "email-code-wrong"
		a.cfg.Render.Render(w, r, http.StatusUnauthorized, "account", view)

		return
	case errors.Is(err, userbus.ErrAddressTaken):
		clearPending(w, emailChangeCookie)

		view.Problem = "address-taken"
		a.cfg.Render.Render(w, r, http.StatusConflict, "account", view)

		return
	case err != nil:
		a.cfg.Log.Error("an address change could not be confirmed", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)

		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account", view)

		return
	}

	clearPending(w, emailChangeCookie)

	// The old address is told. If somebody else made this change -- with a
	// phone left unlocked -- this is how the owner finds out, at the one
	// address they still read.
	a.send(r, mid.LangFrom(r.Context()), old, "email-changed",
		changedMail{Old: old.String(), New: moved.Email.String(), Site: a.cfg.BaseURL})

	http.Redirect(w, r, "/account?done=email", http.StatusSeeOther)
}

func (a app) cancelEmailChange(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	if err := a.cfg.Users.CancelEmailChange(r.Context(), a.cfg.Now(), u.ID); err != nil {
		a.cfg.Log.Error("an address change could not be withdrawn", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)

		view := a.accountFor(r.Context(), u)
		view.Problem = "server"
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "account", view)

		return
	}

	clearPending(w, emailChangeCookie)

	http.Redirect(w, r, "/account#email", http.StatusSeeOther)
}

// next is where to go after signing in: somewhere on this site, or home.
func next(want string) string {
	if safe := mid.SafeNext(want); safe != "" {
		return safe
	}

	return home
}
