// Command reconcile serves the reconcile app.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"syscall"

	"github.com/jroedel/reconcile/app/sdk/muxer"
	"github.com/jroedel/reconcile/foundation/logger"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/jroedel/reconcile/foundation/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "config.toml", "path to the configuration file")
		check      = flag.Bool("check", false, "load and validate the configuration, print a summary, and exit")
	)

	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	// -check exists for the deploy script, which runs the *new* binary against
	// the *live* config while the old one is still serving. A config the new
	// binary will not accept is then a deploy that swaps nothing, rather than a
	// process that dies after the rename with the previous one already gone.
	if *check {
		fmt.Print(cfg.summary())

		return nil
	}

	logFile, shouldClose, err := openLog(cfg.Log.File)
	if err != nil {
		return err
	}
	if shouldClose {
		defer logFile.Close()
	}

	level, err := logger.Level(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("%s: %w", *configPath, err)
	}

	log := logger.New(logFile, level)

	db, err := sqldb.Open(cfg.DB.Path)
	if err != nil {
		return fmt.Errorf("opening the database at %s: %w", cfg.DB.Path, err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := prepare(ctx, db); err != nil {
		return err
	}

	// After prepare, because the file has to exist before its mode can be set
	// and prepare is what creates it on a fresh install.
	if err := sqldb.Restrict(cfg.DB.Path); err != nil {
		return err
	}

	expected := expectedSchema()

	if err := sqldb.CheckSchema(ctx, db, expected); err != nil {
		return fmt.Errorf("the database does not match this binary: %w", err)
	}

	handler, err := muxer.New(muxer.Config{Log: log, DB: db, Expected: expected})
	if err != nil {
		return err
	}

	log.Info("starting", "addr", cfg.Server.Addr, "db", cfg.DB.Path)

	return web.Serve(ctx, log, cfg.Server.ShutdownGrace.Duration, cfg.Server.Addr, handler)
}

// prepare runs every store's Init, in foreign-key order.
//
// A list rather than a loop over anything clever, because the order is the
// order the references point in: a store goes after every store it
// references. A store added in the wrong place fails at startup on a fresh
// database and nowhere else, which is the cheapest moment for it to fail.
func prepare(ctx context.Context, db *sql.DB) error {
	for _, step := range []struct {
		what string
		init func(context.Context, *sql.DB) error
	}{
		{"the infrastructure tables", sqldb.Init},
	} {
		if err := step.init(ctx, db); err != nil {
			return fmt.Errorf("preparing %s: %w", step.what, err)
		}
	}

	return nil
}

// expectedSchema is every column this binary reads, merged. Each store adds
// its Expected to the list as it lands.
//
// /healthz re-checks it on every call, so a binary rolled back onto a newer
// schema reports unhealthy rather than serving against a database it does not
// understand.
func expectedSchema() sqldb.Expected {
	expected := maps.Clone(sqldb.Infrastructure)

	for _, store := range []sqldb.Expected{} {
		maps.Copy(expected, store)
	}

	return expected
}
