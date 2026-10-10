// Package receiptapp is the pages for receipts: an account's or a project's
// inbox, uploading into it from a phone, the list of receipts waiting for a
// match, one receipt with its pages, and attaching it to a transaction.
//
// Uploading is one form that works without JavaScript: the file picker
// takes several photos at once, from the camera or the library, and each
// becomes its own receipt unless "these are pages of one receipt" is ticked.
// Nothing about a receipt has to be typed to keep it; an amount and a date
// are what make a match suggestible, and can be added afterwards.
package receiptapp

import (
	"context"
	"embed"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates
var files embed.FS

// Templates is this app's pages, for page.NewRenderer.
var Templates fs.FS = files

// UploadPatterns are the routes that take files. The muxer gives each a
// branch of its own: multipart only, up to MaxUpload, and UploadTime to
// arrive.
var UploadPatterns = []string{
	"POST /accounts/{id}/receipts",
	"POST /projects/{id}/receipts",
	"POST /transactions/{id}/receipts",
	"POST /accounts/{id}/checks",
}

// The limits on one upload. A phone's photo is three to eight megabytes;
// a scanned PDF can be more.
const (
	MaxFile    = 20 << 20
	MaxFiles   = 20
	MaxUpload  = 120 << 20
	UploadTime = 10 * time.Minute
)

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Config is what this app needs.
type Config struct {
	Log      *slog.Logger
	Receipts *receiptbus.Business
	Tenancy  *tenancybus.Business
	Files    *filebus.Business
	Users    Users
	Render   Renderer

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Users names who uploaded a receipt and who attached it.
type Users interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
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

	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, guard(h)) }

	handle("GET /receipts", a.waiting)
	handle("GET /accounts/{id}/receipts", a.inbox(types.ScopeAccount))
	handle("GET /projects/{id}/receipts", a.inbox(types.ScopeProject))
	handle(UploadPatterns[0], a.upload(types.ScopeAccount))
	handle(UploadPatterns[1], a.upload(types.ScopeProject))
	handle(UploadPatterns[2], a.uploadOnto)
	handle(UploadPatterns[3], a.uploadChecks)
	handle("GET /receipts/{id}", a.receipt)
	handle("GET /receipts/{id}/files/{n}", a.file)
	handle("GET /receipts/{id}/files/{n}/{size}", a.picture)
	handle("POST /receipts/{id}/details", a.details)
	handle("POST /receipts/{id}/number", a.number)
	handle("POST /receipts/{id}/attach", a.attach)
	handle("POST /receipts/{id}/detach", a.detach)
	handle("POST /receipts/{id}/remove", a.remove)
}

// --- the shared tail --------------------------------------------------------

func actor(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
	}

	return u, ok
}

func (a app) pathID(w http.ResponseWriter, r *http.Request) (types.ID, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		a.missing(w, r)

		return types.ID{}, false
	}

	return id, true
}

func (a app) missing(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusNotFound, "receipts-missing", nil)
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, receiptbus.ErrNotFound), errors.Is(err, filebus.ErrNotFound):
		a.missing(w, r)
	case errors.Is(err, receiptbus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "receipts-refused", nil)
	default:
		a.cfg.Log.Error("a request about receipts failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "receipts-refused", "server")
	}
}

func form(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return false
	}

	return true
}

