package ledgerapp

import (
	"errors"
	"net/http"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// release removes a pending charge that never posted, from the account's
// "still pending" list (docs/clearing.md, 4): a bookkeeper's word that the
// hold was released.
func (a app) release(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	// The account's page to go back to, read before the charge is gone.
	t, err := a.cfg.Ledger.Transaction(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	to := "/accounts/" + t.Transaction.AccountID.String() + "/transactions"

	err = a.cfg.Ledger.Release(r.Context(), a.cfg.Now(), me.ID, id)

	switch {
	case errors.Is(err, ledgerbus.ErrNotPending):
		http.Redirect(w, r, to+"?problem=not-pending", http.StatusSeeOther)
	case errors.Is(err, ledgerbus.ErrLocked):
		http.Redirect(w, r, to+"?problem=release-locked", http.StatusSeeOther)
	case err != nil:
		a.failed(w, r, err)
	default:
		back(w, r, to, "released")
	}
}

// pair says that a pending charge posted as another transaction of its
// account: from the account's pairs to confirm, or the pending charge's
// page (ledgerbus.ClearPending). Back to where it was said, the account's
// page when the form says so.
func (a app) pair(w http.ResponseWriter, r *http.Request) {
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

	posted, err := types.ParseID(r.PostForm.Get("posted"))
	if err != nil {
		http.Error(w, "That form could not be read. Open the page again and send it from there.", http.StatusBadRequest)

		return
	}

	t, err := a.cfg.Ledger.Transaction(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	account := "/accounts/" + t.Transaction.AccountID.String() + "/transactions"

	to := "/transactions/" + id.String()
	if r.PostForm.Get("from") == "account" {
		to = account
	}

	a.pending(w, r, a.cfg.Ledger.ClearPending(r.Context(), a.cfg.Now(), me.ID, id, posted), to, account, "paired")
}

// unclear undoes a pairing that was wrong (ledgerbus.Unclear): the
// pending charge counts again, and waits.
func (a app) unclear(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}

	t, err := a.cfg.Ledger.Transaction(r.Context(), me.ID, id)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	account := "/accounts/" + t.Transaction.AccountID.String() + "/transactions"

	a.pending(w, r, a.cfg.Ledger.Unclear(r.Context(), a.cfg.Now(), me.ID, id), "/transactions/"+id.String(), account, "unpaired")
}

// pending answers a change to a pending charge: done, back where it was
// made; or why not, on the account's page, which says it.
func (a app) pending(w http.ResponseWriter, r *http.Request, err error, to, account, done string) {
	switch {
	case errors.Is(err, ledgerbus.ErrNotPending):
		http.Redirect(w, r, account+"?problem=not-pending", http.StatusSeeOther)
	case errors.Is(err, ledgerbus.ErrNotPosted):
		http.Redirect(w, r, account+"?problem=not-posted", http.StatusSeeOther)
	case errors.Is(err, ledgerbus.ErrLocked):
		http.Redirect(w, r, account+"?problem=pair-locked", http.StatusSeeOther)
	case err != nil:
		a.failed(w, r, err)
	default:
		back(w, r, to, done)
	}
}
