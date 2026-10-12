package ledgerapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/jobs"
)

// Many statements at once (ledgerbus, bulk.go): a page to send them on,
// and the list they come back as, each file -- or each account's part of
// one -- with the account proposed for it, what reading it for that
// account found, and whether it can be imported with the others or needs
// its own preview.
//
// The statements a script sent to the person's inbox (ledgerbus, inbox.go;
// docs/books-api.md) are shown on the page before any files are chosen, as
// a link to this same list with them on it, so that they are checked and
// imported exactly as uploaded files are; and each may be put aside.
//
// The list keeps no state of its own. Which files are on it, and which
// accounts a person chose, are in its address (f, and to-<key>), so that
// it can be checked again after a choice, bookmarked, or come back to
// after a preview; the files are the person's own uploads, and anybody
// else's are left off it (ledgerbus.Propose).

// BulkPatterns are the routes that take many files at once. The muxer
// gives them a branch of their own: multipart only, up to MaxBulkUpload,
// and BulkTime to arrive.
var BulkPatterns = []string{"POST /imports"}

const (
	// MaxBulkUpload is the most a bulk import's body may be: room for a
	// batch of statements, most of them far smaller than the largest
	// one may be.
	MaxBulkUpload = 100 << 20

	// BulkTime is how long a batch of files may take to arrive.
	BulkTime = 5 * time.Minute

	// bulkWait is how long a request waits on the list's reading or
	// importing before it answers with a page that asks again: well
	// inside the fifteen seconds the proxy in front of the server waits,
	// with room for the rest of the page.
	bulkWait = 8 * time.Second

	// bulkAgain is how soon a waiting page asks again, in the Refresh
	// header's seconds.
	bulkAgain = "2"
)

// Jobs is the part of jobs.Runner this app uses.
//
// Reading every file of a list is a second or so a file the first time,
// and forty files took longer than the proxy waits, which answered "Bad
// Gateway" and cancelled the request, stopping an import wherever it had
// got to (issue #107). So a list's reading and its importing are jobs: the
// request starts one, waits bulkWait, and shows the list if it is done --
// a small batch looks no different -- or a page that says how far it has
// got and refreshes itself until it is.
type Jobs interface {
	Start(owner, key string, work jobs.Work) (string, error)
	Wait(ctx context.Context, owner, id string, d time.Duration) (jobs.Status, bool)
}

// bulkResult is what a list's job came to: its rows as they were last
// read, and, when it imported, how many.
type bulkResult struct {
	rows     []ledgerbus.Proposal
	imports  bool
	imported int
}

type bulkView struct {
	// Accounts is those the person keeps the books of, to choose from.
	Accounts []tenancybus.Account

	Rows []ledgerbus.Proposal

	// Files is the list's files, for its form to send back, and Ready how
	// many rows can be imported together.
	Files []types.ID
	Ready int

	// Imported is how many were, when the list is shown after importing.
	Imported int
	Done     bool

	// Working is set while the list's files are read or imported in the
	// background, and Progress is how far that has got. Lost is a job the
	// address named that is no longer there -- the site restarted -- so
	// the list was read again.
	Working  bool
	Progress jobs.Progress
	Lost     bool

	// Inbox is the files waiting in the person's inbox, on the page
	// before files are chosen, and InboxList the list with them on it.
	// Dismissed is a file just put aside.
	Inbox     []ledgerbus.InboxFile
	InboxList string
	Dismissed bool

	Problem string
}

// Max is the most files a list takes.
func (bulkView) Max() int { return ledgerbus.MaxBatch }

// ProblemOf is what to say of a row's file that could not be read.
func (bulkView) ProblemOf(err error) string { return problem(err) }

// bulk is the page to send files on, or with files in its address, their
// list.
func (a app) bulk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	v := bulkView{Problem: q.Get("problem"), Dismissed: q.Get("done") == "dismissed"}

	a.bulkPage(w, r, http.StatusOK, q, v)
}