// back is where a form asked to be sent afterwards, if it is one of this
// site's own pages, else the fallback; with done added to its query.
func back(w http.ResponseWriter, r *http.Request, fallback, done string) {
	to := fallback
	if next := r.PostForm.Get("back"); next != "" && mid.SafeNext(next) == next {
		to = next
	}

	if done != "" {
		u, err := url.Parse(to)
		if err == nil {
			q := u.Query()
			q.Set("done", done)
			u.RawQuery = q.Encode()
			to = u.String()
		}
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// homePath is an inbox's page.
func homePath(home types.Scope) string {
	kind := "accounts"
	if home.Kind == types.ScopeProject {
		kind = "projects"
	}

	return "/" + kind + "/" + home.ID.String()
}

// --- uploading --------------------------------------------------------------

// received is what one upload brought: the files kept, and the names of
// those that were not, by why.
type received struct {
	files    []types.ID
	together bool
	note     string

	// number and payee are a check image's (uploadChecks).
	number, payee string

	wrongKind []string
	tooBig    []string
	cut       bool // the request ended early: too big, or the connection went
}

// receive streams the form's files to disk one at a time, never holding
// one in memory, because the host stops a process near 300 MB.
//
// A file that is not a receipt, or too big, is named and skipped rather
// than failing the rest; an upload cut off part way keeps what arrived
// whole. Nothing that arrived is dropped without being said.
func (a app) receive(r *http.Request, me types.ID) (received, error) {
	var got received

	mr, err := r.MultipartReader()
	if err != nil {
		return got, nil
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return got, nil
		}

		if err != nil {
			got.cut = true

			return got, nil
		}

		switch name := part.FormName(); {
		case name == "together":
			got.together = true
		case name == "note":
			note, _ := io.ReadAll(io.LimitReader(part, receiptbus.MaxNote*4))
			got.note = strings.TrimSpace(string(note))
		case name == "number":
			number, _ := io.ReadAll(io.LimitReader(part, 64))
			got.number = strings.TrimSpace(string(number))
		case name == "payee":
			payee, _ := io.ReadAll(io.LimitReader(part, ledgerbus.MaxPayee*4+1))
			got.payee = strings.TrimSpace(string(payee))
		case name == "files" && part.FileName() != "":
			if len(got.files) >= MaxFiles {
				got.tooBig = append(got.tooBig, part.FileName())

				break
			}

			f, err := a.cfg.Files.Save(r.Context(), a.cfg.Now(), me, part.FileName(), part, MaxFile, receiptbus.Accept)

			switch {
			case errors.Is(err, filebus.ErrType):
				got.wrongKind = append(got.wrongKind, part.FileName())
			case errors.Is(err, filebus.ErrTooBig):
				got.tooBig = append(got.tooBig, part.FileName())
			case err != nil:
				if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
					got.cut = true
					part.Close()

					return got, nil
				}

				part.Close()

				return got, err
			case f.Size > 0:
				got.files = append(got.files, f.ID)
			}
		}

		part.Close()
	}
}

// report is the query an upload's page is sent to, saying what happened.
func (got received) report(added int) url.Values {
	q := url.Values{"done": {"added"}, "n": {strconv.Itoa(added)}}

	for _, name := range got.wrongKind {
		q.Add("kind", name)
	}

	for _, name := range got.tooBig {
		q.Add("big", name)
	}

	if got.cut {
		q.Set("cut", "1")
	}

	return q
}

func (a app) upload(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		id, ok := a.pathID(w, r)
		if !ok {
			return
		}

		home := types.Scope{Kind: kind, ID: id}

		// Asked before a byte is stored: an upload somebody may not make is
		// refused, not kept.
		access, err := a.cfg.Tenancy.AccessTo(r.Context(), me.ID, home)

		switch {
		case err != nil:
			a.failed(w, r, err)

			return
		case !access.Can(tenancybus.Read):
			a.missing(w, r)

			return
		case !access.Can(tenancybus.Receipts):
			a.failed(w, r, receiptbus.ErrForbidden)

			return
		}

		got, err := a.receive(r, me.ID)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		added := 0

		if len(got.files) > 0 {
			receipts, err := a.cfg.Receipts.Add(r.Context(), a.cfg.Now(), me.ID, home, got.files, got.together, receiptbus.Details{Note: got.note})
			if err != nil {
				a.failed(w, r, err)

				return
			}

			added = len(receipts)
		}

		http.Redirect(w, r, homePath(home)+"/receipts?"+got.report(added).Encode(), http.StatusSeeOther)
	}
}

