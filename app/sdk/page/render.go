package page

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	texttemplate "text/template"
	"text/template/parse"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// assets holds the shared chrome: the layout, the stylesheet and the scripts
// every app may use.
//
//go:embed assets
var assets embed.FS

// Translator is a string in a language. translationbus.Business is one.
type Translator interface {
	Translate(lang types.Lang, context, en string) string
}

// Renderer turns a named template into a response, in the reader's language.
// Adapted from stewards, which adapted it from mass-intentions.
//
// Templates are parsed once, at startup, from the binary. A parse error is a
// startup failure rather than a 500 in front of somebody, and the binary is a
// deployable artefact on its own with no directory of templates to keep in
// step with it.
//
// # One template set per page, not one set for everything
//
// Every page defines "content", so parsing them all into one set would have
// each overwrite the last, silently, and serve the wrong page. The layout is
// parsed once and cloned per page, which is what lets {{block "content"}} mean
// something different for each.
//
// # And cloned again per request, for t
//
// {{t "…"}} needs the reader's language, and a template function is bound
// when the set is built, not when it runs. So each page's set is parsed with
// a t that only marks the strings (see extract.go), never executed itself,
// and every Render clones it and binds a t for the request's language. A
// clone of a small set is cheap next to the request around it, and the
// alternative -- the language threaded through every template's data, and
// every partial's -- is the kind of thing that is forgotten in one place.
// html/template refuses to clone a set that has been executed, which is the
// rule that makes this safe: the parsed sets are only ever the originals.
type Renderer struct {
	log   *slog.Logger
	tr    Translator
	pages map[string]*template.Template

	// mails is every message the app sends, by name: text/template, because
	// a plain-text message escaped for HTML would arrive full of &amp;.
	mails map[string]*texttemplate.Template
	texts map[string]*texttemplate.Template

	// strings is every {{t}} and {{tc}} in every page and message, read once
	// at startup.
	strings []translationbus.Use

	// The stylesheet is served under a path holding a hash of its content, so
	// it can be cached for ever and still change the moment it is edited.
	css     []byte
	cssPath string
	cssETag string

	// The scripts every app may use, served the same way and for the same
	// reason, by the name of the file served (upload.<hash>.mjs).
	// scriptPaths is the same the other way round, by the file's own name,
	// for Shell.Script.
	scripts     map[string]asset
	scriptPaths map[string]string

	// The installable app the pages of somebody signed in offer, if any:
	// see OfferApp.
	manifest, appScript string

	// images is the site's icons, by file name, from /static/img/. An icon
	// changes only when the brand does, and then under a new file name: the
	// name is the version, as stewards has it.
	images map[string]asset
}

type asset struct {
	body []byte
	etag string
	kind string
}

// Shell is what the layout is executed against. The page's own data is under
// Data, so a field a handler adds can never shadow one the layout needs.
type Shell struct {
	Stylesheet string

	// Lang is the language the page is in, and Langs every language with
	// the link that switches to it, for the header.
	Lang  types.Lang
	Langs []LangLink

	// User is who is signed in, for the header; SignedIn is false on every
	// page of somebody who is not, and User is then the zero User.
	User     userbus.User
	SignedIn bool

	// Manifest and AppScript are the installable app, on the pages of
	// somebody signed in alone, and empty on everybody else's: see
	// OfferApp.
	Manifest, AppScript string

	// scripts is where each shared script is served: see Script.
	scripts map[string]string

	Data any
}

// LangLink is one of the header's language links.
type LangLink struct {
	Lang    types.Lang
	Name    string
	URL     string
	Current bool
}

// Script is where one of the scripts every app may use is served, by its
// file's name. A name that is not one fails the page, rather than linking a
// script that is not there.
func (s Shell) Script(name string) (string, error) {
	p, ok := s.scripts[name]
	if !ok {
		return "", fmt.Errorf("there is no shared script called %s", name)
	}

	return p, nil
}

