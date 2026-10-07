// Command reconcile serves the reconcile app.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jroedel/reconcile/app/sdk/muxer"
	"github.com/jroedel/reconcile/foundation/logger"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handler, err := muxer.New(muxer.Config{Log: log})
	if err != nil {
		return err
	}

	log.Info("starting", "addr", cfg.Server.Addr)

	return web.Serve(ctx, log, cfg.Server.ShutdownGrace.Duration, cfg.Server.Addr, handler)
}
