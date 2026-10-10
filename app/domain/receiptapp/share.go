package receiptapp

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// The installable app (docs/phone.md, 3), lifted from stewards' inbox.
//
// A person who installs Reconcile from Chrome on an Android phone finds it
// in the phone's share sheet: a month's screenshots of checks from a bank's
// app, or three receipts photographed at a shop counter, are chosen in the
// phone's photos app and shared to it. Chrome posts them, as one multipart
// form, to the manifest's share target (sharedPath). A service worker
// (static/share-worker.mjs) takes that post on the phone, before it reaches
// the server, keeps the files in the phone's Cache Storage, and opens the
// share page, whose script (static/share.mjs) puts them in its file input
// as though they had been chosen there. The page asks where they go -- a
// checking account's check images, or an account's or a project's receipts
// -- and from there they are an upload like any other.
//
// Why not let Chrome's post reach the server? It arrives before anybody has
// said where the files go, and the server would have to keep them
// somewhere until somebody did. So the server's handler for that address
// only ever sees a share the worker missed, and asks for it again.

//go:embed static/share.mjs static/share-worker.mjs
var static embed.FS

// The app's addresses. The manifest and the scripts are files in the
// binary, nobody's data, and are served to anybody: Chrome fetches the
// manifest without cookies, and the worker again on its own schedule,
// whether a session has run out or not.
const (
	// ManifestPath is the web app manifest every page of somebody signed
	// in links (page.Renderer's OfferApp), which is what lets Chrome
	// install the app and list it in the share sheet.
	ManifestPath = "/receipts/app.webmanifest"

	// ShareScript is the script every such page loads with the manifest.
	// It registers the share target's worker, so an app installed from
	// any page is ready for its first share; on the share page it also
	// puts the shared files in the form.
	ShareScript = staticDir + "share.mjs"

	staticDir = "/receipts/static/"

	// workerName is the share target's service worker, among the static
	// files. Served with Service-Worker-Allowed, because its scope,
	// sharedPath, is not under the directory it is served from.
	workerName = "share-worker.mjs"

	// sharedPath is the share target in the manifest: where Chrome posts
	// the files shared to the app, each under "files".
	sharedPath = "/receipts/shared"

	// SharePath is the share page, where the worker sends the person with
	// the files, and the address its form posts to.
	SharePath = "/receipts/share"
)

// manifestJSON is Reconcile as Chrome installs it.
//
// Its start is the front page and its scope the whole site, so that Android
// can open a sign-in link from email in the app, rather than in a browser
// whose cookies the app cannot see. English only: Chrome reads it without
// cookies, so in no language anybody chose.
//
// The share target takes any image, though a receipt is JPEG, PNG, WebP or
// HEIC: Android lists an app in the share sheet only when it takes every
// type in the selection, and a phone may say image/* of a mixed one. A file
// that is not a receipt is refused on its own, as from the file input.
var manifestJSON = []byte(`{
  "id": "/",
  "name": "Reconcile",
  "short_name": "Reconcile",
  "description": "Receipts and the images of checks, shared from your phone into the right account or project.",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "background_color": "#fbfaf8",
  "theme_color": "#1f5f8b",
  "icons": [
    {"src": "/static/img/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "/static/img/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
    {"src": "/static/img/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}
  ],
  "share_target": {
    "action": "` + sharedPath + `",
    "method": "POST",
    "enctype": "multipart/form-data",
    "params": {
      "files": [{"name": "files", "accept": ["image/*", "application/pdf"]}]
    }
  }
}
`)

// shareRoutes mounts the app's files for anybody, and the share page and
// its form behind guard. The share target and the form are among
// UploadPatterns, mounted by Routes.
func (a app) shareRoutes(mux *http.ServeMux, guard web.Middleware) {
	mux.HandleFunc("GET "+ManifestPath, manifest)
	mux.HandleFunc("GET "+staticDir+"{file}", serveStatic)
	mux.Handle("GET "+SharePath, guard(http.HandlerFunc(a.sharePage)))
}

// startup is the modification time reported for the app's files, which
// are in the binary and so change only when the binary does.
var startup = time.Now()

// manifest serves the app's manifest. Cached for a day rather than for
// ever: Chrome reads it again to update an installed app, and a change to
// it should reach the phones that installed it within the week.
func manifest(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/manifest+json")
	h.Set("Cache-Control", "public, max-age=86400")

	http.ServeContent(w, r, "app.webmanifest", startup, bytes.NewReader(manifestJSON))
}

