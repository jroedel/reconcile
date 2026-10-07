// Package muxer assembles the application's routes into one http.Handler.
//
// It exists so that there is exactly one place to read to learn what URLs this
// service answers, and one place where the middleware order is decided. A
// handler package registers its own routes; it does not decide what wraps them.
package muxer

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/foundation/web"
)

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log *slog.Logger
}

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil {
		return nil, errors.New("the muxer needs a logger")
	}

	mux := http.NewServeMux()

	// A plain 200 until there is a database. When the first store lands,
	// this becomes a schema check (see CLAUDE.md, "When there is a
	// database"): the deploy rolls back on what /healthz says, and a binary
	// rolled back onto a newer schema must report unhealthy.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	// Outermost first: the id, then the request line, then the headers, so
	// a panic is logged with the id and answered with the policy on it.
	return web.Wrap(mux,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.Panics(cfg.Log),
	), nil
}
