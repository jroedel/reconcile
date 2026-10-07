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

	"github.com/jroedel/reconcile/app/domain/homeapp"
	"github.com/jroedel/reconcile/app/sdk/health"
	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/jroedel/reconcile/foundation/web"
)

// Templates is every app's pages, for page.NewRenderer. main builds the
// renderer rather than this package, because the strings it reads out of
// these are registered for translation before anything is served.
func Templates() []fs.FS {
	return []fs.FS{homeapp.Templates}
}

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
	Render   *page.Renderer
}

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil || cfg.Render == nil {
		return nil, errors.New("the muxer needs a logger, a database and a renderer")
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	mux.HandleFunc("GET "+cfg.Render.StylesheetPath(), cfg.Render.Stylesheet())
	mux.HandleFunc("GET /static/js/{file}", cfg.Render.Scripts())

	homeapp.Routes(mux, cfg.Render)

	// Outermost first: the id, then the request line, then the headers, so
	// a panic is logged with the id and answered with the policy on it.
	return web.Wrap(mux,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.Panics(cfg.Log),
		mid.Lang(),
	), nil
}
