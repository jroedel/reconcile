package ledgerapp

import (
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// --- explaining an amount ---------------------------------------------------

// explainView is "Explain this amount" (docs/clearing.md, 2): the
// explanation as it stands, and, for a bookkeeper, what the chosen accounts
// and months offer to add to it.
type explainView struct {
	X ledgerbus.Explanation

	// Chosen is the accounts and months the offer was gathered from, as
	// the form shows them; Offered is what they have that no explanation
	// has yet.
	Chosen   map[types.ID]bool
	From, To string
	Offered  []ledgerbus.Line
	Gathered bool

	// Groups is the lines by account, with each account's sum, so that a
	// missing holder or month shows as a missing group.
	Groups []lineGroup

	Note    string
	Problem string
	Done    string
}

type lineGroup struct {
	Account string
	Lines   []ledgerbus.Line
	Sum     money.Amount
}

// Back is the transaction's own page.
func (v explainView) Back() string { return "/transactions/" + v.X.Transaction.ID.String() }

// Offer is what the offered lines come to.
func (v explainView) Offer() (sum money.Amount) {
	for _, l := range v.Offered {
		sum += l.Transaction.Amount
	}

	return sum
}

func (a app) explain(w http.ResponseWriter, r *http.Request) {
	a.explainPage(w, r, http.StatusOK, "", "")
}

func (a app) explainPage(w http.ResponseWriter, r *http.Request, status int, code, note string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()

	x, err := a.cfg.Ledger.Explain(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	view := explainView{X: x, Problem: code, Note: x.Note, Done: r.URL.Query().Get("done"), Chosen: map[types.ID]bool{}}

	if code != "" {
		view.Note = note
	}

	for _, l := range x.Lines {
		if n := len(view.Groups); n == 0 || view.Groups[n-1].Account != l.Account.Name {
			view.Groups = append(view.Groups, lineGroup{Account: l.Account.Name})
		}

		g := &view.Groups[len(view.Groups)-1]
		g.Lines = append(g.Lines, l)
		g.Sum += l.Transaction.Amount
	}

	if !x.CanChange() {
		a.cfg.Render.Render(w, r, status, "explain", view)

		return
	}

	// The accounts and months asked for, or as last time, or as the last
	// transaction like this one was gathered: so that next month's
	// withdrawal is one press.
	q := r.URL.Query()

	accounts, from, to := parseIDs(q["account"]), q.Get("from"), q.Get("to")
	if len(accounts) == 0 && !q.Has("from") {
		accounts, from, to = x.Sources, x.From(), x.To()
	}

	view.From, view.To = from, to
	for _, id := range accounts {
		view.Chosen[id] = true
	}

	if len(accounts) > 0 {
		view.Gathered = true

		view.Offered, err = a.cfg.Ledger.Candidates(ctx, me.ID, id, accounts, from, to)

		switch {
		case errors.Is(err, ledgerbus.ErrNotFound):
			view.Problem, view.Gathered = "explain-months", false
		case errors.Is(err, ledgerbus.ErrElsewhere):
			view.Problem, view.Gathered = "explain-elsewhere", false
		case err != nil:
			a.failed(w, r, err)

			return
		}
	}

	a.cfg.Render.Render(w, r, status, "explain", view)
}

// parseIDs reads identifiers, skipping any that is not one.
func parseIDs(values []string) []types.ID {
	var out []types.ID

	for _, v := range values {
		if id, err := types.ParseID(v); err == nil && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}

	return out
}

// gather adds the ticked offered lines and takes out the ticked ones, and
// remembers where they were gathered from.
func (a app) gather(w http.ResponseWriter, r *http.Request) {
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

	f := r.PostForm
	accounts := parseIDs(f["account"])

	err := a.cfg.Ledger.Gather(r.Context(), a.cfg.Now(), me.ID, id, parseIDs(f["add"]), parseIDs(f["remove"]),
		accounts, f.Get("from"), f.Get("to"))

	switch {
	case errors.Is(err, ledgerbus.ErrExplained):
		a.explainPage(w, r, http.StatusConflict, "explain-taken", "")
	case errors.Is(err, ledgerbus.ErrElsewhere):
		a.explainPage(w, r, http.StatusUnprocessableEntity, "explain-elsewhere", "")
	case err != nil:
		a.failed(w, r, err)
	default:
		q := url.Values{"done": {"gathered"}, "from": {f.Get("from")}, "to": {f.Get("to")}}
		for _, id := range accounts {
			q.Add("account", id.String())
		}

		http.Redirect(w, r, "/transactions/"+id.String()+"/explain?"+q.Encode(), http.StatusSeeOther)
	}
}

// settle keeps the note, and whether the difference is accepted.
func (a app) settle(w http.ResponseWriter, r *http.Request) {
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

	note := r.PostForm.Get("note")

	err := a.cfg.Ledger.Settle(r.Context(), a.cfg.Now(), me.ID, id, note, r.PostForm.Get("accept") == "1")

	switch {
	case errors.Is(err, ledgerbus.ErrExplainNote):
		a.explainPage(w, r, http.StatusUnprocessableEntity, "explain-note", note)
	case err != nil:
		a.failed(w, r, err)
	default:
		back(w, r, "/transactions/"+id.String()+"/explain", "settled")
	}
}

// enterLine is the entry form on an explanation's page: a transaction
// entered by hand into one of the accounts the explanation may draw on,
// and added to it as a line. Asked of the explanation before a byte is
// stored; the entry's own account is asked by Enter, as it is from the
// account's page.
func (a app) enterLine(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()

	x, err := a.cfg.Ledger.Explain(ctx, me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	if !x.CanChange() {
		a.failed(w, r, ledgerbus.ErrForbidden)

		return
	}

	e, fields, code, err := a.entryFrom(r, me.ID)

	switch {
	case code != "":
		a.explainPage(w, r, http.StatusUnprocessableEntity, code, "")

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	account, err := types.ParseID(fields["account"])
	if err != nil || !slices.ContainsFunc(x.Choices, func(c tenancybus.Account) bool { return c.ID == account }) {
		a.explainPage(w, r, http.StatusUnprocessableEntity, "explain-elsewhere", "")

		return
	}

	t, err := a.cfg.Ledger.Enter(ctx, a.cfg.Now(), me.ID, account, e)

	if code := enterProblem(err); code != "" {
		a.explainPage(w, r, http.StatusUnprocessableEntity, code, "")

		return
	}

	if err == nil {
		err = a.cfg.Ledger.AddEntry(ctx, a.cfg.Now(), me.ID, id, t)
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/transactions/"+id.String()+"/explain", "entered-line")
}
