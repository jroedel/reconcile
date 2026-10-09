// Package ledgerapp is the pages for an account's money: its transactions by
// month, its statements, and uploading the next one.
//
// Uploading is two steps, because a CSV's columns are a guess until a
// person has looked at them. The file is stored first (filebus); the
// preview page then reads it with the columns remembered for its header, or
// guessed, shows the first rows as they will be imported, checks them
// against the balances, and counts what is new. Import is a button on that
// page, and it is greyed out until the statement balances or there is
// nothing to check it against.
//
// As in tenancyapp, everything the reader holds no role on is the same 404
// as an address that does not exist.
package ledgerapp

import (
	"context"
	"embed"
	"errors"
	"fmt"
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
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
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

// UploadPattern is the one route that takes a file. The muxer gives it a
// branch of its own, with a body limit to fit (MaxUpload) and multipart
// only, instead of the 64 KB every form has.
const UploadPattern = "POST /accounts/{id}/statements"

// MaxUpload is the most an upload's body may be: the file, and room for
// the multipart wrapping around it.
const MaxUpload = ledgerbus.MaxFile + 64<<10

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
}

// Users names who imported a statement.
type Users interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Ledger  *ledgerbus.Business
	Tenancy *tenancybus.Business
	Files   *filebus.Business
	Users   Users

	// Categories names the parts on the transaction list.
	Categories *categorybus.Business

	// Receipts are shown on a transaction, a month and a project's book.
	Receipts *receiptbus.Business

	// Rules is where the transaction page's "always sort charges like
	// this" box writes.
	Rules  *rulebus.Business
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

	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, guard(h)) }

	handle("GET /accounts/{id}/transactions", a.transactions)
	handle(UploadPattern, a.upload)
	handle("GET /accounts/{id}/imports/{file}", a.preview)
	handle("POST /accounts/{id}/imports/{file}", a.importFile)
	handle("GET /statements/{id}", a.statement)
	handle("GET /statements/{id}/file", a.download)
	handle("POST /statements/{id}/remove", a.remove)
	handle("POST /statements/{id}/reconcile", a.reconcile)
	handle("POST /statements/{id}/reopen", a.reopen)
	handle("GET /accounts/{id}/months", a.months)
	handle("GET /transactions/{id}", a.transaction)
	handle("POST /transactions/{id}", a.sortTransaction)
	handle("GET /projects/{id}/book", a.book)
}

// --- the shared tail --------------------------------------------------------

func actor(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
	}

	return u, ok
}

func (a app) pathID(w http.ResponseWriter, r *http.Request, name string) (types.ID, bool) {
	id, err := types.ParseID(r.PathValue(name))
	if err != nil {
		a.missing(w, r)

		return types.ID{}, false
	}

	return id, true
}

func (a app) missing(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusNotFound, "ledger-missing", nil)
}

// failed answers an error that is not about the file: nothing there, not
// allowed, or broken.
func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ledgerbus.ErrNotFound), errors.Is(err, filebus.ErrNotFound):
		a.missing(w, r)
	case errors.Is(err, ledgerbus.ErrForbidden):
		a.cfg.Render.Render(w, r, http.StatusForbidden, "ledger-refused", nil)
	default:
		a.cfg.Log.Error("a request about statements failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "ledger-refused", "server")
	}
}

// problem is the code a page words for an error about the file, or "".
func problem(err error) string {
	switch {
	case errors.Is(err, ledgerbus.ErrPDF):
		return "pdf"
	case errors.Is(err, ledgerbus.ErrUnreadable):
		return "unreadable"
	case errors.Is(err, ledgerbus.ErrEmpty):
		return "empty"
	case errors.Is(err, ledgerbus.ErrUnbalanced):
		return "unbalanced"
	case errors.Is(err, ledgerbus.ErrSameFile):
		return "same-file"
	case errors.Is(err, ledgerbus.ErrLocked):
		return "locked"
	case errors.Is(err, filebus.ErrTooBig):
		return "too-big"
	}

	return ""
}

