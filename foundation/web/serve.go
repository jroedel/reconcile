package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Timeouts a server is built with. Every one of them is set, because a server
// with no timeouts is a server one slow client can hold open.
//
// Raise one per route, through http.ResponseController, the day a route needs
// it. Not before: a generous timeout nothing needs is a generous timeout for
// anybody who wants to hold connections open.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 90 * time.Second
)

// Serve answers on addr until the context is cancelled or the listener fails,
// then shuts down within grace.
//
// The socket is bound before it is served, and that ordering is the point of
// the function. ListenAndServe binds and serves in one call, usually from
// inside a goroutine, so a port already in use is discovered *after* the log
// line claiming to be listening -- which is exactly how a process that is
// about to die reports success. Binding first turns "the port was taken" into
// a startup error, on stderr, with a non-zero exit, while the supervisor still
// has the previous binary one rename away.
func Serve(ctx context.Context, log *slog.Logger, grace time.Duration, addr string, handler http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w; is another copy already running?", addr, err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,

		// net/http's own complaints go through the same logger as everything
		// else, rather than to a bare stderr that nothing is tailing.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// Buffered, so the goroutine reporting a failure during shutdown cannot
	// block on a receiver that has already moved on.
	fatal := make(chan error, 1)

	log.Info("listening", "addr", ln.Addr().String())

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal <- fmt.Errorf("listener on %s: %w", ln.Addr(), err)
		}

		close(fatal)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down", "reason", "signal")
	case serveErr = <-fatal:
		log.Error("shutting down", "reason", "the listener failed", "err", serveErr)
	}

	// WithoutCancel, because by here the parent context is usually the one
	// that was just cancelled -- and a shutdown deadline derived from an
	// already-cancelled context gives in-flight requests no grace at all.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown did not finish in time", "err", err)
		srv.Close()
	}

	// Wait for the serving goroutine, so that nothing of this server outlives
	// the call. A closed channel reads as nil, which is the ordinary ending.
	<-fatal

	return serveErr
}
