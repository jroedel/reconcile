// Package muxer assembles the application's routes into one http.Handler.
//
// It exists so that there is exactly one place to read to learn what URLs this
// service answers, and one place where the middleware order is decided. A
// handler package registers its own routes; it does not decide what wraps them.
package muxer

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jroedel/reconcile/app/sdk/health"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/jroedel/reconcile/foundation/web"
)

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
}

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil {
		return nil, errors.New("the muxer needs a logger and a database")
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	// Outermost first: the id, then the request line, then the headers, so
	// a panic is logged with the id and answered with the policy on it.
	return web.Wrap(mux,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.Panics(cfg.Log),
	), nil
}
