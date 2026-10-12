package reportapp

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/jroedel/reconcile/business/domain/report/reportbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// --- saved reports (docs/reports.md, "Saved reports") ----------------------------

// parsed reads a form, or answers that it could not be read.
func parsed(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return false
	}

	return true
}

// fields is what a form says of a saved report: its words, its default
// period and its choices.
func fields(r *http.Request) reportbus.Fields {
	f := reportbus.Fields{
		Title: r.PostForm.Get("title"), Description: r.PostForm.Get("description"), Period: r.PostForm.Get("default"),
		Choices: reportbus.IncomeExpensesChoices{
			PassThrough: reportbus.PassThroughShown(r.PostForm.Get("passthrough")),
			Detail:      reportbus.Detail(r.PostForm.Get("detail")),
		},
	}

	for _, s := range r.PostForm["account"] {
		if id, err := types.ParseID(s); err == nil {
			f.Choices.Accounts = append(f.Choices.Accounts, id)
		}
	}

	return f
}

// problemOf is the field a page words for an error with what was typed,
// or "".
func problemOf(err error) string {
	if invalid, ok := errors.AsType[reportbus.Invalid](err); ok {
		return invalid.Field
	}

	return ""
}

// save is "Save this report", from a style's run.
func (a app) save(kind types.ScopeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, ok := actor(w, r)
		if !ok || !parsed(w, r) {
			return
		}

		scope, ok := a.scope(w, r, kind)
		if !ok {
			return
		}

		f := fields(r)

		s, err := a.cfg.Reports.Save(r.Context(), a.cfg.Now(), me.ID, scope, r.PostForm.Get("style"), f)
		if code := problemOf(err); code != "" && code != "style" {
			// The run the form was sent from, again, with what was typed.
			r.URL.RawQuery = r.PostForm.Get("query")
			r.SetPathValue("style", r.PostForm.Get("style"))

			if v, ok := a.report(w, r, kind); ok {
				v.Problem, v.Typed = code, f
				a.show(w, r, http.StatusUnprocessableEntity, v)
			}

			return
		}

		if code := problemOf(err); code == "style" {
			err = reportbus.ErrNotFound
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}

		http.Redirect(w, r, "/reports/"+s.ID.String()+"?done=saved", http.StatusSeeOther)
	}
}

// savedID is the saved report a path names.
func (a app) savedID(w http.ResponseWriter, r *http.Request) (types.ID, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		a.failed(w, r, reportbus.ErrNotFound)

		return types.ID{}, false
	}

	return id, true
}

// saved is the saved report a path names, run for the period its address
// asks for. Its choices are its own, never the address's: a saved report's
// link is the report its keeper saved.
func (a app) saved(w http.ResponseWriter, r *http.Request) (reportView, bool) {
	me, ok := actor(w, r)
	if !ok {
		return reportView{}, false
	}

	id, ok := a.savedID(w, r)
	if !ok {
		return reportView{}, false
	}

	ask := askedOf(r)
	now := a.cfg.Now()

	run, err := a.cfg.Reports.Run(r.Context(), now, me.ID, id, ask.period)
	if err != nil {
		a.failed(w, r, err)

		return reportView{}, false
	}

	v := reportView{
		Report: run.Report, Path: scopePath(run.Saved.Scope), Base: "/reports/" + id.String(),
		Today: types.DateOf(now), Saved: &run, CanKeep: run.Report.Access.Can(tenancybus.Bookkeep),
		Done: r.URL.Query().Get("done"), choices: url.Values{},
	}

	if !run.Known {
		a.cfg.Render.Render(w, r, http.StatusOK, "report-gone", v)

		return reportView{}, false
	}

	v.Style, _ = reportbus.StyleOf(run.Saved.Style)

	if ask.bad {
		v.Problem = "period"
	}

	if run.HasNote {
		if u, err := a.cfg.Users.ByID(r.Context(), run.Note.By); err == nil {
			v.NoteBy = u.Named()
		}
	}

	return v, true
}

