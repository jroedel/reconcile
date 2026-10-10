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
	"github.com/jroedel/reconcile/app/domain/apiapp"
	"github.com/jroedel/reconcile/app/domain/authapp"
	"github.com/jroedel/reconcile/app/domain/budgetapp"
	"github.com/jroedel/reconcile/app/domain/categoryapp"
	"github.com/jroedel/reconcile/app/domain/exportapp"
	"github.com/jroedel/reconcile/app/domain/homeapp"
	"github.com/jroedel/reconcile/app/domain/ledgerapp"
	"github.com/jroedel/reconcile/app/domain/mcpapp"
	"github.com/jroedel/reconcile/app/domain/oauthapp"
	"github.com/jroedel/reconcile/app/domain/receiptapp"
	"github.com/jroedel/reconcile/app/domain/ruleapp"
	"github.com/jroedel/reconcile/app/domain/tenancyapp"
	"github.com/jroedel/reconcile/app/domain/translationapp"
	"github.com/jroedel/reconcile/app/sdk/health"
	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/budget/budgetbus"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/export/exportbus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/oauth"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/jroedel/reconcile/foundation/web"
)

// Templates is every app's pages, for page.NewRenderer. main builds the
// renderer rather than this package, because the strings it reads out of
// these are registered for translation before anything is served.
func Templates() []fs.FS {
	return []fs.FS{homeapp.Templates, authapp.Templates, tenancyapp.Templates, ledgerapp.Templates, categoryapp.Templates, receiptapp.Templates, ruleapp.Templates, budgetapp.Templates, exportapp.Templates, adminapp.Templates, oauthapp.Templates, translationapp.Templates}
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
	Rules      *rulebus.Business
	Budgets    *budgetbus.Business
	Receipts   *receiptbus.Business
	Export     *exportbus.Business
	Shapes     *shapebus.Business

	// Translations is the interface's strings, which the API fills.
	Translations *translationbus.Business

	// OAuthClients reads a program's metadata document; nil reads it over
	// the network. A test hands in its own, since the hosts it trusts are
	// not ones a test can reach.
	OAuthClients oauthapp.Clients

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

// maxJSON is the most a request to the API or /mcp may send: a full batch of
// translations (translationbus.MaxBatch) with the English beside each one,
// which at a few hundred bytes a string is past maxBody.
const maxJSON = 1 << 20

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil || cfg.Render == nil || cfg.Users == nil || cfg.Tenancy == nil || cfg.History == nil ||
		cfg.Files == nil || cfg.Ledger == nil || cfg.Categories == nil || cfg.Rules == nil || cfg.Budgets == nil || cfg.Receipts == nil || cfg.Export == nil ||
		cfg.Shapes == nil || cfg.Translations == nil {
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

			Translators: cfg.Translations,
		}, guard)

		tenancyapp.Routes(mux, tenancyapp.Config{
			Log:     cfg.Log,
			Tenancy: cfg.Tenancy,
			History: cfg.History,
			Users:   cfg.Users,
			Render:  cfg.Render,
			Mail:    cfg.Mail,
			BaseURL: cfg.BaseURL,

			Categories: cfg.Categories,
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
			Rules:      cfg.Rules,
		}, guard)

		budgetapp.Routes(mux, budgetapp.Config{
			Log:     cfg.Log,
			Budgets: cfg.Budgets,
			Years:   cfg.Tenancy,
			Render:  cfg.Render,
		}, guard)

		ruleapp.Routes(mux, ruleapp.Config{
			Log:        cfg.Log,
			Rules:      cfg.Rules,
			Ledger:     cfg.Ledger,
			Categories: cfg.Categories,
			Tenancy:    cfg.Tenancy,
			Render:     cfg.Render,
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
			Ledger:     cfg.Ledger,
		}, guard)

		adminapp.Routes(mux, adminapp.Config{
			Log:    cfg.Log,
			Users:  cfg.Users,
			Names:  cfg.Tenancy,
			Shapes: cfg.Shapes,
			Render: cfg.Render,

			Translators: cfg.Translations,
		}, guard)

		translationapp.Routes(mux, translationapp.Config{
			Log:          cfg.Log,
			Translations: cfg.Translations,
			Users:        cfg.Users,
			Render:       cfg.Render,
		}, guard)

		// How Claude on claude.ai gets a key: a translator agrees on a page
		// of ours, and the key comes back through the token endpoint
		// instead of through their clipboard. Behind the same guard as the
		// screens, since agreeing is a signed-in person's.
		clients := cfg.OAuthClients
		if clients == nil {
			clients = oauth.NewFetcher(nil)
		}

		oauthapp.Routes(mux, oauthapp.Config{
			Log:         cfg.Log,
			Render:      cfg.Render,
			Users:       cfg.Users,
			Translators: cfg.Translations,
			Clients:     clients,
			BaseURL:     cfg.BaseURL,
		}, guard)
	}

	// The API on a mux of its own, so that nothing under /api is ever
	// reached through the cookie's chain or the cookie through the API's: a
	// page somebody is signed in to cannot be made to call it, and a key
	// cannot open a page. Mounted only with sign-in on, like the screens,
	// since a key is made on one. Translations, the statement inbox and
	// reading the books (apiapp), each endpoint behind the scope it needs.
	api := http.NewServeMux()
	if cfg.BaseURL != "" {
		apiapp.Routes(api, apiapp.Config{
			Log: cfg.Log, Translations: cfg.Translations, Inbox: cfg.Ledger, BaseURL: cfg.BaseURL,
			Books: apiapp.Books{Ledger: cfg.Ledger, Tenancy: cfg.Tenancy, Categories: cfg.Categories, Receipts: cfg.Receipts, History: cfg.History, Rules: cfg.Rules, Pictures: cfg.Files},
		})
	}

	// Who is signed in, then the language, which may be theirs (mid.Lang).
	inner := web.Wrap(mux,
		mid.Authenticate(cfg.Log, cfg.Users),
		mid.Lang(),
	)
	apiInner := web.Wrap(api, mid.APIKey(cfg.Log, cfg.Users))

	// The API again, as an MCP server for Claude on claude.ai: each tool
	// call is a request to the site, so it is handled exactly as the same
	// request from a program would be. site is set below, before anything
	// can call it.
	var site, mcp http.Handler
	if cfg.BaseURL != "" {
		m := mcpapp.New(mcpapp.Config{Log: cfg.Log, Keys: cfg.Users, BaseURL: cfg.BaseURL, Site: func() http.Handler { return site }})
		m.Routes(mux)
		mcp = m.Handler()
	}

	// How big and what shape a write may be is a fork rather than a line,
	// so that an upload gets a branch of its own and never passes through
	// the small limit (web.MaxBody). Every form is 64 KB and form encoded;
	// a statement is up to ledgerapp.MaxUpload and multipart, and receipts
	// up to receiptapp.MaxUpload. An upload also gets longer than the
	// server's 30 seconds to arrive: a phone on one bar is slow.
	shape := http.NewServeMux()
	shape.Handle("/", web.Wrap(inner, web.MaxBody(maxBody), web.FormEncodedOnly()))

	// And who is asking is decided per branch: a session cookie for the
	// pages, an API key for the API and /mcp, never both.
	shape.Handle("/api/", web.Wrap(apiInner, web.MaxBody(maxJSON), web.JSONOnly()))
	if mcp != nil {
		shape.Handle(mcpapp.Path, web.Wrap(mcp, web.MaxBody(maxJSON), web.JSONOnly()))
	}

	// A statement sent to the inbox is the file itself, not JSON: as big
	// as a statement may be, of whatever type it is, and an upload's time
	// to arrive. Still the API's branch, asked by a key and never a cookie.
	for _, pattern := range apiapp.InboxPatterns {
		shape.Handle(pattern, web.Wrap(apiInner, web.Deadline(receiptapp.UploadTime), web.MaxBody(apiapp.MaxInbox)))
	}
	for _, pattern := range ledgerapp.UploadPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(receiptapp.UploadTime), web.MaxBody(ledgerapp.MaxUpload), web.MultipartOnly()))
	}

	// Many statements at once, and the pages that read them all
	// (ledgerapp, bulk.go): longer to arrive and to answer than a page.
	for _, pattern := range ledgerapp.BulkPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(ledgerapp.BulkTime), web.MaxBody(ledgerapp.MaxBulkUpload), web.MultipartOnly()))
	}

	for _, pattern := range ledgerapp.SlowPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(ledgerapp.BulkTime), web.MaxBody(maxBody), web.FormEncodedOnly()))
	}

	for _, pattern := range receiptapp.UploadPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(receiptapp.UploadTime), web.MaxBody(receiptapp.MaxUpload), web.MultipartOnly()))
	}

	// The site administrator's trial of a layout draft on a statement of
	// theirs (adminapp): as big as a statement may be.
	for _, pattern := range adminapp.UploadPatterns {
		shape.Handle(pattern, web.Wrap(inner,
			web.Deadline(receiptapp.UploadTime), web.MaxBody(adminapp.MaxUpload), web.MultipartOnly()))
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
	site = web.Wrap(shape,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.Panics(cfg.Log),
		web.SameOriginOnly(cfg.BaseURL),
	)

	return site, nil
}
