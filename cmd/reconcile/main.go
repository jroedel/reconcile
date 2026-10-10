// Command reconcile serves the reconcile app.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jroedel/reconcile/app/sdk/muxer"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/budget/stores/budgetdb"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/category/stores/categorydb"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/export/exportbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/ledger/stores/ledgerdb"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/receipt/stores/receiptdb"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/rule/stores/ruledb"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/domain/shape/stores/shapedb"
	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/translation/stores/translationdb"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/logger"
	"github.com/jroedel/reconcile/foundation/mail"
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

	// Every string the pages say is registered before anything is served,
	// so that a string arriving in this release is waiting to be translated
	// from the moment it can first be seen.
	translations := translationbus.NewBusiness(translationdb.NewStore(db), nil)

	render, err := page.NewRenderer(log, translations, muxer.Templates()...)
	if err != nil {
		return err
	}

	if err := translations.Register(ctx, render.Strings()); err != nil {
		return err
	}

	if err := translations.Reload(ctx); err != nil {
		return err
	}

	sender, err := mailer(log, cfg)
	if err != nil {
		return err
	}

	users := userbus.NewBusiness(log, userdb.NewStore(db))
	tenancy := tenancybus.NewBusiness(log, tenancydb.NewStore(db), users)
	history := eventbus.NewBusiness(eventdb.NewStore(db))

	bytes, err := filefs.NewStore(cfg.Files.Dir)
	if err != nil {
		return err
	}

	files := filebus.NewBusiness(log, filedb.NewStore(db), bytes)
	categories := categorybus.NewBusiness(log, categorydb.NewStore(db), tenancy)
	rules := rulebus.NewBusiness(log, ruledb.NewStore(db), tenancy, categories)
	shapes := shapebus.NewBusiness(log, shapedb.NewStore(db))
	ledger := ledgerbus.NewBusiness(log, ledgerdb.NewStore(db), tenancy, files, categories, rules, shapes)
	receipts := receiptbus.NewBusiness(log, receiptdb.NewStore(db), tenancy, ledger, files)
	ledger.OnImport(receipts.MatchChecks)
	budgets := budgetbus.NewBusiness(log, budgetdb.NewStore(db), tenancy, ledger, categories)
	export := exportbus.NewBusiness(log, tenancy, ledger, receipts, categories, files)

	go prune(ctx, log, users)

	handler, err := muxer.New(muxer.Config{
		Log:        log,
		DB:         db,
		Expected:   expected,
		Render:     render,
		Users:      users,
		Tenancy:    tenancy,
		History:    history,
		Files:      files,
		Ledger:     ledger,
		Shapes:     shapes,
		Categories: categories,
		Rules:      rules,
		Budgets:    budgets,
		Receipts:   receipts,
		Export:     export,
		BaseURL:    cfg.Server.BaseURL,

		Translations: translations,

		Mail:       sender,
		Bootstrap:  cfg.Auth.BootstrapSecret,
		TrustProxy: cfg.Server.TrustProxy,
	})
	if err != nil {
		return err
	}

	log.Info("starting", "addr", cfg.Server.Addr, "db", cfg.DB.Path, "files", cfg.Files.Dir)

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
		{"the interface's translations", translationdb.Init},
		{"the users", userdb.Init},
		{"the history", eventdb.Init},
		{"the organizations, accounts and projects", tenancydb.Init},
		{"the uploaded files", filedb.Init},
		{"the category lists", categorydb.Init},
		{"the sorting rules", ruledb.Init},
		{"the budgets", budgetdb.Init},
		{"the statements and transactions", ledgerdb.Init},
		{"the receipts", receiptdb.Init},
		{"the layouts of statements seen", shapedb.Init},
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

	for _, store := range []sqldb.Expected{
		translationdb.Expected,
		userdb.Expected,
		eventdb.Expected,
		tenancydb.Expected,
		filedb.Expected,
		categorydb.Expected,
		ruledb.Expected,
		budgetdb.Expected,
		ledgerdb.Expected,
		receiptdb.Expected,
		shapedb.Expected,
	} {
		maps.Copy(expected, store)
	}

	return expected
}

// mailer is how messages leave: through the configured relay, or -- on a
// developer's own machine with none -- into the log, so that `make run` can
// sign in. Nil anywhere else, which authapp logs at every code it cannot
// send: a public site must never quietly write sign-in codes to a file.
func mailer(log *slog.Logger, cfg config) (mail.Sender, error) {
	if cfg.Mail.Host != "" {
		return mail.NewSMTP(mail.Config{
			Host:     cfg.Mail.Host,
			Port:     cfg.Mail.Port,
			User:     cfg.Mail.User,
			Password: cfg.Mail.Password,
			From:     cfg.Mail.From,
			FromName: cfg.Mail.FromName,

			// The host's own Exim offers STARTTLS with a certificate for
			// its public name, which fails against "localhost"; over the
			// loopback nothing leaves the machine. mail.NewSMTP refuses
			// this for any relay that is not on this machine.
			NoTLS: isLoopback(cfg.Mail.Host),
		})
	}

	if base, err := url.Parse(cfg.Server.BaseURL); err == nil && base.Scheme == "http" && isLoopback(base.Hostname()) {
		log.Warn("no mail relay is configured: sign-in codes are written to this log, which is only for a developer's own machine")

		return logSender{log: log}, nil
	}

	return nil, nil
}

// logSender writes each message to the log instead of sending it.
type logSender struct{ log *slog.Logger }

func (s logSender) Send(_ context.Context, m mail.Message) error {
	s.log.Info("a message that would have been sent", "to", m.To, "subject", m.Subject, "text", m.Text)

	return nil
}

// prune clears spent and expired codes and sessions every hour, until the
// server stops. Housekeeping only: every check reads the expiry anyway.
func prune(ctx context.Context, log *slog.Logger, users *userbus.Business) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()

	for {
		if err := users.Prune(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Error("expired credentials could not be pruned", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
