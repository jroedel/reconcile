package ledgerapp

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
)

// setPayee writes whom a transaction was paid to, from its page: one of
// the list's payees, or a new name, which becomes a payee of its own
// (docs/payees.md, 1). Whoever may attach receipts may, as for a check's
// payee read off its image.
func (a app) setPayee(w http.ResponseWriter, r *http.Request) {
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

	// Back to the transaction, still carrying the way back to wherever it
	// was opened from.
	q := url.Values{}
	if ret := page.ReturnFrom(r); ret != nil {
		q.Set("back", ret.Path)
	}

	to := func(k, v string) string {
		q.Set(k, v)

		return "/transactions/" + id.String() + "?" + q.Encode() + "#paid-to"
	}

	_, err := a.cfg.Ledger.SetPayee(r.Context(), a.cfg.Now(), me.ID, id, r.PostForm.Get("payee"))

	switch {
	case errors.Is(err, ledgerbus.ErrPayee):
		http.Redirect(w, r, to("problem", "payee"), http.StatusSeeOther)
	case err != nil:
		a.failed(w, r, err)
	default:
		http.Redirect(w, r, to("done", "payee"), http.StatusSeeOther)
	}
}
