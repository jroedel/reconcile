// Package reportapp is the report pages (docs/reports.md): the styles a
// scope's books can be reported in, a style run for a period with its
// choices in the address, the parts behind any figure, and the same figures
// as a spreadsheet. Printing is the same page, through the print
// stylesheet.
//
// Everything a report says is reportbus's; this package reads the address,
// asks, and lays the answer out. Nothing here is saved: a style run with
// its choices in the address is a link that comes out the same way every
// time, which is what a saved report will be built on.
package reportapp

import (
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
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
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/web"
)

//go:embed templates text
var files embed.FS

// Templates is this app's pages and texts, for page.NewRenderer.
var Templates fs.FS = files

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
	Text(lang types.Lang, name string, data any) (string, error)
}

// Users names who wrote a note.
type Users interface {
	ByID(ctx context.Context, id types.ID) (userbus.User, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Reports *reportbus.Business
	Users   Users
	Render  Renderer

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

	for _, s := range []struct {
		prefix string
		kind   types.ScopeKind
	}{{"/orgs", types.ScopeOrg}, {"/accounts", types.ScopeAccount}, {"/projects", types.ScopeProject}} {
		handle("GET "+s.prefix+"/{id}/reports", a.list(s.kind))
		handle("POST "+s.prefix+"/{id}/reports", a.save(s.kind))
		handle("GET "+s.prefix+"/{id}/reports/{style}", a.run(s.kind))
		handle("GET "+s.prefix+"/{id}/reports/{style}/parts", a.parts(a.unsaved(s.kind)))
		handle("GET "+s.prefix+"/{id}/reports/{style}/download", a.download(a.unsaved(s.kind)))
	}

	handle("GET /reports/{id}", a.runSaved)
	handle("GET /reports/{id}/parts", a.parts(a.saved))
	handle("GET /reports/{id}/download", a.download(a.saved))
	handle("GET /reports/{id}/edit", a.edit)
	handle("POST /reports/{id}/edit", a.change)
	handle("POST /reports/{id}/note", a.note)
	handle("POST /reports/{id}/remove", a.remove)
}

func actor(w http.ResponseWriter, r *http.Request) (userbus.User, bool) {
	u, ok := mid.UserFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
	}

	return u, ok
}

func (a app) failed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, reportbus.ErrNotFound) {
		a.cfg.Render.Render(w, r, http.StatusNotFound, "report-missing", nil)

		return
	}

	if errors.Is(err, reportbus.ErrForbidden) {
		a.cfg.Render.Render(w, r, http.StatusForbidden, "report-missing", "refused")

		return
	}

	a.cfg.Log.Error("a request about a report failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	a.cfg.Render.Render(w, r, http.StatusInternalServerError, "report-missing", "server")
}

// scope is the books a request names, of a kind.
func (a app) scope(w http.ResponseWriter, r *http.Request, kind types.ScopeKind) (types.Scope, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		a.failed(w, r, reportbus.ErrNotFound)

		return types.Scope{}, false
	}

	return types.Scope{Kind: kind, ID: id}, true
}

// style is the style a request names, if it takes the kind of scope.
func (a app) style(w http.ResponseWriter, r *http.Request, kind types.ScopeKind) (reportbus.Style, bool) {
	s, ok := reportbus.StyleOf(r.PathValue("style"))
	if !ok || !slices.Contains(s.Scopes, kind) {
		a.failed(w, r, reportbus.ErrNotFound)

		return reportbus.Style{}, false
	}

	return s, true
}

// --- the list ----------------------------------------------------------------------

type listView struct {
	Books   reportbus.Books
	Path    string
	Styles  []reportbus.Style
	Saved   []reportbus.Saved
	CanKeep bool
	Done    string
}

func (a app) list(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok {
			return
		}

		scope, ok := a.scope(w, r, kind)
		if !ok {
			return
		}

		books, err := a.cfg.Reports.Books(r.Context(), me.ID, scope)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		saved, err := a.cfg.Reports.SavedOf(r.Context(), me.ID, scope)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		a.cfg.Render.Render(w, r, http.StatusOK, "reports", listView{
			Books: books, Path: page.ScopePath(scope), Styles: reportbus.StylesFor(kind), Saved: saved,
			CanKeep: books.Access.Can(tenancybus.Bookkeep), Done: r.URL.Query().Get("done"),
		})
	}
}

