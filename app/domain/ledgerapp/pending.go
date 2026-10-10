package ledgerapp

import (
	"errors"
	"net/http"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
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
