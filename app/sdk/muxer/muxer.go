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

	"github.com/jroedel/reconcile/app/domain/adminapp"
	"github.com/jroedel/reconcile/app/domain/authapp"
	"github.com/jroedel/reconcile/app/domain/categoryapp"
	"github.com/jroedel/reconcile/app/domain/exportapp"
	"github.com/jroedel/reconcile/app/domain/homeapp"
	"github.com/jroedel/reconcile/app/domain/ledgerapp"
	"github.com/jroedel/reconcile/app/domain/receiptapp"
	"github.com/jroedel/reconcile/app/domain/tenancyapp"
	"github.com/jroedel/reconcile/app/sdk/health"
	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/export/exportbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/jroedel/reconcile/foundation/web"
)

// Templates is every app's pages, for page.NewRenderer. main builds the
// renderer rather than this package, because the strings it reads out of
// these are registered for translation before anything is served.
func Templates() []fs.FS {
	return []fs.FS{homeapp.Templates, authapp.Templates, tenancyapp.Templates, ledgerapp.Templates, categoryapp.Templates, receiptapp.Templates, exportapp.Templates, adminapp.Templates}
}

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
	Render   *page.Renderer
	Users    *userbus.Business
	Tenancy  *tenancybus.Business
	History  *eventbus.Business
	Files    *filebus.Business
	Ledger   *ledgerbus.Business

	Categories *categorybus.Business
	Receipts   *receiptbus.Business
	Export     *exportbus.Business

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
	if cfg.Log == nil || cfg.DB == nil || cfg.Render == nil || cfg.Users == nil || cfg.Tenancy == nil || cfg.History == nil ||
		cfg.Files == nil || cfg.Ledger == nil || cfg.Categories == nil || cfg.Receipts == nil || cfg.Export == nil {
		return nil, errors.New("the muxer needs a logger, a database, a renderer, and every domain's business")
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	mux.HandleFunc("GET "+cfg.Render.StylesheetPath(), cfg.Render.Stylesheet())
	mux.HandleFunc("GET /static/js/{file}", cfg.Render.Scripts())

	homeapp.Routes(mux, cfg.Log, cfg.Render, cfg.Tenancy, cfg.Receipts)

	// Everything behind sign-in needs sign-in to exist, so all of it is
	// mounted only when there is a public address.
	if cfg.BaseURL != "" {
		guard := mid.Require(authapp.SignInPath)

		authapp.Routes(mux, authapp.Config{
			Log:        cfg.Log,
			Users:      cfg.Users,
			Render:     cfg.Render,
			Mail:       cfg.Mail,
			BaseURL:    cfg.BaseURL,
			Bootstrap:  cfg.Bootstrap,
			TrustProxy: cfg.TrustProxy,
			Grants:     cfg.Tenancy,
		}, guard)

		tenancyapp.Routes(mux, tenancyapp.Config{
			Log:     cfg.Log,
			Tenancy: cfg.Tenancy,
			History: cfg.History,
			Users:   cfg.Users,
			Render:  cfg.Render,
			Mail:    cfg.Mail,
			BaseURL: cfg.BaseURL,
		}, guard)

		ledgerapp.Routes(mux, ledgerapp.Config{
			Log:     cfg.Log,
			Ledger:  cfg.Ledger,
			Tenancy: cfg.Tenancy,
			Files:   cfg.Files,
			Users:   cfg.Users,
			Render:  cfg.Render,

			Categories: cfg.Categories,
			Receipts:   cfg.Receipts,
		}, guard)

		receiptapp.Routes(mux, receiptapp.Config{
			Log:      cfg.Log,
			Receipts: cfg.Receipts,
			Tenancy:  cfg.Tenancy,
			Files:    cfg.Files,
			Users:    cfg.Users,
			Render:   cfg.Render,
		}, guard)

		exportapp.Routes(mux, exportapp.Config{
			Log:    cfg.Log,
			Export: cfg.Export,
			Render: cfg.Render,
		}, guard)

		categoryapp.Routes(mux, categoryapp.Config{
			Log:        cfg.Log,
			Categories: cfg.Categories,
			Tenancy:    cfg.Tenancy,
			Render:     cfg.Render,
		}, guard)

		adminapp.Routes(mux, adminapp.Config{
			Log:    cfg.Log,
			Users:  cfg.Users,
			Names:  cfg.Tenancy,
			Render: cfg.Render,
		}, guard)
	}

	// Who is signed in, then the language, which may be theirs (mid.Lang).
	inner := web.Wrap(mux,
		mid.Authenticate(cfg.Log, cfg.Users),
		mid.Lang(),
	)

	// How big and what shape a write may be is a fork rather than a line,
	// so that an upload gets a branch of its own and never passes through
	// the small limit (web.MaxBody). Every form is 64 KB and form encoded;
	// a statement is up to ledgerapp.MaxUpload and multipart, and receipts
	// up to receiptapp.MaxUpload. An upload also gets longer than the
	// server's 30 seconds to arrive: a phone on one bar is slow.
	shape := http.NewServeMux()
	shape.Handle("/", web.Wrap(inner, web.MaxBody(maxBody), web.FormEncodedOnly()))
	shape.Handle(ledgerapp.UploadPattern, web.Wrap(inner,
		web.Deadline(receiptapp.UploadTime), web.MaxBody(ledgerapp.MaxUpload), web.MultipartOnly()))

	for _, pattern := range receiptapp.UploadPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(receiptapp.UploadTime), web.MaxBody(receiptapp.MaxUpload), web.MultipartOnly()))
	}

	// A download is the other way round: a small request, and an answer
	// that may take many minutes to send.
	for _, pattern := range exportapp.DownloadPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(exportapp.DownloadTime), web.MaxBody(maxBody), web.FormEncodedOnly()))
	}

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