func (a app) bulkPage(w http.ResponseWriter, r *http.Request, status int, q url.Values, v bulkView) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	files, chosen := bulkChoices(q)

	accounts, err := a.cfg.Ledger.Bookkept(r.Context(), me.ID)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	v.Accounts = accounts

	if len(files) == 0 {
		inbox, err := a.cfg.Ledger.Inbox(r.Context(), me.ID)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		v.Inbox = inbox

		var waiting []types.ID
		for _, f := range inbox[:min(len(inbox), ledgerbus.MaxBatch)] {
			waiting = append(waiting, f.FileID)
		}

		if len(waiting) > 0 {
			v.InboxList = bulkAddress(waiting, nil, nil)
		}
	}

	if len(files) > 0 {
		st, err := a.bulkStatus(r, me.ID, q.Get("job"), files, chosen, &v)

		switch {
		case errors.Is(err, jobs.ErrBusy):
			v.Problem = "busy"
		case err != nil:
			a.failed(w, r, err)

			return
		case !st.Finished:
			v.Working, v.Progress = true, st.Progress

			// The header rather than a meta element or a script: the page
			// needs nothing in its head, and works with scripts off.
			w.Header().Set("Refresh", bulkAgain+"; url="+bulkAddress(files, chosen, url.Values{"job": {st.ID}}))
		default:
			res, _ := st.Result.(bulkResult)
			v.Rows, v.Done, v.Imported = res.rows, res.imports, res.imported
		}

		seen := map[types.ID]bool{}

		for _, p := range v.Rows {
			if !seen[p.File.ID] {
				seen[p.File.ID] = true
				v.Files = append(v.Files, p.File.ID)
			}

			if p.Unattended() {
				v.Ready++
			}
		}
	}

	a.cfg.Render.Render(w, r, status, "imports", v)
}

// bulkStatus is the job the list's address names, waited on for bulkWait;
// or, when it names none, a reading of the list's files started now. A job
// the person has none by -- the site has restarted since, or it finished
// long ago -- is started again as a reading, and the page says so: what was
// imported before shows as imported, since the list is read as it stands.
//
// A job that failed is its error, as a request's would have been.
func (a app) bulkStatus(r *http.Request, me types.ID, id string, files []types.ID, chosen map[string]types.ID, v *bulkView) (jobs.Status, error) {
	owner := me.String()

	if id != "" {
		st, ok := a.cfg.Jobs.Wait(r.Context(), owner, id, a.cfg.JobWait)
		if ok && st.Err == nil {
			return st, nil
		}

		if ok {
			return st, st.Err
		}

		v.Lost = true
	}

	id, err := a.cfg.Jobs.Start(owner, "read "+bulkAddress(files, chosen, nil), func(ctx context.Context, report func(jobs.Progress)) (any, error) {
		rows, err := a.read(ctx, report, me, files, chosen)

		return bulkResult{rows: rows}, err
	})
	if err != nil {
		return jobs.Status{}, err
	}

	st, _ := a.cfg.Jobs.Wait(r.Context(), owner, id, a.cfg.JobWait)

	return st, st.Err
}

// read is the list's files read for its rows, saying how many so far.
func (a app) read(ctx context.Context, report func(jobs.Progress), me types.ID, files []types.ID, chosen map[string]types.ID) ([]ledgerbus.Proposal, error) {
	return a.cfg.Ledger.ProposeSaying(ctx, me, files, chosen, func(done, of int) {
		report(jobs.Progress{Stage: "reading", Done: done, Of: of})
	})
}

// bulkChoices reads the list's files and the accounts chosen for them from
// its address or its form. What is not an identifier is not one of them.
//
// A choice that is the account the page proposed by itself (was-<key>, by
// the number the file prints or where a file like it went) is left out:
// the list proposes it again from the files alone. Only a person's own
// choices travel, so the address stays short. Carrying every one, a batch
// of 36 statements of five accounts each made an address of 15 KB, and
// Apache refuses one over 8 KB with "Request-URI Too Long" before the app
// sees it (2026-10-12).
func bulkChoices(q url.Values) ([]types.ID, map[string]types.ID) {
	var files []types.ID

	for _, s := range q["f"] {
		if id, err := types.ParseID(s); err == nil && len(files) < ledgerbus.MaxBatch {
			files = append(files, id)
		}
	}

	chosen := map[string]types.ID{}

	for k, vs := range q {
		key, ok := strings.CutPrefix(k, "to-")
		if !ok || len(vs) == 0 {
			continue
		}

		if vs[0] == q.Get("was-"+key) {
			continue
		}

		if id, err := types.ParseID(vs[0]); err == nil {
			chosen[key] = id
		}
	}

	return files, chosen
}

