package ledgerapp

import "net/http"

// setByHolder turns "statements arrive one file per holder" on or off
// for an account (docs/clearing.md, 3). An owner's, because it changes what
// every stored row of the account is.
func (a app) setByHolder(w http.ResponseWriter, r *http.Request) {
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

	on := r.PostForm.Get("on") == "1"

	if err := a.cfg.Ledger.SetByHolder(r.Context(), a.cfg.Now(), me.ID, id, on); err != nil {
		a.failed(w, r, err)

		return
	}

	done := "split-off"
	if on {
		done = "split-on"
	}

	back(w, r, "/accounts/"+id.String()+"/transactions", done)
}
