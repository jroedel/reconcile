// Package homeapp is the front page: what this app is, for somebody who has
// just been sent its address.
package homeapp

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed templates
var templates embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = templates

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Routes mounts the front page. {$} so that it is "/" and nothing else; every
// other path that nothing claims is a 404, not the front page again.
func Routes(mux *http.ServeMux, render Renderer) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		render.Render(w, r, http.StatusOK, "index", nil)
	})
}