func (a app) runSaved(w http.ResponseWriter, r *http.Request) {
	if v, ok := a.saved(w, r); ok {
		a.show(w, r, http.StatusOK, v)
	}
}

// --- changing one --------------------------------------------------------------------

type editView struct {
	Report  reportView
	Typed   reportbus.Fields
	Problem string
}

// Chosen reports whether an account is ticked in the form.
func (v editView) Chosen(id types.ID) bool {
	if len(v.Typed.Choices.Accounts) == 0 {
		return true
	}

	for _, a := range v.Typed.Choices.Accounts {
		if a == id {
			return true
		}
	}

	return false
}

func (a app) edit(w http.ResponseWriter, r *http.Request) {
	a.editPage(w, r, http.StatusOK, nil, "")
}

// editPage is the form that changes a saved report, as it is or as typed.
func (a app) editPage(w http.ResponseWriter, r *http.Request, status int, typed *reportbus.Fields, problem string) {
	r.URL.RawQuery = ""

	v, ok := a.saved(w, r)
	if !ok {
		return
	}

	if !v.CanKeep {
		a.cfg.Render.Render(w, r, http.StatusForbidden, "report-missing", "refused")

		return
	}

	s := v.Saved.Saved
	f := reportbus.Fields{Title: s.Title, Description: s.Description, Period: s.Period, Choices: s.IncomeExpensesChoices()}

	if typed != nil {
		f = *typed
	}

	a.cfg.Render.Render(w, r, status, "report-edit", editView{Report: v, Typed: f, Problem: problem})
}

// keep runs a change to a saved report and answers it: back to the report
// saying what was done, the form again with what was wrong, or the reason
// it could not be done.
func (a app) keep(w http.ResponseWriter, r *http.Request, err error, again func(code string), to string) {
	if code := problemOf(err); code != "" {
		again(code)

		return
	}

	if errors.Is(err, reportbus.ErrForbidden) {
		a.cfg.Render.Render(w, r, http.StatusForbidden, "report-missing", "refused")

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (a app) change(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.savedID(w, r)
	if !ok {
		return
	}

	f := fields(r)

	_, err := a.cfg.Reports.Change(r.Context(), a.cfg.Now(), me.ID, id, f)
	a.keep(w, r, err, func(code string) { a.editPage(w, r, http.StatusUnprocessableEntity, &f, code) }, "/reports/"+id.String()+"?done=changed")
}

func (a app) remove(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.savedID(w, r)
	if !ok {
		return
	}

	// Its list is where a person goes next, and the report is gone by then.
	s, _, err := a.cfg.Reports.Saved(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	err = a.cfg.Reports.Remove(r.Context(), a.cfg.Now(), me.ID, id)
	a.keep(w, r, err, func(string) { a.failed(w, r, err) }, scopePath(s.Scope)+"/reports?done=removed")
}

// note writes the note for the period the form was sent from.
func (a app) note(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !parsed(w, r) {
		return
	}

	id, ok := a.savedID(w, r)
	if !ok {
		return
	}

	var p reportbus.Period

	switch {
	case r.PostForm.Get("period") == "all":
		p = reportbus.Period{All: true}
	default:
		var err error
		if p, err = reportbus.NewPeriod(r.PostForm.Get("from"), r.PostForm.Get("to")); err != nil {
			a.failed(w, r, reportbus.ErrNotFound)

			return
		}
	}

	text := r.PostForm.Get("note")
	back := url.Values{}
	back = periodValues(back, p)

	done := "noted"
	if text == "" {
		done = "note-removed"
	}

	back.Set("done", done)

	err := a.cfg.Reports.SetNote(r.Context(), a.cfg.Now(), me.ID, id, p, text)
	a.keep(w, r, err, func(code string) {
		r.URL.RawQuery = periodValues(url.Values{}, p).Encode()

		if v, ok := a.saved(w, r); ok {
			v.Problem = code
			a.show(w, r, http.StatusUnprocessableEntity, v)
		}
	}, "/reports/"+id.String()+"?"+back.Encode()+"#note")
}
