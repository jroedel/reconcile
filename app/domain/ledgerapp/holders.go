package ledgerapp

import (
	"net/http"
	"regexp"
	"strings"
)

// setByHolder turns "statements arrive one file per holder" on or off
// for an account (docs/clearing.md, 3). An owner's, because it changes what
// every stored row of the account is.
//
// From a file's preview that looked like one holder's (issue #81), it
// goes back to that preview, which reads the file again under the
// option: return names it, and only a preview of this account is
// returned to, so that the form is no way to send anybody elsewhere.
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

	to := "/accounts/" + id.String() + "/transactions"
	if ret := r.PostForm.Get("return"); previewOf.MatchString(ret) && strings.HasPrefix(ret, "/accounts/"+id.String()+"/imports/") {
		to = ret
	}

	back(w, r, to, done)
}

// previewOf is the path of a file's preview for an account.
var previewOf = regexp.MustCompile(`^/accounts/[0-9a-f]{32}/imports/[0-9a-f]{32}$`)