// --- the address ---------------------------------------------------------------------

// asked is what an address asks of a style: its period and its choices,
// each style reading its own. A period that is not one is no period, which
// the style defaults, and the page says so.
type asked struct {
	period  reportbus.Period
	choices reportbus.IncomeExpensesChoices
	project reportbus.ProjectChoices
	bad     bool
}

// gapOf reads a quiet gap as a form or an address writes it: nothing is
// the default, and what is not a number is -1, which a saved report
// refuses and a run defaults.
func gapOf(s string) int {
	if s = strings.TrimSpace(s); s == "" {
		return 0
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}

	return n
}

func askedOf(r *http.Request) asked {
	q := r.URL.Query()

	var ask asked

	switch {
	case q.Get("period") == "all":
		ask.period = reportbus.Period{All: true}
	case q.Get("from") != "" || q.Get("to") != "":
		p, err := reportbus.NewPeriod(q.Get("from"), q.Get("to"))
		ask.period, ask.bad = p, err != nil
	}

	for _, s := range q["account"] {
		if id, err := types.ParseID(s); err == nil {
			ask.choices.Accounts = append(ask.choices.Accounts, id)
		}
	}

	ask.choices.PassThrough = reportbus.PassThroughShown(q.Get("passthrough"))
	ask.choices.Detail = reportbus.Detail(q.Get("detail"))
	ask.choices.Compare = reportbus.Compared(q.Get("compare"))
	ask.project = reportbus.ProjectChoices{Gap: gapOf(q.Get("gap")), HideAccounts: q.Get("accounts") == "hidden", HideBudget: q.Get("budget") == "hidden"}

	return ask
}

// choiceValues is a report's choices in an address: only what differs from
// the defaults, so that the plain link is the plain report.
func choiceValues(r reportbus.IncomeExpenses) url.Values {
	v := url.Values{}

	if r.Partial {
		for _, acct := range r.Chosen {
			v.Add("account", acct.ID.String())
		}
	}

	if r.Choices.PassThrough == reportbus.EachCategory {
		v.Set("passthrough", string(reportbus.EachCategory))
	}

	if r.Choices.Detail == reportbus.EachGroupRow {
		v.Set("detail", string(reportbus.EachGroupRow))
	}

	if r.Choices.Compare != reportbus.WithNothing {
		v.Set("compare", string(r.Choices.Compare))
	}

	return v
}

// projectValues is a project report's choices in an address, as
// choiceValues is the monthly one's.
func projectValues(r reportbus.ProjectReport) url.Values {
	v := url.Values{}

	if r.Choices.Gap != reportbus.DefaultGap {
		v.Set("gap", strconv.Itoa(r.Choices.Gap))
	}

	if r.Choices.HideAccounts {
		v.Set("accounts", "hidden")
	}

	if r.Choices.HideBudget {
		v.Set("budget", "hidden")
	}

	return v
}

// periodValues is a period in an address.
func periodValues(v url.Values, p reportbus.Period) url.Values {
	v = cloned(v)

	if p.All {
		v.Set("period", "all")
	} else if p.From != "" {
		v.Set("from", p.From)
		v.Set("to", p.To)
	}

	return v
}

func cloned(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = slices.Clone(vs)
	}

	return out
}

// --- running it ----------------------------------------------------------------------

type reportView struct {
	// Frame is the books and the period, whichever the style; Report is
	// income and expenses by month's run, and Project a project's, the
	// other one zero.
	Frame   reportbus.Frame
	Report  reportbus.IncomeExpenses
	Project reportbus.ProjectReport
	Style   reportbus.Style
	Path    string // the books' page
	Base    string // the style's page for these books, or the saved report's
	Problem string
	Done    string
	Today   types.Date

	// Saved is the saved report run, nil for a style run without saving;
	// NoteBy names who wrote its period's note. CanKeep is whether the
	// reader keeps the books: may save the report, change it, and write
	// its notes.
	Saved   *reportbus.SavedRun
	NoteBy  string
	CanKeep bool

	// Typed is what was typed into the save form, shown again with a
	// problem.
	Typed reportbus.Fields

	choices url.Values
}