// serveStatic serves the app's scripts. They are asked about each time
// (no-cache, with their hash as the ETag) rather than kept for ever under
// a hashed name, as page.Renderer's are: the worker's address is in every
// phone it was registered on, and both must be the new one the moment the
// binary is.
func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")

	body, err := static.ReadFile("static/" + name)
	if err != nil || path.Ext(name) != ".mjs" {
		http.NotFound(w, r)

		return
	}

	sum := sha256.Sum256(body)

	h := w.Header()
	h.Set("ETag", `"`+hex.EncodeToString(sum[:])[:16]+`"`)

	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")

	if name == workerName {
		h.Set("Service-Worker-Allowed", sharedPath)
	}

	http.ServeContent(w, r, name, startup, bytes.NewReader(body))
}

// shareChoice is one place a share can go: a checking account's check
// images, or an account's or a project's receipts. Its Value is what the
// form sends.
type shareChoice struct {
	Value  string
	Checks bool
	Name   string

	// Project is whether it is a project's receipts, not an account's.
	Project bool
}

type shareView struct {
	// Shared is what the worker's redirect says: how many files it kept,
	// "lost" when the phone could not keep them, or "again" from the
	// server when the worker missed the share.
	Shared string
	Count  int
	Max    int

	Choices []shareChoice
}

// sharePage asks where the shared files go, among the places the person
// may add receipts to.
func (a app) sharePage(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	view := shareView{Shared: r.URL.Query().Get("shared"), Max: MaxFiles}
	view.Count, _ = strconv.Atoi(view.Shared)

	var err error
	if view.Choices, err = a.shareChoices(r, me.ID); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "receipts-share", view)
}

// shareChoices is every inbox the actor may add receipts to, an account's
// check images first among its own, since that is what a checking
// account's share most often is.
func (a app) shareChoices(r *http.Request, me types.ID) ([]shareChoice, error) {
	ov, err := a.cfg.Tenancy.Overview(r.Context(), me)
	if err != nil {
		return nil, err
	}

	var (
		accounts []tenancybus.Account
		projects []tenancybus.Project
	)

	accounts, projects = append(accounts, ov.Accounts...), append(projects, ov.Projects...)

	for _, o := range ov.Orgs {
		accounts, projects = append(accounts, o.Accounts...), append(projects, o.Projects...)
	}

	var out []shareChoice

	for _, acct := range accounts {
		if ok, err := a.may(r, me, acct.Scope()); err != nil {
			return nil, err
		} else if !ok {
			continue
		}

		if acct.Kind == tenancybus.Checking {
			out = append(out, shareChoice{Value: "checks:" + acct.ID.String(), Checks: true, Name: acct.Name})
		}

		out = append(out, shareChoice{Value: "account:" + acct.ID.String(), Name: acct.Name})
	}

	for _, p := range projects {
		if ok, err := a.may(r, me, p.Scope()); err != nil {
			return nil, err
		} else if !ok {
			continue
		}

		out = append(out, shareChoice{Value: "project:" + p.ID.String(), Name: p.Name, Project: true})
	}

	return out, nil
}

func (a app) may(r *http.Request, me types.ID, scope types.Scope) (bool, error) {
	access, err := a.cfg.Tenancy.AccessTo(r.Context(), me, scope)

	return access.Can(tenancybus.Receipts), err
}

// share is the share page's form: where the files go, which is its first
// part, and then the files. Where is read and asked about before a byte of
// a file is stored, as for every upload; then it is that inbox's upload,
// or its check images'.
func (a app) share(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		http.Redirect(w, r, SharePath, http.StatusSeeOther)

		return
	}

	part, err := mr.NextPart()
	if err != nil || part.FormName() != "to" {
		http.Redirect(w, r, SharePath, http.StatusSeeOther)

		return
	}

	to, _ := io.ReadAll(io.LimitReader(part, 128))
	part.Close()

	kind, rawID, _ := strings.Cut(strings.TrimSpace(string(to)), ":")

	id, err := types.ParseID(rawID)
	if err != nil || kind != "checks" && kind != "account" && kind != "project" {
		a.missing(w, r)

		return
	}

	home := types.AccountScope(id)
	if kind == "project" {
		home = types.ProjectScope(id)
	}

	if !a.mayAdd(w, r, me.ID, home) {
		return
	}

	got, err := a.receiveFrom(r, mr, me.ID)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	if kind == "checks" {
		a.addChecks(w, r, me.ID, id, got)

		return
	}

	a.addReceipts(w, r, me.ID, home, got)
}

// shared is a share the phone's worker did not catch: the app was
// installed and used before any page registered the worker, or Chrome
// cleared it for room. It reads the files to the end, so that the phone
// hears an answer rather than a connection closed while it was still
// sending, keeps none of them, and sends the person to the share page,
// which registers the worker and asks for the share again. Keeping them
// would mean keeping files nobody has said where to put.
func (a app) shared(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(UploadTime))

	if _, err := io.Copy(io.Discard, r.Body); err != nil && !errors.Is(err, io.EOF) {
		a.cfg.Log.Info("a share that reached the server was cut off", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	}

	http.Redirect(w, r, SharePath+"?shared=again", http.StatusSeeOther)
}
