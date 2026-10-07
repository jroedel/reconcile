// Package muxer assembles the application's routes into one http.Handler.
//
// It exists so that there is exactly one place to read to learn what URLs this
// service answers, and one place where the middleware order is decided. A
// handler package registers its own routes; it does not decide what wraps them.
package muxer

import (
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/jroedel/reconcile/app/domain/authapp"
	"github.com/jroedel/reconcile/app/domain/homeapp"
	"github.com/jroedel/reconcile/app/sdk/health"
	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/jroedel/reconcile/foundation/web"
)

// Templates is every app's pages, for page.NewRenderer. main builds the
// renderer rather than this package, because the strings it reads out of
// these are registered for translation before anything is served.
func Templates() []fs.FS {
	return []fs.FS{homeapp.Templates, authapp.Templates}
}

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
	Render   *page.Renderer
	Users    *userbus.Business

	// BaseURL is the public origin. Empty means sign-in is off: its routes
	// are not mounted, because a code sent from a site that cannot say where
	// it is would name nowhere.
	BaseURL string

	// Mail may be nil: no relay configured. Bootstrap may be empty: no
	// one-time secret.
	Mail      mail.Sender
	Bootstrap string

	// TrustProxy says whether X-Forwarded-For is the visitor's address.
	TrustProxy bool
}

// maxBody is the most any form here may send. A sign-in form is a few
// hundred bytes; uploads will get routes of their own outside this limit
// rather than a second MaxBody inside it (web.MaxBody).
const maxBody = 64 << 10

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil || cfg.Render == nil || cfg.Users == nil {
		return nil, errors.New("the muxer needs a logger, a database, a renderer and the users")
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	mux.HandleFunc("GET "+cfg.Render.StylesheetPath(), cfg.Render.Stylesheet())
	mux.HandleFunc("GET /static/js/{file}", cfg.Render.Scripts())

	homeapp.Routes(mux, cfg.Render)

	if cfg.BaseURL != "" {
		authapp.Routes(mux, authapp.Config{
			Log:        cfg.Log,
			Users:      cfg.Users,
			Render:     cfg.Render,
			Mail:       cfg.Mail,
			BaseURL:    cfg.BaseURL,
			Bootstrap:  cfg.Bootstrap,
			TrustProxy: cfg.TrustProxy,
		}, mid.Require(authapp.SignInPath))
	}

	// Who is signed in, then the language, which may be theirs (mid.Lang).
	inner := web.Wrap(mux,
		mid.Authenticate(cfg.Log, cfg.Users),
		mid.Lang(),
	)

	// How big and what shape a write may be is a fork rather than a line,
	// so that when uploads arrive they get branches of their own and never
	// pass through the small limit (web.MaxBody). For now there is one
	// branch: 64 KB, form encoded.
	shape := http.NewServeMux()
	shape.Handle("/", web.Wrap(inner, web.MaxBody(maxBody), web.FormEncodedOnly()))

	// Outermost first: the id, then the request line, then the headers, so
	// a panic is logged with the id and answered with the policy on it. A
	// write from another site is refused before anything reads a cookie.
	return web.Wrap(shape,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.Panics(cfg.Log),
		web.SameOriginOnly(cfg.BaseURL),
	), nil
}