func back(w http.ResponseWriter, r *http.Request, to, done string) {
	if done != "" {
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}

		to += sep + "done=" + url.QueryEscape(done)
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// --- an account's transactions ----------------------------------------------

type transactionsView struct {
	Account     tenancybus.Account
	Path        string
	CanBookkeep bool

	Months     []ledgerbus.Month
	Month      string
	Shown      []ledgerbus.Transaction
	Statements []ledgerbus.Statement

	// Unsorted shows only the month's transactions with a part that has
	// no category: the treasurer's to-do list. ByRule shows only those a
	// sorting rule sorted, for checking what a statement brought.
	Unsorted bool
	ByRule   bool

	// Names of the categories and projects the shown parts point at.
	Categories map[types.ID]string
	Projects   map[types.ID]string

	// Receipts is how many each shown transaction has.
	Receipts map[types.ID]int

	Problem string
	Done    string
}

func (a app) transactions(w http.ResponseWriter, r *http.Request) {
	a.transactionsPage(w, r, http.StatusOK, "")
}

func (a app) transactionsPage(w http.ResponseWriter, r *http.Request, status int, problem string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()

	account, access, err := a.cfg.Tenancy.Account(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := transactionsView{
		Account:     account,
		Path:        "/accounts/" + id.String(),
		CanBookkeep: access.Can(tenancybus.Bookkeep),
		Problem:     problem,
		Done:        r.URL.Query().Get("done"),
	}

	if view.Months, err = a.cfg.Ledger.Months(ctx, me.ID, id); err != nil {
		a.failed(w, r, err)

		return
	}

	if view.Statements, err = a.cfg.Ledger.Statements(ctx, me.ID, id); err != nil {
		a.failed(w, r, err)

		return
	}

	// The month asked for, or the newest there is.
	view.Month = r.URL.Query().Get("month")
	if view.Month == "" && len(view.Months) > 0 {
		view.Month = view.Months[0].Month
	}

	if view.Month != "" {
		if view.Shown, err = a.cfg.Ledger.Transactions(ctx, me.ID, id, view.Month); err != nil {
			a.failed(w, r, err)

			return
		}
	}

	switch q := r.URL.Query(); {
	case q.Get("unsorted") == "1":
		view.Unsorted = true
		view.Shown = slices.DeleteFunc(view.Shown, ledgerbus.Transaction.Sorted)
	case q.Get("byrule") == "1":
		view.ByRule = true
		view.Shown = slices.DeleteFunc(view.Shown, func(t ledgerbus.Transaction) bool { return !t.ByRule() })
	}

	if err := a.names(r, account, &view); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, status, "transactions", view)
}

// names fills in what the shown parts' categories and projects are called.
// The reader may read the account, which was asked first: the names of
// where its own money went are part of it (tenancybus.ProjectNames).
func (a app) names(r *http.Request, account tenancybus.Account, view *transactionsView) error {
	cats, err := a.cfg.Categories.ForAccount(r.Context(), account)
	if err != nil {
		return err
	}

	view.Categories = make(map[types.ID]string, len(cats))
	for _, c := range cats {
		view.Categories[c.ID] = c.Name
	}

	var projects []types.ID

	for _, t := range view.Shown {
		for _, s := range t.Splits {
			if !s.ProjectID.Zero() {
				projects = append(projects, s.ProjectID)
			}
		}
	}

	if view.Projects, err = a.cfg.Tenancy.ProjectNames(r.Context(), projects); err != nil {
		return err
	}

	ids := make([]types.ID, len(view.Shown))
	for i, t := range view.Shown {
		ids[i] = t.ID
	}

	on, err := a.cfg.Receipts.OnTransactions(r.Context(), ids)

	view.Receipts = make(map[types.ID]int, len(on))
	for id, rs := range on {
		view.Receipts[id] = len(rs)
	}

	return err
}

// --- uploading --------------------------------------------------------------

// upload streams the file to disk and sends the person to its preview.
//
// Read part by part rather than with ParseMultipartForm, which would write
// the file to a temporary directory first and then again to the store.
func (a app) upload(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	// Asked before a byte is stored: a viewer's upload is refused, not kept.
	_, access, err := a.cfg.Tenancy.Account(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	if !access.Can(tenancybus.Bookkeep) {
		a.failed(w, r, ledgerbus.ErrForbidden)

		return
	}

	f, err := a.receive(r, me.ID)

	switch {
	case errors.Is(err, errNoFile):
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "no-file")
	case problem(err) != "":
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, problem(err))
	case err != nil:
		a.failed(w, r, err)
	default:
		http.Redirect(w, r, "/accounts/"+id.String()+"/imports/"+f.ID.String(), http.StatusSeeOther)
	}
}

