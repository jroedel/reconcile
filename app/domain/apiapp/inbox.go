package apiapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// The statement inbox (docs/books-api.md, "The inbox"): where a script in
// somebody's Google account sends the statements their email brings, with
// a key that may do nothing else. What arrives waits on /imports for its
// person, and later for their Claude.

// InboxPatterns are the routes whose body is a file rather than JSON. The
// muxer gives them a branch of their own: as big as a statement may be,
// any content type, and an upload's time to arrive.
var InboxPatterns = []string{"POST " + Prefix + "/inbox"}

// MaxInbox is the most an inbox request's body may be: a statement, and
// nothing else rides with it.
const MaxInbox = ledgerbus.MaxFile + 64<<10

// Inbox is the slice of ledgerbus the inbox needs.
type Inbox interface {
	Receive(ctx context.Context, now time.Time, actor types.ID, name, source string, r io.Reader) (ledgerbus.Received, error)
}

func (a app) inboxEndpoints() []Endpoint {
	return []Endpoint{{
		Method: http.MethodPost, Path: Prefix + "/inbox", Scope: userbus.Upload,
		Summary: "Send a statement file to your inbox, where it waits for you to import it at " + a.base + "/imports. " +
			"The body is the file itself -- a PDF, CSV, OFX or QFX, at most 10 MB -- not JSON, sent with its own Content-Type. " +
			"The same file sent again is recognised by its content and adds nothing.",
		Query: []Field{
			{Name: "name", Type: "string", Required: true, Description: "The file's name, as the email had it."},
			{Name: "source", Type: "string", Description: fmt.Sprintf("A note for your own reference, at most %d characters, such as gmail:<message id>.", ledgerbus.MaxSource)},
		},
		Body:    &Body{Encoding: "file", Fields: []Field{}},
		Returns: `{"outcome": "received" | "already_waiting" | "already_imported"}: received (201) when the file is new to the inbox; already_waiting when it is there, or was put aside; already_imported when it was imported.`,
		handler: a.receive,
	}}
}

func (a app) receive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	name := q.Get("name")
	if name == "" {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("name", "Give the file's name as ?name=, such as statement.pdf."))

		return
	}

	u, _ := mid.UserFrom(r.Context())

	got, err := a.inbox.Receive(r.Context(), time.Now(), u.ID, name, q.Get("source"), r.Body)

	_, overLimit := errors.AsType[*http.MaxBytesError](err)

	switch {
	case overLimit, errors.Is(err, filebus.ErrTooBig):
		web.WriteJSON(w, http.StatusRequestEntityTooLarge, web.Problem("", "That file is larger than a statement may be, 10 MB. Send it on its own, or upload it at /imports."))

		return
	case errors.Is(err, ledgerbus.ErrEmpty):
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", "The body is empty. Send the file itself as the body."))

		return
	case err != nil:
		a.fail(w, r, "receiving a statement in the inbox", err)

		return
	}

	status := http.StatusOK
	if got == ledgerbus.Arrived {
		status = http.StatusCreated
	}

	web.WriteJSON(w, status, map[string]string{"outcome": string(got)})
}