// uploadOnto is the transaction page's form: receipts straight onto it.
func (a app) uploadOnto(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	got, err := a.receive(r, me.ID)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	added := 0

	if len(got.files) > 0 {
		receipts, err := a.cfg.Receipts.AddToTransaction(r.Context(), a.cfg.Now(), me.ID, id, got.files, got.together, receiptbus.Details{Note: got.note})
		if err != nil {
			a.failed(w, r, err)

			return
		}

		added = len(receipts)
	}

	q := got.report(added)
	q.Set("done", "receipts")

	http.Redirect(w, r, "/transactions/"+id.String()+"?"+q.Encode(), http.StatusSeeOther)
}

// uploadChecks is the account's form for the images of checks: each is
// attached to the transaction with its number (receiptbus.AddChecks), and
// the others wait in the inbox, which says how many of each.
func (a app) uploadChecks(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	home := types.AccountScope(id)

	access, err := a.cfg.Tenancy.AccessTo(r.Context(), me.ID, home)

	switch {
	case err != nil:
		a.failed(w, r, err)

		return
	case !access.Can(tenancybus.Read):
		a.missing(w, r)

		return
	case !access.Can(tenancybus.Receipts):
		a.failed(w, r, receiptbus.ErrForbidden)

		return
	}

	got, err := a.receive(r, me.ID)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	q := got.report(0)
	q.Set("done", "checks")

	if len(got.files) > 0 {
		receipts, err := a.cfg.Receipts.AddChecks(r.Context(), a.cfg.Now(), me.ID, id, got.files, got.number, got.payee)

		invalid, isInvalid := errors.AsType[receiptbus.Invalid](err)

		switch {
		case isInvalid:
			q.Del("done")
			q.Set("check", invalid.Field)
		case err != nil:
			a.failed(w, r, err)

			return
		}

		waiting := 0

		for _, rc := range receipts {
			if rc.Waiting() {
				waiting++
			}
		}

		q.Set("n", strconv.Itoa(len(receipts)-waiting))
		q.Set("waiting", strconv.Itoa(waiting))
	}

	http.Redirect(w, r, homePath(home)+"/receipts?"+q.Encode(), http.StatusSeeOther)
}

// uploaded is what the page an upload lands on says about it, read from
// its query. ledgerapp reads the same query for an upload onto a
// transaction, with its own copy of these few lines: an app does not
// import another.
type uploaded struct {
	Added     int
	WrongKind []string
	TooBig    []string
	Cut       bool
}

func uploadedFrom(q url.Values) uploaded {
	n, _ := strconv.Atoi(q.Get("n"))

	return uploaded{Added: n, WrongKind: q["kind"], TooBig: q["big"], Cut: q.Get("cut") == "1"}
}

// --- an inbox ---------------------------------------------------------------

type inboxView struct {
	Name        string
	Path        string // the account's or project's page
	CanReceipts bool

	Inbox       receiptbus.Inbox
	Suggestions map[types.ID][]ledgerbus.Transaction
	Accounts    map[types.ID]tenancybus.Account

	Done     string
	Uploaded uploaded

	// Checks is whether the inbox takes check images (an account's), and
	// CheckProblem why the last ones were not added.
	Checks       bool
	CheckProblem string

	// CheckAdded is how many check images the last upload attached, and
	// CheckWaiting how many it left waiting.
	CheckAdded, CheckWaiting int
}

func (a app) inbox(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		id, ok := a.pathID(w, r)
		if !ok {
			return
		}

		ctx := r.Context()
		home := types.Scope{Kind: kind, ID: id}

		q := r.URL.Query()
		view := inboxView{
			Path: homePath(home), Done: q.Get("done"), Uploaded: uploadedFrom(q),
			Checks: kind == types.ScopeAccount, CheckProblem: q.Get("check"),
		}

		// Check images say so in their own words, not as receipts that
		// still want a date and an amount.
		if view.Done == "checks" {
			view.CheckAdded, view.Uploaded.Added = view.Uploaded.Added, 0
			view.CheckWaiting, _ = strconv.Atoi(q.Get("waiting"))
		}

		var err error

		if kind == types.ScopeAccount {
			var acct tenancybus.Account
			acct, _, err = a.cfg.Tenancy.Account(ctx, me.ID, id)
			view.Name = acct.Name
		} else {
			var p tenancybus.Project
			p, _, err = a.cfg.Tenancy.Project(ctx, me.ID, id)
			view.Name = p.Name
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		if view.Inbox, err = a.cfg.Receipts.Inbox(ctx, me.ID, home); err != nil {
			a.failed(w, r, err)

			return
		}

		view.CanReceipts = view.Inbox.Access.Can(tenancybus.Receipts)

		if view.Suggestions, view.Accounts, err = a.suggest(ctx, me.ID, view.Inbox.Waiting); err != nil {
			a.failed(w, r, err)

			return
		}

		a.cfg.Render.Render(w, r, http.StatusOK, "receipts-inbox", view)
	}
}

