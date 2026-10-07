// Package homeapp is the front page: what this app is, for somebody who has
// just been sent its address, and for somebody signed in, everything they can
// reach.
package homeapp

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates
var templates embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = templates

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Overviewer is what a signed-in front page lists.
type Overviewer interface {
	Overview(ctx context.Context, actor types.ID) (tenancybus.Overview, error)
}

// view is the front page's data. Overview is empty for somebody signed out.
type view struct {
	Overview tenancybus.Overview
}

// Routes mounts the front page. {$} so that it is "/" and nothing else; every
// other path that nothing claims is a 404, not the front page again.
func Routes(mux *http.ServeMux, log *slog.Logger, render Renderer, overview Overviewer) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		var v view

		if u, ok := mid.UserFrom(r.Context()); ok {
			ov, err := overview.Overview(r.Context(), u.ID)
			if err != nil {
				log.Error("the overview could not be read", "request_id", web.RequestIDFrom(r.Context()), "error", err)
				http.Error(w, "Something went wrong at our end. Please try again in a few minutes.", http.StatusInternalServerError)

				return
			}

			v.Overview = ov
		}

		render.Render(w, r, http.StatusOK, "index", v)
	})
}