var errNoFile = errors.New("no file was chosen")

// receive saves the form's "file" part.
func (a app) receive(r *http.Request, me types.ID) (filebus.File, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return filebus.File{}, errNoFile
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return filebus.File{}, errNoFile
		}

		if err != nil {
			return filebus.File{}, tooBig(err)
		}

		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()

			continue
		}

		f, err := a.cfg.Files.Save(r.Context(), a.cfg.Now(), me, part.FileName(), part, ledgerbus.MaxFile, nil)
		part.Close()

		if err != nil {
			return filebus.File{}, tooBig(err)
		}

		if f.Size == 0 {
			return filebus.File{}, errNoFile
		}

		return f, nil
	}
}

// tooBig is a body cut off by the muxer's limit, said as the file being too
// big, which is what it is.
func tooBig(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return filebus.ErrTooBig
	}

	return err
}

// --- the preview ------------------------------------------------------------

type previewView struct {
	Path  string
	Draft ledgerbus.Draft

	// Rows is the first rows as they will be imported.
	Rows []importbus.Record
	More int

	DateFormats []string

	// Typed into the balance fields, kept as typed.
	Opening, Closing string

	Problem string
}

// Options is the choices for one column's select: the file's headings,
// and an empty choice where the column is optional. A heading remembered
// from another file and missing from this one is offered too, so that the
// select shows what was chosen rather than quietly the first heading.
func (v previewView) Options(chosen string, optional bool) []string {
	var out []string
	if optional {
		out = append(out, "")
	}

	out = append(out, v.Draft.Inspection.Header...)

	if chosen != "" && !slices.Contains(out, chosen) {
		out = append(out, chosen)
	}

	return out
}

// Example is a date layout as the last day of July 2026 would be written
// in it: a day past the twelfth, so day-first and month-first look
// different.
func (previewView) Example(layout string) string {
	return time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC).Format(layout)
}

// previewRows is how many rows the preview shows.
const previewRows = 10

func (a app) preview(w http.ResponseWriter, r *http.Request) {
	a.previewPage(w, r, nil, "", "", "")
}

func (a app) previewPage(w http.ResponseWriter, r *http.Request, opts *ledgerbus.Options, opening, closing, problem string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	file, ok := a.pathID(w, r, "file")
	if !ok {
		return
	}

	d, err := a.cfg.Ledger.Prepare(r.Context(), me.ID, id, file, opts)

	if code := problemOf(err); code != "" {
		// Not a statement at all: back to the account, which says so.
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, code)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := previewView{
		Path:        "/accounts/" + id.String() + "/imports/" + file.String(),
		Draft:       d,
		DateFormats: csvsource.DateFormats,
		Opening:     opening,
		Closing:     closing,
		Problem:     problem,
	}

	view.Rows = d.Result.Records[:min(len(d.Result.Records), previewRows)]
	view.More = len(d.Result.Records) - len(view.Rows)

	// The balances the file states, until somebody types their own.
	if opts == nil {
		if d.Opening.Known {
			view.Opening = d.Opening.Amount.String()
		}

		if d.Closing.Known {
			view.Closing = d.Closing.Amount.String()
		}
	}

	if d.Unmapped && view.Problem == "" {
		view.Problem = "unmapped"
	}

	status := http.StatusOK
	if problem != "" {
		status = http.StatusUnprocessableEntity
	}

	a.cfg.Render.Render(w, r, status, "statement-preview", view)
}

