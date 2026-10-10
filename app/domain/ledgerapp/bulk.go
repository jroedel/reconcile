package ledgerapp

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Many statements at once (ledgerbus, bulk.go): a page to send them on,
// and the list they come back as, each file -- or each account's part of
// one -- with the account proposed for it, what reading it for that
// account found, and whether it can be imported with the others or needs
// its own preview.
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

// SlowPatterns are the routes that read every file of a bulk import, which
// takes longer than a page is given: a second or so a file. The muxer
// gives them BulkTime.
var SlowPatterns = []string{"GET /imports", "POST /imports/import"}

const (
	// MaxBulkUpload is the most a bulk import's body may be: room for a
	// batch of statements, most of them far smaller than the largest
	// one may be.
	MaxBulkUpload = 100 << 20

	// BulkTime is how long a bulk import's pages may take.
	BulkTime = 5 * time.Minute
)

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

	v := bulkView{Problem: q.Get("problem")}
	if q.Has("imported") {
		v.Imported, _ = strconv.Atoi(q.Get("imported"))
		v.Done = true
	}

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

	if len(files) > 0 {
		props, err := a.cfg.Ledger.Propose(r.Context(), me.ID, files, chosen)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		v.Rows = props

		seen := map[types.ID]bool{}

		for _, p := range props {
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

// bulkChoices reads the list's files and the accounts chosen for them from
// its address or its form. What is not an identifier is not one of them.
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

	props, err := a.cfg.Ledger.Propose(r.Context(), me.ID, files, chosen)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	n, err := a.cfg.Ledger.ImportProposals(r.Context(), a.cfg.Now(), me.ID, props)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, bulkAddress(files, chosen, url.Values{"imported": {strconv.Itoa(n)}}), http.StatusSeeOther)
}