// suggest is the suggestions for some receipts, and the accounts they are
// in, for naming.
func (a app) suggest(ctx context.Context, me types.ID, receipts []receiptbus.Receipt) (map[types.ID][]ledgerbus.Transaction, map[types.ID]tenancybus.Account, error) {
	s, err := a.cfg.Receipts.Suggestions(ctx, me, receipts)
	if err != nil {
		return nil, nil, err
	}

	accounts := map[types.ID]tenancybus.Account{}

	for _, txs := range s {
		for _, t := range txs {
			if _, ok := accounts[t.AccountID]; ok {
				continue
			}

			acct, _, err := a.cfg.Tenancy.Account(ctx, me, t.AccountID)
			if err != nil {
				return nil, nil, err
			}

			accounts[t.AccountID] = acct
		}
	}

	return s, accounts, nil
}

// --- everything waiting -----------------------------------------------------

type waitingView struct {
	Waiting     []receiptbus.Receipt
	Suggestions map[types.ID][]ledgerbus.Transaction
	Accounts    map[types.ID]tenancybus.Account
	Done        string
}

func (a app) waiting(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	view := waitingView{Done: r.URL.Query().Get("done")}

	var err error

	if view.Waiting, err = a.cfg.Receipts.Waiting(ctx, me.ID); err != nil {
		a.failed(w, r, err)

		return
	}

	if view.Suggestions, view.Accounts, err = a.suggest(ctx, me.ID, view.Waiting); err != nil {
		a.failed(w, r, err)

		return
	}

	// Those with a suggestion first: they are a click each.
	slices.SortStableFunc(view.Waiting, func(x, y receiptbus.Receipt) int {
		return boolOrder(len(view.Suggestions[y.ID]) > 0) - boolOrder(len(view.Suggestions[x.ID]) > 0)
	})

	a.cfg.Render.Render(w, r, http.StatusOK, "receipts-waiting", view)
}

func boolOrder(b bool) int {
	if b {
		return 1
	}

	return 0
}

// --- one receipt ------------------------------------------------------------

type receiptView struct {
	View        receiptbus.View
	Suggestions []ledgerbus.Transaction
	Accounts    map[types.ID]tenancybus.Account
	UploadedBy  string
	HomeName    string
	HomePath    string
	Done        string
	Problem     string

	// Typed into the details form, kept when it is refused.
	Typed detailsForm
}

type detailsForm struct {
	SpentOn, Amount, Merchant, Note string
}

func formOf(d receiptbus.Details) detailsForm {
	f := detailsForm{SpentOn: d.SpentOn.String(), Merchant: d.Merchant, Note: d.Note}
	if d.HasAmount {
		f.Amount = d.Amount.String()
	}

	return f
}

func (a app) receipt(w http.ResponseWriter, r *http.Request) {
	a.receiptPage(w, r, http.StatusOK, nil, "")
}

