package ledgerapp

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// --- a statement, and reconciling it ------------------------------------------

type statementView struct {
	Review      ledgerbus.Review
	ImportedBy  string
	CanBookkeep bool
	Done        string

	// Export is the accountant's package for the statement's period, for
	// a reader who may download it.
	Export string

	// ReconciledBy names who reconciled it, when somebody has.
	ReconciledBy string

	// Waiting is how many receipts the reader can see that wait for a
	// match: after an import is when some of them can be matched.
	Waiting int

	// Receipts is those of them that may belong to this statement: put
	// into this account's inbox and dated in its period, or for the same
	// amount as one of its transactions, close to its date. Not required
	// -- receipts never hold up a month -- but the moment of reconciling
	// is when somebody looks.
	Receipts []receiptbus.Receipt

	Form statementForm
}

// statementForm is what was typed into the statement's forms, kept when
// the page is shown again with a problem.
type statementForm struct {
	From, To, Note, Reason string
	Problem                string
}

func (a app) statement(w http.ResponseWriter, r *http.Request) {
	a.statementPage(w, r, http.StatusOK, statementForm{})
}

func (a app) statementPage(w http.ResponseWriter, r *http.Request, status int, form statementForm) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()

	rv, err := a.cfg.Ledger.Review(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	st := rv.Statement

	view := statementView{
		Review:      rv,
		CanBookkeep: rv.Access.Can(tenancybus.Bookkeep),
		Done:        r.URL.Query().Get("done"),
		Form:        form,
	}

	if view.Form.From == "" && view.Form.To == "" {
		start, end := st.Period()
		view.Form.From, view.Form.To = start.String(), end.String()
	}

	if rv.Access.Can(tenancybus.Export) {
		start, end := st.Period()
		view.Export = "/accounts/" + st.AccountID.String() + "/export?" +
			url.Values{"from": {start.String()}, "to": {end.String()}}.Encode()
	}

	if u, err := a.cfg.Users.ByID(ctx, st.ImportedBy); err == nil {
		view.ImportedBy = u.Named()
	}

	if st.Reconciled() {
		if u, err := a.cfg.Users.ByID(ctx, st.Reconciliation.By); err == nil {
			view.ReconciledBy = u.Named()
		}
	}

	if err := a.belonging(r, me.ID, &view); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, status, "statement", view)
}

// belonging finds the waiting receipts that may be this statement's.
func (a app) belonging(r *http.Request, me types.ID, view *statementView) error {
	ctx := r.Context()
	st := view.Review.Statement
	start, end := st.Period()
	home := types.AccountScope(st.AccountID)

	waiting, err := a.cfg.Receipts.Waiting(ctx, me)
	if err != nil {
		return err
	}

	view.Waiting = len(waiting)

	suggested, err := a.cfg.Receipts.Suggestions(ctx, me, waiting)
	if err != nil {
		return err
	}

	in := func(d types.Date) bool {
		return !d.Zero() && !d.Before(start.AddDays(-receiptbus.MatchDays)) && !end.AddDays(receiptbus.MatchDays).Before(d)
	}

	for _, rc := range waiting {
		mine := rc.Home == home && (rc.SpentOn.Zero() || in(rc.SpentOn))

		for _, t := range suggested[rc.ID] {
			mine = mine || t.AccountID == st.AccountID && in(t.PostedOn)
		}

		if mine {
			view.Receipts = append(view.Receipts, rc)
		}
	}

	return nil
}

func (a app) reconcile(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return
	}

	form := statementForm{From: r.PostForm.Get("from"), To: r.PostForm.Get("to"), Note: r.PostForm.Get("note")}

	from, errFrom := types.ParseDate(form.From)
	to, errTo := types.ParseDate(form.To)

	if errFrom != nil || errTo != nil {
		form.Problem = "period"
		a.statementPage(w, r, http.StatusUnprocessableEntity, form)

		return
	}

	_, err := a.cfg.Ledger.Reconcile(r.Context(), a.cfg.Now(), me.ID, id, from, to, form.Note)

	switch {
	case errors.Is(err, ledgerbus.ErrPeriod):
		form.Problem = "period"
	case errors.Is(err, ledgerbus.ErrNote):
		form.Problem = "note"
	case errors.Is(err, ledgerbus.ErrReconciled):
		form.Problem = "already-reconciled"
	case err != nil:
		a.failed(w, r, err)

		return
	default:
		back(w, r, "/statements/"+id.String(), "reconciled")

		return
	}

	status := http.StatusUnprocessableEntity
	if form.Problem == "already-reconciled" {
		status = http.StatusConflict
	}

	a.statementPage(w, r, status, form)
}

func (a app) reopen(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return
	}

	form := statementForm{Reason: r.PostForm.Get("reason")}

	_, err := a.cfg.Ledger.Reopen(r.Context(), a.cfg.Now(), me.ID, id, form.Reason)

	switch {
	case errors.Is(err, ledgerbus.ErrReason):
		form.Problem = "reason"
		a.statementPage(w, r, http.StatusUnprocessableEntity, form)
	case errors.Is(err, ledgerbus.ErrNotReconciled):
		// Somebody reopened it first; the page says how it stands.
		back(w, r, "/statements/"+id.String(), "")
	case err != nil:
		a.failed(w, r, err)
	default:
		back(w, r, "/statements/"+id.String(), "reopened")
	}
}

// --- month by month -----------------------------------------------------------

type monthsView struct {
	Account     tenancybus.Account
	Path        string
	CanBookkeep bool
	CanExport   bool
	Months      []ledgerbus.MonthCover

	// LastMonth is the month before this one, "2026-07": what the download
	// form offers first, because the end of a month is when it is wanted.
	LastMonth string
}

// months is an account's months, each with how it stands: the page a
// treasurer opens at the end of a month to see what is left to do.
func (a app) months(w http.ResponseWriter, r *http.Request) {
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

	view := monthsView{
		Account:     account,
		Path:        "/accounts/" + id.String(),
		CanBookkeep: access.Can(tenancybus.Bookkeep),
		CanExport:   access.Can(tenancybus.Export),
		LastMonth:   a.cfg.Now().AddDate(0, 0, -a.cfg.Now().Day()).Format("2006-01"),
	}

	// Today where the server is. A month's edge is a day either way for
	// someone far from it, and the month still going is shown as such
	// rather than as missing, so a day early or late costs nothing.
	if view.Months, err = a.cfg.Ledger.Coverage(ctx, me.ID, id, types.DateOf(a.cfg.Now())); err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "account-months", view)
}