// PageScope is the books the report adds up, for the top bar: a saved
// report's address names only the report.
func (v reportView) PageScope() types.Scope { return v.Frame.Books.Scope }

// Preset is the key of the preset the period shown is, or "".
func (v reportView) Preset() string {
	for _, pr := range v.Frame.Presets {
		if pr.Period == v.Frame.Period {
			return pr.Key
		}
	}

	return ""
}

// NoteDate is the day the period's note was written.
func (v reportView) NoteDate() types.Date {
	if v.Saved == nil {
		return types.Date{}
	}

	return types.DateOf(v.Saved.Note.At)
}

// PeriodFields is the period shown, as the hidden fields of a form.
func (v reportView) PeriodFields() url.Values { return periodValues(url.Values{}, v.Frame.Period) }

// Query is the report as it is in an address: its choices and period.
func (v reportView) Query() string { return periodValues(v.choices, v.Frame.Period).Encode() }

// For is the same report for another period, by preset key or as the
// period before ("before") or after ("after").
func (v reportView) For(which string) string {
	p := v.Frame.Period

	switch which {
	case "before":
		p = v.Frame.Before
	case "after":
		p = v.Frame.After
	default:
		for _, pr := range v.Frame.Presets {
			if pr.Key == which {
				p = pr.Period
			}
		}
	}

	return v.Base + "?" + periodValues(v.choices, p).Encode()
}

// Current reports whether a preset is the period shown.
func (v reportView) Current(key string) bool {
	for _, pr := range v.Frame.Presets {
		if pr.Key == key {
			return pr.Period == v.Frame.Period
		}
	}

	return false
}

// ChosenNames is the names of the accounts in the report.
func (v reportView) ChosenNames() []string {
	out := make([]string, len(v.Report.Chosen))
	for i, a := range v.Report.Chosen {
		out[i] = a.Name
	}

	return out
}

// Chosen reports whether an account is in the report, for its box.
func (v reportView) Chosen(id types.ID) bool {
	return slices.ContainsFunc(v.Report.Chosen, func(a tenancybus.Account) bool { return a.ID == id })
}

// Figure is the address of the parts behind a figure: by the dimensions
// named, comma-separated, with the kind ("unsorted" for parts not sorted
// yet), category and month a figure is of, "" for those it is not.
func (v reportView) Figure(by, kind, category, month, currency string) string {
	q := periodValues(v.choices, v.Frame.Period)
	q.Set("by", by)
	q.Set("currency", currency)

	for k, s := range map[string]string{"kind": kind, "category": category, "month": month} {
		if s != "" {
			q.Set(k, s)
		}
	}

	return v.Base + "/parts?" + q.Encode()
}

// RowFigure is the address of the parts behind a row's figure for a month,
// or for the period when month is "": a group's, or a category's.
func (v reportView) RowFigure(row reportbus.Row, month, currency string) string {
	if row.GroupID.Zero() {
		by := "category"
		if month != "" {
			by += ",month"
		}

		return v.Figure(by, "", row.CategoryID.String(), month, currency)
	}

	by := "group"
	if month != "" {
		by += ",month"
	}

	u := v.Figure(by, "", "", month, currency)

	return u + "&" + url.Values{"group": {row.GroupID.String()}}.Encode()
}

// Comparing is the comparison the table shows beside its total:
// "year-before", "budget", or "" -- also when the budget was asked for and
// there is none for the year, or the months are in two years, which the
// page says instead.
func (v reportView) Comparing() string {
	r := v.Report

	switch {
	case r.Choices.Compare == reportbus.WithYearBefore && !r.YearBefore.Zero():
		return "year-before"
	case r.Choices.Compare == reportbus.WithBudget && r.Budget.Set && r.Budget.Fits:
		return "budget"
	}

	return ""
}

// Columns is how many columns the table has: the row's name, a month to a
// column, the total, and two for a comparison.
func (v reportView) Columns() int {
	n := len(v.Report.Months) + 2
	if v.Comparing() != "" {
		n += 2
	}

	return n
}