// problemOf is problem for the errors that mean the file is not one this
// can read at all.
func problemOf(err error) string {
	switch {
	case errors.Is(err, ledgerbus.ErrPDF), errors.Is(err, ledgerbus.ErrUnreadable):
		return problem(err)
	}

	return ""
}

// importFile is the preview's form: look again with other columns or
// balances, or import.
func (a app) importFile(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	file, ok := a.pathID(w, r, "file")
	if !ok {
		return
	}

	opts, bad := options(r)
	opening, closing := r.PostForm.Get("opening"), r.PostForm.Get("closing")

	if bad != "" || r.PostForm.Get("action") != "import" {
		a.previewPage(w, r, &opts, opening, closing, bad)

		return
	}

	st, err := a.cfg.Ledger.Import(r.Context(), a.cfg.Now(), me.ID, id, file, opts)
	if code := problem(err); code != "" {
		a.previewPage(w, r, &opts, opening, closing, code)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/statements/"+st.ID.String(), "imported")
}

// options reads the preview's form, and the code of a balance that is not
// an amount.
func options(r *http.Request) (ledgerbus.Options, string) {
	f := r.PostForm

	skip, _ := strconv.Atoi(f.Get("skip"))

	m := csvsource.Mapping{
		Date:         f.Get("date"),
		Description:  f.Get("description"),
		Balance:      f.Get("balance"),
		DateFormat:   f.Get("date_format"),
		Separator:    f.Get("separator"),
		DecimalComma: f.Get("decimal_comma") == "1",
		SkipLines:    min(max(skip, 0), 50),
		Invert:       f.Get("invert") == "1",
	}

	// One signed column, or two unsigned ones: whichever was chosen.
	if f.Get("columns") == "split" {
		m.Debit, m.Credit = f.Get("debit"), f.Get("credit")
	} else {
		m.Amount = f.Get("amount")
	}

	opts := ledgerbus.Options{Mapping: m}

	var bad string

	for name, into := range map[string]*importbus.Balance{"opening": &opts.Opening, "closing": &opts.Closing} {
		s := strings.TrimSpace(f.Get(name))
		if s == "" {
			continue
		}

		amount, err := typedAmount(s)
		if err != nil {
			bad = name

			continue
		}

		*into = importbus.Balance{Amount: amount, Known: true}
	}

	return opts, bad
}

// typedAmount reads a balance as a person types it, in whichever
// convention they write: "1,234.56" and "1.234,56" are the same, and so are
// "12,5" and "12.5". The last separator followed by one or two digits is
// the decimal one; three digits after it is thousands.
func typedAmount(s string) (money.Amount, error) {
	last := strings.LastIndexAny(s, ".,")
	comma := last >= 0 && s[last] == ',' && len(strings.TrimRight(s[last+1:], " ")) <= 2

	return csvsource.ParseAmount(s, comma)
}

// --- a statement's file -------------------------------------------------------

// download sends the file a statement was read from, as an attachment
// whatever it is: a file somebody uploaded is never shown inline on this
// site's origin.
func (a app) download(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	f, err := a.cfg.Ledger.StatementFile(r.Context(), me.ID, id)
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

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	w.Header().Set("Cache-Control", "private, no-store")

	http.ServeContent(w, r, "", f.UploadedAt, rc)
}

func (a app) remove(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	st, err := a.cfg.Ledger.RemoveStatement(r.Context(), a.cfg.Now(), me.ID, id)
	if errors.Is(err, ledgerbus.ErrLocked) {
		a.statementPage(w, r, http.StatusConflict, statementForm{Problem: "remove-locked"})

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, fmt.Sprintf("/accounts/%s/transactions", st.AccountID), "removed")
}
