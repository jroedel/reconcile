package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jroedel/reconcile/foundation/vitals"
	"github.com/jroedel/reconcile/foundation/web"
)

// The server's black box (foundation/vitals says why there is one).
//
// Three things go to the log: at start, the limits the process runs under,
// and a warning carrying the last record of a run that did not stop
// properly; every few minutes, a reading of its memory and CPU and what it
// is answering. One thing goes to a file beside the database: that record,
// rewritten within a second of any request beginning or ending and every
// minute regardless, and removed when the server stops properly. So the
// next start after a silent stop says how big the process had grown, how
// much CPU it had used, and which requests were being answered when it
// went -- the request line of a request that took the process with it is
// never written.

const (
	// vitalsEvery is how often a reading goes to the log: often enough to
	// see a process growing toward a limit, and few enough lines (288 a
	// day) not to bury the requests in a log nothing rotates.
	vitalsEvery = 5 * time.Minute

	// recordEvery is how often the record is rewritten when nothing began
	// or ended, so that its reading is never more than this old.
	recordEvery = time.Minute

	// flightsShown is the most requests a record or a log line names.
	flightsShown = 8
)

// boxPath is the black box beside the database: reconcile.running beside
// reconcile.db.
func boxPath(db string) string {
	return strings.TrimSuffix(db, filepath.Ext(db)) + ".running"
}

// openBlackBox starts the record, and logs what the last run left and the
// limits this one runs under. It never stops the server from starting:
// a box that cannot be kept is a warning and no box.
func openBlackBox(log *slog.Logger, path string) *vitals.Box {
	log.Info("the limits the server runs under",
		append([]any{"pid", os.Getpid(), "go", runtime.Version(), "cpus", runtime.NumCPU()}, vitals.Limits()...)...)

	box, left, err := vitals.OpenBox(path)
	if err != nil {
		log.Warn("the server's black box could not be read, so a silent stop will not be explained", "path", path, "error", err)

		return nil
	}

	if left != "" {
		log.Warn("the last run ended without shutting down: it was killed from outside, or the machine stopped. Its last record follows",
			"last_record", left)
	}

	return box
}

// keepVitals writes the readings and the record until ctx ends.
func keepVitals(ctx context.Context, log *slog.Logger, box *vitals.Box, flights *web.InFlight, started time.Time) {
	failed := false

	write := func() {
		err := box.Write(record(started, flights.Now(), vitals.Read()))
		if err != nil && !failed && ctx.Err() == nil {
			failed = true
			log.Warn("the server's black box could not be written; it will be tried again, and this said once", "error", err)
		}
	}

	write()

	second := time.NewTicker(time.Second)
	defer second.Stop()

	lastRecord, lastLog := time.Now(), time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-second.C:
			if flights.Changed() || now.Sub(lastRecord) >= recordEvery {
				write()
				lastRecord = now
			}

			if now.Sub(lastLog) >= vitalsEvery {
				lastLog = now

				fl := flights.Now()
				log.Info("vitals", append(vitals.Read().Attrs(), "in_flight", len(fl), "longest", describe(fl, 1, now))...)
			}
		}
	}
}

// logSignals logs the signals that would otherwise pass unseen, until ctx
// ends. SIGXCPU is the warning a CPU-time limit gives before its kill;
// Go ignores it unless asked, so without this the kill would come out of
// nowhere. SIGHUP and the others are logged in case a host sends one
// before it kills. The server carries on after each: interrupt and SIGTERM
// remain the ways to stop it, and they say so in the log.
func logSignals(ctx context.Context, log *slog.Logger, box *vitals.Box, flights *web.InFlight, started time.Time) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGXCPU, syscall.SIGXFSZ, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case s := <-ch:
			r := vitals.Read()
			log.Warn("a signal arrived; the server carries on", append([]any{"signal", s.String()}, r.Attrs()...)...)
			box.Write(record(started, flights.Now(), r))
		}
	}
}

// record is the black box's line: the reading, and the requests being
// answered, in the log's own key=value form so that the warning carrying
// it reads like the lines around it.
func record(started time.Time, fl []web.Flight, r vitals.Reading) string {
	now := time.Now()

	var b bytes.Buffer
	l := slog.New(slog.NewTextHandler(&b, nil))

	attrs := []any{"pid", os.Getpid(), "started", started.Format(time.RFC3339)}
	attrs = append(attrs, r.Attrs()...)
	attrs = append(attrs, "in_flight", len(fl), "requests", describe(fl, flightsShown, now))

	l.Info("record", attrs...)

	return b.String()
}

// describe names at most n requests, the longest-running first:
// "GET /receipts/…/files/0/large 2.3s 9a73…; POST /mcp 4m12s 15f2…".
func describe(fl []web.Flight, n int, now time.Time) string {
	var parts []string

	for i, f := range fl {
		if i == n {
			parts = append(parts, fmt.Sprintf("and %d more", len(fl)-n))

			break
		}

		parts = append(parts, fmt.Sprintf("%s %s %s %s", f.Method, f.Path, now.Sub(f.Since).Round(100*time.Millisecond), f.ID))
	}

	return strings.Join(parts, "; ")
}
