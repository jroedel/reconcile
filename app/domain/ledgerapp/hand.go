package ledgerapp

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// enter is the transactions page's "Enter a transaction by hand"
// (docs/clearing.md, 1): the date, description and amount, which way the
// money went, and the document, in one multipart form. Asked of the
// account before a byte is stored, as a statement's upload is.
func (a app) enter(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	_, access, err := a.cfg.Tenancy.Account(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	if !access.Can(tenancybus.Bookkeep) {
		a.failed(w, r, ledgerbus.ErrForbidden)

		return
	}

	e, _, code, err := a.entryFrom(r, me.ID)

	switch {
	case code != "":
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, code)

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	t, err := a.cfg.Ledger.Enter(r.Context(), a.cfg.Now(), me.ID, id, e)

	if code := enterProblem(err); code != "" {
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, code)

		return
	}

	if err != nil {
		a.failed(w, r, err)

		return
	}

	back(w, r, "/transactions/"+t.ID.String(), "entered")
}

// entryFrom reads the entry's form: the entry, the form's other fields,
// and the code of what was wrong with it that a page words, or an error
// that is not the form's.
func (a app) entryFrom(r *http.Request, me types.ID) (ledgerbus.Entry, map[string]string, string, error) {
	fields, f, err := a.receiveEntry(r, me)

	switch {
	case errors.Is(err, filebus.ErrType):
		return ledgerbus.Entry{}, fields, "entry-file-type", nil
	case errors.Is(err, errNoFile):
		return ledgerbus.Entry{}, fields, "entry-file", nil
	case problem(err) != "":
		return ledgerbus.Entry{}, fields, problem(err), nil
	case err != nil:
		return ledgerbus.Entry{}, fields, "", err
	}

	e := ledgerbus.Entry{Description: fields["description"], File: f.ID}

	e.Date, _ = types.ParseDate(strings.TrimSpace(fields["date"]))

	amount, err := typedAmount(strings.TrimSpace(fields["amount"]))
	if err != nil || amount < 0 {
		return ledgerbus.Entry{}, fields, "entry-amount", nil
	}

	// Typed as a number and a direction, because a person writes "40.00
	// out", not "-40.00".
	if fields["direction"] != "in" {
		amount = -amount
	}

	e.Amount = amount

	return e, fields, "", nil
}

// enterProblem is the code a page words for what Enter refused, or "".
func enterProblem(err error) string {
	if invalid, ok := errors.AsType[ledgerbus.EntryInvalid](err); ok {
		return "entry-" + invalid.Field
	}

	switch {
	case errors.Is(err, ledgerbus.ErrEntered):
		return "entry-already"
	case errors.Is(err, ledgerbus.ErrLocked):
		return "entry-locked"
	}

	return ""
}

// maxField is the longest a typed field of the entry may be.
const maxField = 1 << 10

// receiveEntry reads the entry's form part by part: its typed fields, and
// its document, saved as a receipt is, of a receipt's kinds.
func (a app) receiveEntry(r *http.Request, me types.ID) (map[string]string, filebus.File, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, filebus.File{}, errNoFile
	}

	fields := map[string]string{}

	var f filebus.File

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, filebus.File{}, tooBig(err)
		}

		switch name := part.FormName(); {
		case name == "document" && part.FileName() != "":
			f, err = a.cfg.Files.Save(r.Context(), a.cfg.Now(), me, part.FileName(), part, ledgerbus.MaxFile, ledgerbus.HandAccept)
			part.Close()

			if err != nil {
				return nil, filebus.File{}, tooBig(err)
			}
		case name == "date" || name == "description" || name == "amount" || name == "direction" || name == "account":
			b, err := io.ReadAll(io.LimitReader(part, maxField))
			part.Close()

			if err != nil {
				return nil, filebus.File{}, tooBig(err)
			}

			fields[name] = string(b)
		default:
			part.Close()
		}
	}

	if f.ID.Zero() || f.Size == 0 {
		return fields, filebus.File{}, errNoFile
	}

	return fields, f, nil
}
