package ledgerapp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// --- one transaction, and sorting it ----------------------------------------

// partRow is one row of the parts form, as typed: the amount as a person
// writes it, without its sign, which is the transaction's.
type partRow struct {
	Amount     string
	CategoryID string
	ProjectID  string
	Memo       string
}

type transactionView struct {
	Editor      ledgerbus.Editor
	CanBookkeep bool
	Back        string

	Rows []partRow

	// Problem is a code, and Part the row it is about, counting from one;
	// zero for the parts together.
	Problem string
	Part    int
}

// Out reports whether the transaction is money out, for the labels.
func (v transactionView) Out() bool { return v.Editor.Transaction.Amount < 0 }

// monthOf is the transaction list a transaction is listed on.
func monthOf(t ledgerbus.Transaction) string {
	return "/accounts/" + t.AccountID.String() + "/transactions?month=" + t.PostedOn.String()[:7]
}

// rowsOf is the form's rows for the parts as stored.
func rowsOf(t ledgerbus.Transaction) []partRow {
	rows := make([]partRow, len(t.Splits))

	for i, s := range t.Splits {
		rows[i] = partRow{Amount: s.Amount.Abs().String(), Memo: s.Memo}

		if !s.CategoryID.Zero() {
			rows[i].CategoryID = s.CategoryID.String()
		}

		if !s.ProjectID.Zero() {
			rows[i].ProjectID = s.ProjectID.String()
		}
	}

	return rows
}

func (a app) transaction(w http.ResponseWriter, r *http.Request) {
	a.transactionPage(w, r, http.StatusOK, nil, "", 0)
}

func (a app) transactionPage(w http.ResponseWriter, r *http.Request, status int, rows []partRow, problem string, part int) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	e, err := a.cfg.Ledger.Transaction(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	if rows == nil {
		rows = rowsOf(e.Transaction)
	}

	a.cfg.Render.Render(w, r, status, "transaction", transactionView{
		Editor:      e,
		CanBookkeep: e.Access.Can(tenancybus.Bookkeep),
		Back:        monthOf(e.Transaction),
		Rows:        rows,
		Problem:     problem,
		Part:        part,
	})
}

// sortTransaction is the parts form: save, or add a row and show it again.
func (a app) sortTransaction(w http.ResponseWriter, r *http.Request) {
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

	rows := postedRows(r)

	if r.PostForm.Get("action") == "add" {
		if len(rows) < ledgerbus.MaxParts {
			rows = append(rows, partRow{})
		}

		a.transactionPage(w, r, http.StatusOK, rows, "", 0)

		return
	}

	// The sign is the transaction's, so it is read before the parts are.
	e, err := a.cfg.Ledger.Transaction(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	parts, bad := partsOf(rows, e.Transaction)
	if bad > 0 {
		a.transactionPage(w, r, http.StatusUnprocessableEntity, rows, "amount", bad)

		return
	}

	t, err := a.cfg.Ledger.SetSplits(r.Context(), a.cfg.Now(), me.ID, id, parts)
	if invalid, ok := errors.AsType[ledgerbus.Invalid](err); ok {
		a.transactionPage(w, r, http.StatusUnprocessableEntity, rows, invalid.Field, invalid.Index+1)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, monthOf(t), "sorted")
}

// postedRows reads the form's rows: amount-0, category-0, and so on. A row
// left entirely empty is dropped, which is how a part is taken out.
func postedRows(r *http.Request) []partRow {
	var rows []partRow

	for i := range ledgerbus.MaxParts {
		n := strconv.Itoa(i)

		if _, present := r.PostForm["amount-"+n]; !present {
			break
		}

		row := partRow{
			Amount:     strings.TrimSpace(r.PostForm.Get("amount-" + n)),
			CategoryID: r.PostForm.Get("category-" + n),
			ProjectID:  r.PostForm.Get("project-" + n),
			Memo:       r.PostForm.Get("memo-" + n),
		}

		if row == (partRow{}) {
			continue
		}

		rows = append(rows, row)
	}

	return rows
}

// partsOf turns the rows into parts with the transaction's sign, or names
// the row, counting from one, whose amount is not one. A single row with
// no amount is the whole transaction: the common case is one part, sorted,
// and nobody should have to type the amount the bank already gave.
func partsOf(rows []partRow, t ledgerbus.Transaction) ([]ledgerbus.Part, int) {
	parts := make([]ledgerbus.Part, len(rows))

	for i, row := range rows {
		amount := t.Amount

		if row.Amount != "" || len(rows) > 1 {
			typed, err := typedAmount(row.Amount)
			if err != nil {
				return nil, i + 1
			}

			amount = typed.Abs()
			if t.Amount < 0 {
				amount = -amount
			}
		}

		// An ID that does not parse is no choice at all: the business
		// says so if it mattered.
		category, _ := types.ParseID(row.CategoryID)
		project, _ := types.ParseID(row.ProjectID)

		parts[i] = ledgerbus.Part{Amount: amount, CategoryID: category, ProjectID: project, Memo: row.Memo}
	}

	return parts, 0
}

// --- a project's book -------------------------------------------------------

type bookView struct {
	Book ledgerbus.Book
	Path string
}

func (a app) book(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	b, err := a.cfg.Ledger.ProjectBook(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "project-book", bookView{Book: b, Path: "/projects/" + id.String()})
}