// Grouped reports whether the books have groups of categories, or the
// report shows them: the choice of rows is worth offering only then.
func (v reportView) Grouped() bool {
	return len(v.Report.Headings) > 0 || v.Report.Choices.Detail == reportbus.EachGroupRow
}

// builder is how a page finds the report it is about: a style run with
// its choices in the address (unsaved), or a saved report (saved).
type builder func(w http.ResponseWriter, r *http.Request) (reportView, bool)

// unsaved is the style's report the address asks for.
func (a app) unsaved(kind types.ScopeKind) builder {
	return func(w http.ResponseWriter, r *http.Request) (reportView, bool) { return a.report(w, r, kind) }
}

// report is the style's report the address asks for.
func (a app) report(w http.ResponseWriter, r *http.Request, kind types.ScopeKind) (reportView, bool) {
	me, ok := actor(w, r)
	if !ok {
		return reportView{}, false
	}

	scope, ok := a.scope(w, r, kind)
	if !ok {
		return reportView{}, false
	}

	style, ok := a.style(w, r, kind)
	if !ok {
		return reportView{}, false
	}

	ask := askedOf(r)
	now := a.cfg.Now()

	v := reportView{Style: style, Path: page.ScopePath(scope), Base: page.ScopePath(scope) + "/reports/" + style.Key, Today: types.DateOf(now)}

	var err error

	switch style.Key {
	case reportbus.ProjectStyle:
		v.Project, err = a.cfg.Reports.ProjectReport(r.Context(), now, me.ID, scope, ask.period, ask.project)
		v.Frame, v.choices = v.Project.Frame, projectValues(v.Project)
	default:
		v.Report, err = a.cfg.Reports.IncomeExpenses(r.Context(), now, me.ID, scope, ask.period, ask.choices)
		v.Frame, v.choices = v.Report.Frame, choiceValues(v.Report)
	}

	if err != nil {
		a.failed(w, r, err)

		return reportView{}, false
	}

	v.CanKeep = v.Frame.Access.Can(tenancybus.Bookkeep)
	v.Problem = v.badPeriod(ask.bad)

	return v, true
}

// badPeriod is the problem of a period the address names that is not one,
// which each style defaults in its own way, or "".
func (v reportView) badPeriod(bad bool) string {
	switch {
	case !bad:
		return ""
	case v.Style.Key == reportbus.ProjectStyle:
		return "project-period"
	}

	return "period"
}

func (a app) run(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v, ok := a.report(w, r, kind); ok {
			a.show(w, r, http.StatusOK, v)
		}
	}
}

// show is a report's page.
func (a app) show(w http.ResponseWriter, r *http.Request, status int, v reportView) {
	a.cfg.Render.Render(w, r, status, "report-"+v.Style.Key, v)
}

// --- the parts behind a figure ------------------------------------------------------

type partsView struct {
	Report reportView
	Lines  []ledgerbus.ProjectLine
	Key    reportbus.Key
	Kind   string // as the address names it
	Group  string // the group's name, for a group's figure
	Sum    money.Amount
}

// PageScope is the report's books.
func (v partsView) PageScope() types.Scope { return v.Report.PageScope() }

func (a app) parts(build builder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := build(w, r)
		if !ok {
			return
		}

		me, _ := mid.UserFrom(r.Context())
		q := r.URL.Query()

		var by []reportbus.Dimension

		for d := range strings.SplitSeq(q.Get("by"), ",") {
			if dim := reportbus.Dimension(d); slices.Contains(reportbus.Dimensions, dim) && !slices.Contains(by, dim) {
				by = append(by, dim)
			}
		}

		k := reportbus.Key{Month: q.Get("month"), Currency: q.Get("currency")}

		switch q.Get("kind") {
		case "unsorted":
			k.Kind = categorybus.Unsaid
		default:
			k.Kind = categorybus.Kind(q.Get("kind"))
		}

		if id, err := types.ParseID(q.Get("category")); err == nil {
			k.CategoryID = id
		}

		if id, err := types.ParseID(q.Get("group")); err == nil {
			k.GroupID = id
		}

		question := reportbus.Question{Scope: v.Frame.Scope, Accounts: v.Report.Choices.Accounts, Period: v.Frame.Period, By: by}

		answer, lines, err := a.cfg.Reports.Parts(r.Context(), me.ID, question, k)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		view := partsView{Report: v, Lines: lines, Key: k, Kind: q.Get("kind"), Group: answer.Headings[k.GroupID].Name}
		for _, l := range lines {
			view.Sum += l.Split.Amount
		}

		a.cfg.Render.Render(w, r, http.StatusOK, "report-parts", view)
	}
}