// NewRenderer parses the layout and every page template in the given
// filesystems, each holding templates/*.html and perhaps mail/*.txt, and
// reads out every string the pages and messages translate.
//
// A page or message name defined twice is a startup error rather than a
// silent win for whichever was parsed last.
func NewRenderer(log *slog.Logger, tr Translator, own ...fs.FS) (*Renderer, error) {
	if log == nil || tr == nil {
		return nil, errors.New("a renderer needs a logger and a translator")
	}

	if len(own) == 0 {
		return nil, errors.New("a renderer needs at least one app's templates")
	}

	chrome, err := fs.Sub(assets, "assets/app")
	if err != nil {
		return nil, fmt.Errorf("the chrome is missing from the binary: %w", err)
	}

	base, err := template.New("base").Funcs(Funcs).Funcs(marking).ParseFS(chrome, "*.html")
	if err != nil {
		return nil, fmt.Errorf("the layout could not be read: %w", err)
	}

	pages := map[string]*template.Template{}

	for _, fsys := range own {
		names, err := fs.Glob(fsys, "templates/*.html")
		if err != nil {
			return nil, fmt.Errorf("the page templates could not be listed: %w", err)
		}

		// Partials are pieces an app's pages share, in templates/partials.
		// Parsed into each of that app's pages and no other app's, so two
		// apps can each have a "fields" without either seeing the other's.
		partials, err := fs.Glob(fsys, "templates/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("the partial templates could not be listed: %w", err)
		}

		for _, name := range names {
			set, err := base.Clone()
			if err != nil {
				return nil, fmt.Errorf("the layout could not be copied: %w", err)
			}

			if len(partials) > 0 {
				if set, err = set.ParseFS(fsys, partials...); err != nil {
					return nil, fmt.Errorf("the partials for %s could not be read: %w", name, err)
				}
			}

			if set, err = set.ParseFS(fsys, name); err != nil {
				return nil, fmt.Errorf("%s could not be read: %w", name, err)
			}

			page := strings.TrimSuffix(path.Base(name), ".html")

			if _, taken := pages[page]; taken {
				return nil, fmt.Errorf("two apps both define a %s page", page)
			}

			pages[page] = set
		}
	}

	if len(pages) == 0 {
		return nil, errors.New("there are no page templates to read")
	}

	mails, err := parseMails(own)
	if err != nil {
		return nil, err
	}

	texts, err := parseTexts(own)
	if err != nil {
		return nil, err
	}

	trees := map[string][]*parse.Tree{}

	for name, set := range pages {
		for _, tmpl := range set.Templates() {
			trees[name+".html"] = append(trees[name+".html"], tmpl.Tree)
		}
	}

	for name, set := range mails {
		for _, tmpl := range set.Templates() {
			trees[name+".txt"] = append(trees[name+".txt"], tmpl.Tree)
		}
	}

	for name, set := range texts {
		for _, tmpl := range set.Templates() {
			trees["text/"+name+".txt"] = append(trees["text/"+name+".txt"], tmpl.Tree)
		}
	}

	found, err := extract(trees)
	if err != nil {
		return nil, err
	}

	css, err := fs.ReadFile(chrome, "app.css")
	if err != nil {
		return nil, fmt.Errorf("the stylesheet could not be read: %w", err)
	}

	sum := sha256.Sum256(css)
	digest := hex.EncodeToString(sum[:])[:12]

	rn := &Renderer{
		log:         log,
		tr:          tr,
		pages:       pages,
		mails:       mails,
		texts:       texts,
		strings:     found,
		css:         css,
		cssPath:     "/static/app." + digest + ".css",
		cssETag:     `"` + digest + `"`,
		scripts:     map[string]asset{},
		scriptPaths: map[string]string{},
	}

	rn.images = map[string]asset{}

	icons, err := fs.Glob(chrome, "img/*.png")
	if err != nil {
		return nil, fmt.Errorf("listing the icons: %w", err)
	}

	for _, name := range icons {
		body, err := fs.ReadFile(chrome, name)
		if err != nil {
			return nil, fmt.Errorf("%s could not be read: %w", name, err)
		}

		rn.images[path.Base(name)] = asset{body: body, kind: "image/png"}
	}

	modules, err := fs.Glob(chrome, "js/*.mjs")
	if err != nil {
		return nil, fmt.Errorf("listing the scripts: %w", err)
	}

	for _, name := range modules {
		if strings.HasSuffix(name, "_test.mjs") {
			continue
		}

		body, err := fs.ReadFile(chrome, name)
		if err != nil {
			return nil, fmt.Errorf("%s could not be read: %w", name, err)
		}

		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])[:12]
		base := path.Base(name)
		served := strings.TrimSuffix(base, ".mjs") + "." + hash + ".mjs"

		rn.scripts[served] = asset{body: body, etag: `"` + hash + `"`, kind: "text/javascript; charset=utf-8"}
		rn.scriptPaths[base] = "/static/js/" + served
	}

	return rn, nil
}

// Strings is every string the pages translate, with the files it is in,
// for translationbus.Register.
func (rn *Renderer) Strings() []translationbus.Use { return rn.strings }