func (a app) receiptPage(w http.ResponseWriter, r *http.Request, status int, typed *detailsForm, problem string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	ctx := r.Context()

	v, err := a.cfg.Receipts.Receipt(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := receiptView{View: v, Done: r.URL.Query().Get("done"), Problem: problem, Typed: formOf(v.Receipt.Details)}
	if typed != nil {
		view.Typed = *typed
	}

	if u, err := a.cfg.Users.ByID(ctx, v.Receipt.UploadedBy); err == nil {
		view.UploadedBy = u.Named()
	}

	// The inbox is named only for a reader of it: somebody who sees the
	// receipt through its transaction may not know the project's name.
	if access, err := a.cfg.Tenancy.AccessTo(ctx, me.ID, v.Receipt.Home); err == nil && access.Can(tenancybus.Read) {
		view.HomePath = homePath(v.Receipt.Home)

		if v.Receipt.Home.Kind == types.ScopeAccount {
			acct, _, _ := a.cfg.Tenancy.Account(ctx, me.ID, v.Receipt.Home.ID)
			view.HomeName = acct.Name
		} else {
			p, _, _ := a.cfg.Tenancy.Project(ctx, me.ID, v.Receipt.Home.ID)
			view.HomeName = p.Name
		}
	}

	if v.Receipt.Waiting() {
		s, accounts, err := a.suggest(ctx, me.ID, []receiptbus.Receipt{v.Receipt})
		if err != nil {
			a.failed(w, r, err)

			return
		}

		view.Suggestions, view.Accounts = s[v.Receipt.ID], accounts
	}

	if view.Accounts == nil {
		view.Accounts = map[types.ID]tenancybus.Account{}
	}

	for _, t := range v.Transactions {
		if _, ok := view.Accounts[t.AccountID]; !ok && v.Readable[t.ID] {
			acct, _, _ := a.cfg.Tenancy.Account(ctx, me.ID, t.AccountID)
			view.Accounts[t.AccountID] = acct
		}
	}

	a.cfg.Render.Render(w, r, status, "receipt", view)
}

// file serves one page of a receipt. A photo inline, so that a page can
// show it; anything else -- a PDF, a HEIC most browsers cannot show -- as a
// download, so that nothing uploaded is ever rendered as a document on
// this site's origin.
func (a app) file(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		a.missing(w, r)

		return
	}

	f, err := a.cfg.Receipts.File(r.Context(), me.ID, id, n)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	rc, err := a.cfg.Files.Open(f)
	if err != nil {
		a.failed(w, r, err)

		return
	}
	defer rc.Close()

	disposition := "attachment"
	if Shown(f.ContentType) {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))

	// Private and cached for an hour: the same photo is on the inbox, the
	// receipt and the transaction, and a phone should not fetch eight
	// megabytes three times.
	w.Header().Set("Cache-Control", "private, max-age=3600")

	http.ServeContent(w, r, "", f.UploadedAt, rc)
}

// picture is a photo's smaller picture (filebus.Picture), asked of the
// receipt exactly as the original is. A file with none -- a PDF, a WebP, a
// photo too small to need one -- is sent on to the original, so that a
// page may ask for the small picture of any photo.
func (a app) picture(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		a.missing(w, r)

		return
	}

	f, err := a.cfg.Receipts.File(r.Context(), me.ID, id, n)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	rc, err := a.cfg.Files.Picture(r.Context(), f, filebus.Size(r.PathValue("size")))
	if errors.Is(err, filebus.ErrNoPicture) {
		http.Redirect(w, r, "/receipts/"+id.String()+"/files/"+strconv.Itoa(n), http.StatusSeeOther)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", filebus.JPEG)
	w.Header().Set("Content-Disposition", "inline")

	// A picture of a photo never changes, so a day; private, as the
	// original is.
	w.Header().Set("Cache-Control", "private, max-age=86400")

	http.ServeContent(w, r, "", f.UploadedAt, rc)
}

// Shown reports whether a file is a photo a browser shows in a page.
func Shown(contentType string) bool {
	switch contentType {
	case filebus.JPEG, filebus.PNG, filebus.WebP:
		return true
	}

	return false
}

// --- changing one -----------------------------------------------------------

