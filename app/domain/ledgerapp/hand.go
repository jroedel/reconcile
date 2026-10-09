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

	fields, f, err := a.receiveEntry(r, me.ID)

	switch {
	case errors.Is(err, filebus.ErrType):
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "entry-file-type")

		return
	case errors.Is(err, errNoFile):
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "entry-file")

		return
	case problem(err) != "":
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, problem(err))

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	e := ledgerbus.Entry{Description: fields["description"], File: f.ID}

	e.Date, _ = types.ParseDate(strings.TrimSpace(fields["date"]))

	amount, err := typedAmount(strings.TrimSpace(fields["amount"]))
	if err != nil || amount < 0 {
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "entry-amount")

		return
	}

	// Typed as a number and a direction, because a person writes "40.00
	// out", not "-40.00".
	if fields["direction"] != "in" {
		amount = -amount
	}

	e.Amount = amount

	t, err := a.cfg.Ledger.Enter(r.Context(), a.cfg.Now(), me.ID, id, e)

	invalid, isInvalid := errors.AsType[ledgerbus.EntryInvalid](err)

	switch {
	case isInvalid:
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "entry-"+invalid.Field)
	case errors.Is(err, ledgerbus.ErrEntered):
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "entry-already")
	case errors.Is(err, ledgerbus.ErrLocked):
		a.transactionsPage(w, r, http.StatusUnprocessableEntity, "entry-locked")
	case err != nil:
		a.failed(w, r, err)
	default:
		back(w, r, "/transactions/"+t.ID.String(), "entered")
	}
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
		case name == "date" || name == "description" || name == "amount" || name == "direction":
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
