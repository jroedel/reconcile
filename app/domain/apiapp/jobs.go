package apiapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/jobs"
	"github.com/jroedel/reconcile/foundation/web"
)

// Jobs is the part of jobs.Runner this app uses.
//
// Reading every file in a person's inbox is a second or so a file the first
// time, and an inbox of forty took longer than the proxy in front of the
// server waits: it answered "Bad Gateway", and the import stopped wherever
// it had got to (issue #107). So list_inbox and import_from_inbox are jobs,
// as the bulk import's pages are (ledgerapp, bulk.go). A call waits apiWait
// and answers as it always did if the job is done by then, which a few
// files are; if not, it answers 202 with the job, and the same tool called
// again with it carries on waiting.
type Jobs interface {
	Start(owner, key string, work jobs.Work) (string, error)
	Wait(ctx context.Context, owner, id string, d time.Duration) (jobs.Status, bool)
}

// apiWait is how long a call waits on its job: inside the proxy's fifteen
// seconds, with room for the answer.
const apiWait = 10 * time.Second

// workingNote and workingReturns are what the two endpoints' descriptions
// say of a job, so that a program reading the index knows to ask again.
const (
	workingNote    = "A large inbox takes longer to read than one call may last: then the answer says it is still working, and the same call again with its job carries on."
	workingReturns = "202 {working: true, job, stage: reading|importing, done, of, note}: call again with job, after a few seconds."
)

// jobField is the field that names the job an earlier call answered with.
func jobField(tool string) Field {
	return Field{Name: "job", Type: "string", Description: fmt.Sprintf("The job an earlier %s answered with while it was still working, to ask after it; the other fields are then not read.", tool)}
}

// inboxResult is what an inbox job came to: the inbox as it was read, its
// files' proposals as they were last read, and for an import, which files
// were asked for and how many parts went in.
type inboxResult struct {
	inbox []ledgerbus.InboxFile
	props []ledgerbus.Proposal

	files    []types.ID
	names    map[types.ID]string
	imported int
}

// reading is report as ProposeSaying says it.
func reading(report func(jobs.Progress)) func(done, of int) {
	return func(done, of int) { report(jobs.Progress{Stage: "reading", Done: done, Of: of}) }
}

// job starts work as the person's, under key, or asks after the job id
// names, and waits apiWait. It reports true with what the work came to when
// it has finished; otherwise it has answered the call itself -- still
// working, a job no longer there, the line full, or the work's error, which
// is answered as the call's own would have been.
//
// A job is the person's and the tool's: one list_inbox started is not
// there for import_from_inbox, whose answer it could not be.
func (a app) job(w http.ResponseWriter, r *http.Request, me types.ID, tool, id, key string, work jobs.Work) (inboxResult, bool) {
	owner := me.String() + " " + tool

	if id == "" {
		var err error

		id, err = a.jobs.Start(owner, key, work)
		if errors.Is(err, jobs.ErrBusy) {
			web.WriteJSON(w, http.StatusServiceUnavailable, web.Problem("", "The site has too much reading waiting just now. Call "+tool+" again in a minute."))

			return inboxResult{}, false
		}

		if err != nil {
			a.fail(w, r, "starting a job", err)

			return inboxResult{}, false
		}
	}

	st, ok := a.jobs.Wait(r.Context(), owner, id, a.jobWait)

	switch {
	case !ok:
		web.WriteJSON(w, http.StatusNotFound, web.Problem("job", "That job is no longer here: the site has restarted since, or it finished more than half an hour ago. Call "+tool+" again without it; anything it imported is recognized as imported."))

		return inboxResult{}, false
	case !st.Finished:
		web.WriteJSON(w, http.StatusAccepted, map[string]any{
			"working": true, "job": st.ID, "stage": st.Progress.Stage, "done": st.Progress.Done, "of": st.Progress.Of,
			"note": "Still " + cmp.Or(st.Progress.Stage, "starting") + ". It carries on meanwhile: call " + tool + " again with this job in a few seconds.",
		})

		return inboxResult{}, false
	case a.bookRefused(w, r, st.Err, "file"):
		return inboxResult{}, false
	case st.Err != nil:
		a.fail(w, r, "reading the inbox", st.Err)

		return inboxResult{}, false
	}

	res, _ := st.Result.(inboxResult)

	return res, true
}