// --- as a spreadsheet ---------------------------------------------------------------

// words is how many lines the report-words text has, in its order.
const words = 13

// download is the report as a CSV: a block per currency, a row per line of
// the table, the months across, with a byte-order mark so that a
// spreadsheet reads its accents, as the accountant's package does.
func (a app) download(build builder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := build(w, r)
		if !ok {
			return
		}

		if v.Style.Key == reportbus.ProjectStyle {
			a.projectSheet(w, r, v)

			return
		}

		said, err := a.lines(mid.LangFrom(r.Context()), "report-words", words)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		income, expenses, totalIncome, totalExpenses, net, unsorted, total, currency, owed := said[0], said[1], said[2], said[3], said[4], said[5], said[6], said[7], said[8]

		// The comparison's two columns, beside the total, as on the page.
		var compared []string

		switch v.Comparing() {
		case "year-before":
			compared = said[9:11]
		case "budget":
			compared = said[11:13]
		}

		var buf bytes.Buffer

		buf.WriteString("\ufeff")

		cw := csv.NewWriter(&buf)
		cw.UseCRLF = true

		rep := v.Report

		row := func(name string, ln reportbus.Row) {
			out := []string{cell(name)}
			for _, m := range ln.Months {
				out = append(out, m.String())
			}

			out = append(out, ln.Total.String())

			switch v.Comparing() {
			case "year-before":
				out = append(out, ln.Before.String(), ln.Change().String())
			case "budget":
				if ln.Budgeted {
					out = append(out, ln.Budget.String(), ln.AgainstBudget().String())
				}
			}

			_ = cw.Write(out)
		}

		for i, s := range rep.Sections {
			if i > 0 {
				_ = cw.Write(nil)
			}

			_ = cw.Write(append(append(append([]string{currency + ": " + s.Currency}, rep.Months...), total), compared...))

			_ = cw.Write([]string{income})
			for _, ln := range s.Income {
				row(ln.Name, ln)
			}

			row(totalIncome, s.IncomeTotal)

			_ = cw.Write([]string{expenses})
			for _, ln := range s.Expenses {
				row(ln.Name, ln)
			}

			row(totalExpenses, s.ExpensesTotal)
			row(net, s.Net)
			row(unsorted, s.Unsorted)

			_ = cw.Write([]string{owed, s.PassThroughTotal.String()})
		}

		name := "report.csv"
		if len(rep.Months) > 0 {
			name = fmt.Sprintf("report_%s_%s.csv", rep.Months[0], rep.Months[len(rep.Months)-1])
		}

		a.sheet(w, r, cw, &buf, name)
	}
}

// sheet sends a spreadsheet written into buf.
func (a app) sheet(w http.ResponseWriter, r *http.Request, cw *csv.Writer, buf *bytes.Buffer, name string) {
	cw.Flush()

	if err := cw.Error(); err != nil {
		a.failed(w, r, err)

		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(buf.Bytes())
}

// lines is a text's lines, which must be n.
func (a app) lines(lang types.Lang, name string, n int) ([]string, error) {
	text, err := a.cfg.Render.Text(lang, name, nil)
	if err != nil {
		return nil, err
	}

	var out []string

	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}

	if len(out) != n {
		return nil, fmt.Errorf("%s.txt has %d lines, not %d", name, len(out), n)
	}

	return out, nil
}

// cell keeps a spreadsheet from taking a name for a formula: one that
// starts with =, +, - or @ is read as text, behind a quote, as the
// accountant's package does.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}

	return s
}