// bulkAddress is the list's address for these files and choices.
func bulkAddress(files []types.ID, chosen map[string]types.ID, extra url.Values) string {
	q := url.Values{}

	for _, f := range files {
		q.Add("f", f.String())
	}

	for k, id := range chosen {
		q.Set("to-"+k, id.String())
	}

	for k, vs := range extra {
		q[k] = vs
	}

	return "/imports?" + q.Encode()
}

// bulkUpload keeps every file sent and shows them as a list. A file over
// the size a statement may be, or past the batch's number, is left off it
// and said.
func (a app) bulkUpload(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		a.bulkPage(w, r, http.StatusUnprocessableEntity, nil, bulkView{Problem: "no-file"})

		return
	}

	var (
		files   []types.ID
		trouble string
	)

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			if problem(tooBig(err)) == "too-big" {
				trouble = "too-many"

				break
			}

			a.failed(w, r, err)

			return
		}

		if part.FormName() != "files" || part.FileName() == "" {
			part.Close()

			continue
		}

		if len(files) == ledgerbus.MaxBatch {
			part.Close()

			trouble = "too-many"

			continue
		}

		f, err := a.cfg.Files.Save(r.Context(), a.cfg.Now(), me.ID, part.FileName(), part, ledgerbus.MaxFile, nil)
		part.Close()

		switch {
		case errors.Is(tooBig(err), filebus.ErrTooBig):
			trouble = "too-big-some"
		case err != nil:
			a.failed(w, r, err)

			return
		case f.Size > 0:
			files = append(files, f.ID)
		}
	}

	if len(files) == 0 {
		if trouble == "" {
			trouble = "no-file"
		}

		a.bulkPage(w, r, http.StatusUnprocessableEntity, nil, bulkView{Problem: trouble})

		return
	}

	extra := url.Values{}
	if trouble != "" {
		extra.Set("problem", trouble)
	}

	http.Redirect(w, r, bulkAddress(files, nil, extra), http.StatusSeeOther)
}

// bulkImport imports every row of the list that needs nobody, and shows
// the list again, where they now say they are imported.
func (a app) bulkImport(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)

		return
	}

	files, chosen := bulkChoices(r.PostForm)
	owner := me.ID.String()

	id, err := a.cfg.Jobs.Start(owner, "import "+bulkAddress(files, chosen, nil), func(ctx context.Context, report func(jobs.Progress)) (any, error) {
		rows, err := a.read(ctx, report, me.ID, files, chosen)
		if err != nil {
			return nil, err
		}

		report(jobs.Progress{Stage: "importing"})

		n, err := a.cfg.Ledger.ImportProposals(ctx, a.cfg.Now(), me.ID, rows)
		if err != nil {
			return nil, err
		}

		// Read again for the list to show, now that some are in: a row
		// imported says so, with its statement, only when it is read
		// after. The files' readings are remembered (ledgerbus,
		// readings.go), so this is the quick part.
		rows, err = a.read(ctx, report, me.ID, files, chosen)

		return bulkResult{rows: rows, imports: true, imported: n}, err
	})

	switch {
	case errors.Is(err, jobs.ErrBusy):
		http.Redirect(w, r, bulkAddress(files, chosen, url.Values{"problem": {"busy"}}), http.StatusSeeOther)

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	// Waited on here too, so that a small batch lands on its list, imported,
	// without a page in between. The list it lands on is the job's.
	_, _ = a.cfg.Jobs.Wait(r.Context(), owner, id, a.cfg.JobWait)

	http.Redirect(w, r, bulkAddress(files, chosen, url.Values{"job": {id}}), http.StatusSeeOther)
}

// bulkCheck is the list's "Check again": the files and the person's own
// choices, back as the list's address. A post rather than the form's own
// GET, which would put every account on the list into the address,
// proposed or chosen, and outgrow what Apache accepts (bulkChoices).
func (a app) bulkCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := actor(w, r); !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)

		return
	}

	files, chosen := bulkChoices(r.PostForm)

	http.Redirect(w, r, bulkAddress(files, chosen, nil), http.StatusSeeOther)
}

// dismiss puts a file of the person's inbox aside without importing it.
// One that is not in their inbox -- somebody else's, or put aside already
// -- is a 404, as anything that is not theirs is.
func (a app) dismiss(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	switch err := a.cfg.Ledger.Dismiss(r.Context(), a.cfg.Now(), me.ID, id); {
	case errors.Is(err, ledgerbus.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		a.failed(w, r, err)
	default:
		http.Redirect(w, r, "/imports?done=dismissed", http.StatusSeeOther)
	}
}