// OfferApp has every page somebody is signed in on link manifest and load
// script, and no page anybody else sees. Lifted from stewards.
//
// It is how a person installs Reconcile from Chrome on a phone, from
// whichever page they are on, and with it Android's share target. The
// paths come from the app that owns them, through the muxer, so this
// layer knows that there is an installable app and not which: today it is
// receiptapp's, whose script registers the worker that catches a share.
// Linking the manifest only where that script runs as well is what makes
// an app installed from any page ready for its first share.
//
// Called while the routes are built, before anything is served.
func (rn *Renderer) OfferApp(manifest, script string) {
	rn.manifest, rn.appScript = manifest, script
}

// StylesheetPath is where the stylesheet is served, including its hash.
func (rn *Renderer) StylesheetPath() string { return rn.cssPath }

// Render writes a page, in the language the request asked for.
//
// Executed into a buffer first, and the status written only once that
// succeeded: a template error halfway down would otherwise be a half page
// under a 200, which looks like it worked.
func (rn *Renderer) Render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	original, ok := rn.pages[name]
	if !ok {
		rn.fail(w, r, name, errors.New("a handler asked for a page that does not exist"))

		return
	}

	lang := mid.LangFrom(r.Context())

	set, err := original.Clone()
	if err != nil {
		rn.fail(w, r, name, err)

		return
	}

	set.Funcs(translating(rn.tr, lang))

	links := make([]LangLink, 0, len(types.Langs))
	for _, l := range types.Langs {
		links = append(links, LangLink{Lang: l, Name: l.Name(), URL: mid.SwitchURL(r, l), Current: l == lang})
	}

	user, signedIn := mid.UserFrom(r.Context())

	var manifest, appScript string
	if signedIn {
		manifest, appScript = rn.manifest, rn.appScript
	}

	var buf bytes.Buffer

	if err := set.ExecuteTemplate(&buf, "base", Shell{
		Stylesheet: rn.cssPath,
		Lang:       lang,
		Langs:      links,
		User:       user,
		SignedIn:   signedIn,
		Manifest:   manifest,
		AppScript:  appScript,
		scripts:    rn.scriptPaths,
		Data:       data,
	}); err != nil {
		rn.fail(w, r, name, err)

		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")

	// The language is chosen from a cookie and Accept-Language, so a cache
	// between here and the phone must not hand one person's Spanish page to
	// the next person's English request.
	h.Add("Vary", "Cookie, Accept-Language")
	h.Set("Content-Language", string(lang))
	w.WriteHeader(status)

	if _, err := buf.WriteTo(w); err != nil {
		rn.log.Warn("a page was cut off while being sent",
			"id", web.RequestIDFrom(r.Context()), "template", name, "err", err)
	}
}

func (rn *Renderer) fail(w http.ResponseWriter, r *http.Request, name string, err error) {
	rn.log.Error("a page could not be rendered",
		"id", web.RequestIDFrom(r.Context()), "template", name, "err", err)
	http.Error(w, "Something went wrong on our end. Try once more in a minute.", http.StatusInternalServerError)
}

// Stylesheet serves the stylesheet, cacheable for ever because its path
// carries its hash. That overrides the page policy's no-store, which is right
// for a page of somebody's transactions and wrong for a file compiled into
// the binary.
func (rn *Renderer) Stylesheet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("ETag", rn.cssETag)

		http.ServeContent(w, r, "app.css", startup, bytes.NewReader(rn.css))
	}
}

// Scripts serves the scripts every app may use, from /static/js/{file},
// cacheable for ever as the stylesheet is. A name that is not one, an old
// hash included, is a plain 404.
func (rn *Renderer) Scripts() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")

		s, ok := rn.scripts[file]
		if !ok {
			http.NotFound(w, r)

			return
		}

		h := w.Header()
		h.Set("Content-Type", s.kind)
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("ETag", s.etag)

		http.ServeContent(w, r, file, startup, bytes.NewReader(s.body))
	}
}

// Images serves the site's icons from /static/img/{file}: the favicon every
// page links, and the installable app's (receiptapp's manifest). Cached
// for a week rather than for ever, though the name is the version, so
// that a phone's home screen catches up with a corrected icon.
func (rn *Renderer) Images() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")

		img, ok := rn.images[file]
		if !ok {
			http.NotFound(w, r)

			return
		}

		h := w.Header()
		h.Set("Content-Type", img.kind)
		h.Set("Cache-Control", "public, max-age=604800")

		http.ServeContent(w, r, file, startup, bytes.NewReader(img.body))
	}
}

// startup is the modification time reported for embedded files. embed.FS
// records none, and a zero time makes ServeContent skip conditional requests.
var startup = time.Now()
