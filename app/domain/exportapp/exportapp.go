// Package exportapp is the download of the accountant's package
// (exportbus): an account's for a period, or a project's whole.
//
// A download is a GET, from a link or a small form on the month-by-month
// page, a statement's page, or a project's book. Nothing changes when one
// is made, so nothing is written to the history; the log keeps who
// downloaded what.
package exportapp

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/export/exportbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates text
var files embed.FS

// Templates is this app's pages and texts, for page.NewRenderer.
var Templates fs.FS = files

// DownloadPatterns are the downloads. The muxer gives them DownloadTime to
// be written: a year of receipt photos is hundreds of megabytes, and the
// server's 30 seconds would cut it off for anybody on a slow line.
var DownloadPatterns = []string{
	"GET /accounts/{id}/export",
	"GET /projects/{id}/export",
}

// DownloadTime is how long a download may take to send.
const DownloadTime = 30 * time.Minute

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
	Text(lang types.Lang, name string, data any) (string, error)
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Export *exportbus.Business
	Render Renderer

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type app struct {
	cfg Config
}

// Routes mounts this app behind guard, which is mid.Require.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg}

	mux.Handle(DownloadPatterns[0], guard(http.HandlerFunc(a.account)))
	mux.Handle(DownloadPatterns[1], guard(http.HandlerFunc(a.project)))
}

func (a app) account(w http.ResponseWriter, r *http.Request) {
	a.download(w, r, func(ctx context.Context, me, id types.ID) (exportbus.Package, error) {
		from, to, ok := period(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if !ok {
			return exportbus.Package{}, exportbus.ErrPeriod
		}

		return a.cfg.Export.Account(ctx, me, id, from, to)
	})
}

func (a app) project(w http.ResponseWriter, r *http.Request) {
	a.download(w, r, a.cfg.Export.Project)
}

// download makes the package and sends it.
//
// Everything that can be refused is refused before the first byte, while
// there is still a status to choose. After that a failure -- a file gone
// from disk, a person who closed the tab -- can only end the zip early,
// which every unzipper reports as a damaged file rather than taking it
// for a whole one; the log says which.
func (a app) download(w http.ResponseWriter, r *http.Request, build func(ctx context.Context, me, id types.ID) (exportbus.Package, error)) {
	me, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)

		return
	}

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		a.cfg.Render.Render(w, r, http.StatusNotFound, "export-refused", "missing")

		return
	}

	ctx := r.Context()

	p, err := build(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	text, err := a.cfg.Render.Text(mid.LangFrom(ctx), "export-columns", nil)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	kinds, err := a.kinds(mid.LangFrom(ctx))
	if err != nil {
		a.failed(w, r, err)

		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": p.Name}))
	w.Header().Set("Cache-Control", "private, no-store")

	if err := a.cfg.Export.Write(ctx, a.cfg.Now(), w, p, columns(text), kinds); err != nil {
		a.cfg.Log.Warn("a download was cut short", "request_id", web.RequestIDFrom(ctx), "user_id", me.ID.String(),
			"name", p.Name, "error", err)

		return
	}

	a.cfg.Log.Info("the accountant's package was downloaded", "user_id", me.ID.String(), "path", r.URL.Path,
		"rows", len(p.Rows), "files", len(p.Entries))
}

// kinds is the words for each kind of money in the kind column, in the
// reader's language (text/export-kinds.txt).
func (a app) kinds(lang types.Lang) (map[categorybus.Kind]string, error) {
	text, err := a.cfg.Render.Text(lang, "export-kinds", nil)
	if err != nil {
		return nil, err
	}

	words := columns(text)
	if len(words) != len(categorybus.Kinds) {
		return nil, fmt.Errorf("export-kinds.txt has %d words, not %d", len(words), len(categorybus.Kinds))
	}

	out := make(map[categorybus.Kind]string, len(words))
	for i, k := range categorybus.Kinds {
		out[k] = words[i]
	}

	return out, nil
}

// columns is the header row, one column name to a line of the text.
func columns(text string) []string {
	var out []string

	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}

	return out
}

// period reads the form's from and to: days, YYYY-MM-DD, or months,
// YYYY-MM, which mean their first and their last day. A browser that has
// no month picker shows a text box, and a person types a month.
func period(from, to string) (types.Date, types.Date, bool) {
	start, err := day(from, false)
	if err != nil {
		return types.Date{}, types.Date{}, false
	}

	end, err := day(to, true)
	if err != nil {
		return types.Date{}, types.Date{}, false
	}

	return start, end, true
}

func day(s string, last bool) (types.Date, error) {
	s = strings.TrimSpace(s)

	if t, err := time.Parse("2006-01", s); err == nil {
		if last {
			t = t.AddDate(0, 1, -1)
		}

		return types.DateOf(t), nil
	}

	return types.ParseDate(s)
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, exportbus.ErrNotFound):
		a.cfg.Render.Render(w, r, http.StatusNotFound, "export-refused", "missing")
	case errors.Is(err, exportbus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "export-refused", "forbidden")
	case errors.Is(err, exportbus.ErrPeriod):
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "export-refused", "period")
	default:
		a.cfg.Log.Error("a download failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "export-refused", "server")
	}
}