func (a app) details(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	typed := detailsForm{
		SpentOn:  strings.TrimSpace(r.PostForm.Get("spent_on")),
		Amount:   strings.TrimSpace(r.PostForm.Get("amount")),
		Merchant: strings.TrimSpace(r.PostForm.Get("merchant")),
		Note:     strings.TrimSpace(r.PostForm.Get("note")),
	}

	d, problem := typed.parse()
	if problem != "" {
		a.receiptPage(w, r, http.StatusUnprocessableEntity, &typed, problem)

		return
	}

	_, err := a.cfg.Receipts.SetDetails(r.Context(), a.cfg.Now(), me.ID, id, d)
	if invalid, ok := errors.AsType[receiptbus.Invalid](err); ok {
		a.receiptPage(w, r, http.StatusUnprocessableEntity, &typed, invalid.Field)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/receipts/"+id.String(), "saved")
}

// number says which check a waiting check image is (receiptbus.NumberCheck).
func (a app) number(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	rc, err := a.cfg.Receipts.NumberCheck(r.Context(), a.cfg.Now(), me.ID, id, r.PostForm.Get("number"))

	switch {
	case errors.Is(err, receiptbus.ErrAttached):
		a.receiptPage(w, r, http.StatusUnprocessableEntity, nil, "attached")

		return
	case errors.Is(err, receiptbus.ErrRemoved):
		a.receiptPage(w, r, http.StatusUnprocessableEntity, nil, "removed")

		return
	}

	if invalid, ok := errors.AsType[receiptbus.Invalid](err); ok {
		a.receiptPage(w, r, http.StatusUnprocessableEntity, nil, invalid.Field)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	done := "numbered"
	if !rc.Waiting() {
		done = "attached"
	}

	back(w, r, "/receipts/"+id.String(), done)
}

// parse reads the details as typed, or names the field that is not one.
func (f detailsForm) parse() (receiptbus.Details, string) {
	d := receiptbus.Details{Merchant: f.Merchant, Note: f.Note}

	if f.SpentOn != "" {
		on, err := types.ParseDate(f.SpentOn)
		if err != nil {
			return d, "spent_on"
		}

		d.SpentOn = on
	}

	if f.Amount != "" {
		amount, err := typedAmount(f.Amount)
		if err != nil {
			return d, "amount"
		}

		d.Amount, d.HasAmount = amount.Abs(), true
	}

	return d, ""
}

// typedAmount reads an amount as a person types it: "12.50", "12,50",
// "1.234,56", "$12.50". The last separator followed by one or two digits
// is the decimal one.
func typedAmount(s string) (money.Amount, error) {
	s = strings.TrimSpace(strings.TrimLeft(s, "$€£R "))

	last := strings.LastIndexAny(s, ".,")
	if last >= 0 && s[last] == ',' && len(s)-last-1 <= 2 {
		s = strings.ReplaceAll(s[:last], ".", "") + "." + s[last+1:]
	}

	return money.Parse(strings.ReplaceAll(s, ",", ""))
}

// transactionOf is the transaction a form names.
func (a app) transactionOf(w http.ResponseWriter, r *http.Request) (types.ID, bool) {
	id, err := types.ParseID(r.PostForm.Get("transaction"))
	if err != nil {
		a.missing(w, r)

		return types.ID{}, false
	}

	return id, true
}

func (a app) attach(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	t, ok := a.transactionOf(w, r)
	if !ok {
		return
	}

	if _, err := a.cfg.Receipts.Attach(r.Context(), a.cfg.Now(), me.ID, id, t); err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/receipts/"+id.String(), "attached")
}

func (a app) detach(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	t, ok := a.transactionOf(w, r)
	if !ok {
		return
	}

	if _, err := a.cfg.Receipts.Detach(r.Context(), me.ID, id, t); err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/receipts/"+id.String(), "detached")
}

func (a app) remove(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	removed := r.PostForm.Get("removed") == "1"

	rec, err := a.cfg.Receipts.SetRemoved(r.Context(), a.cfg.Now(), me.ID, id, removed)
	if errors.Is(err, receiptbus.ErrAttached) {
		a.receiptPage(w, r, http.StatusUnprocessableEntity, nil, "attached")

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	done := "restored"
	if removed {
		done = "removed"
	}

	back(w, r, homePath(rec.Home)+"/receipts", done)
}
