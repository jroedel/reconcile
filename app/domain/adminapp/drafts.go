package adminapp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/shapes"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/types"
)

// The drafts of layout declarations (shapebus, drafts.go): the
// administrator writes one here, starting from a layout seen or from a
// built-in declaration to revise it, tries it on a PDF of their own beside
// the reading the site makes of it now, and copies it out as the file a
// pull request adds. Nothing here reads anybody's import, and the PDF
// tried is read and forgotten: it is never stored, and only the person who
// sent it sees its rows.
//
// What is wrong with a draft is said in the words of shapes.Parse, which
// are a developer's and English: the person reading them is writing JSON
// to be merged into the code, and a translation of "its id is not
// lower-case words joined by hyphens" would help nobody do that. The page
// around them goes through t like every other.

// UploadPatterns are the routes that take a file. The muxer gives each a
// branch of its own, multipart only and up to MaxUpload, as it does a
// statement's (ledgerapp.UploadPatterns).
var UploadPatterns = []string{"POST /admin/drafts/{id}/try"}

// MaxUpload is the most a trial's body may be: the PDF, and room for the
// form around it.
const MaxUpload = shapebus.MaxFile + 64<<10

// shownRows is how many rows of each part a trial shows: enough to see
// that dates, descriptions and amounts landed in the right places.
const shownRows = 12

type draftView struct {
	// Draft is the draft, or for a new one only its Text.
	Draft shapebus.Draft
	New   bool

	Problems []string

	// File is the name a built-in declaration's file would have, and
	// Copy the declaration as that file says it, once the draft is one.
	File string
	Copy string

	// Trial is the draft tried on a file, named TrialFile.
	Trial     *trialView
	TrialFile string

	Saved   bool
	Problem string
}

// trialView is a trial as the page shows it: the two readings, each part
// with the first few of its rows.
type trialView struct {
	shapebus.Trial

	Draft, Now []partView
}

type partView struct {
	shapebus.Part

	Shown []importbus.Record
	More  int
}

func parts(r shapebus.Reading) []partView {
	var out []partView

	for _, p := range r.Parts {
		n := min(len(p.Records), shownRows)
		out = append(out, partView{Part: p, Shown: p.Records[:n], More: len(p.Records) - n})
	}

	return out
}

// draftRow is a draft as the administration page lists it.
type draftRow struct {
	Draft shapebus.Draft
	Name  string
	OK    bool
}

func draftRows(v *view, drafts []shapebus.Draft) {
	for _, d := range drafts {
		row := draftRow{Draft: d}

		if decl, err := d.Declaration(); err == nil {
			row.Name, row.OK = decl.Name, true
		}

		v.Drafts = append(v.Drafts, row)
	}

	v.Builtin = shapes.Builtin()
}

// newDraft shows a draft not yet kept: a skeleton, from a layout seen if
// ?from names one, or a built-in declaration with its version counted on
// if ?revise names one.
func (a app) newDraft(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var text string

	switch {
	case q.Get("revise") != "":
		i := slices.IndexFunc(shapes.Builtin(), func(d shapes.Declaration) bool { return d.ID == q.Get("revise") })
		if i < 0 {
			http.NotFound(w, r)

			return
		}

		d := shapes.Builtin()[i]
		d.Version++
		text = shapebus.Format(d)

	case q.Get("from") != "":
		sightings, err := a.cfg.Shapes.Sightings(r.Context())
		if err != nil {
			a.failed(w, r, err)

			return
		}

		i := slices.IndexFunc(sightings, func(s shapebus.Sighting) bool { return s.Signature == q.Get("from") })
		if i < 0 {
			http.NotFound(w, r)

			return
		}

		text = shapebus.Skeleton(a.cfg.Now(), &sightings[i])

	default:
		text = shapebus.Skeleton(a.cfg.Now(), nil)
	}

	a.renderDraft(w, r, http.StatusOK, draftView{Draft: shapebus.Draft{Text: text}, New: true})
}

func (a app) createDraft(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.UserFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)

		return
	}

	text := r.PostFormValue("text")

	d, err := a.cfg.Shapes.CreateDraft(r.Context(), a.cfg.Now(), me.ID, text)

	switch {
	case errors.Is(err, shapebus.ErrTooLong):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: shapebus.Draft{Text: text}, New: true, Problem: "too-long"})

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	a.cfg.Log.Info("layout draft started", "draft_id", d.ID.String(), "by", me.ID.String())

	http.Redirect(w, r, "/admin/drafts/"+d.ID.String(), http.StatusSeeOther)
}

// draftID reads the path's draft, answering 404 if there is none.
func (a app) draftID(w http.ResponseWriter, r *http.Request) (shapebus.Draft, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return shapebus.Draft{}, false
	}

	d, err := a.cfg.Shapes.Draft(r.Context(), id)

	switch {
	case errors.Is(err, shapebus.ErrNotFound):
		http.NotFound(w, r)

		return d, false
	case err != nil:
		a.failed(w, r, err)

		return d, false
	}

	return d, true
}

func (a app) draft(w http.ResponseWriter, r *http.Request) {
	d, ok := a.draftID(w, r)
	if !ok {
		return
	}

	a.renderDraft(w, r, http.StatusOK, draftView{Draft: d, Saved: r.URL.Query().Has("saved")})
}

func (a app) saveDraft(w http.ResponseWriter, r *http.Request) {
	d, ok := a.draftID(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)

		return
	}

	text := r.PostFormValue("text")

	saved, err := a.cfg.Shapes.SaveDraft(r.Context(), a.cfg.Now(), d.ID, text)

	switch {
	case errors.Is(err, shapebus.ErrTooLong):
		d.Text = text
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "too-long"})

		return
	case errors.Is(err, shapebus.ErrNotFound):
		http.NotFound(w, r)

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/admin/drafts/"+saved.ID.String()+"?saved", http.StatusSeeOther)
}

func (a app) removeDraft(w http.ResponseWriter, r *http.Request) {
	d, ok := a.draftID(w, r)
	if !ok {
		return
	}

	err := a.cfg.Shapes.RemoveDraft(r.Context(), d.ID)

	switch {
	case errors.Is(err, shapebus.ErrNotFound):
		http.NotFound(w, r)

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	http.Redirect(w, r, "/admin#drafts", http.StatusSeeOther)
}

// tryDraft reads the PDF sent with it by the draft and as it is read now.
// The PDF is held in memory, as large as a statement may be, and dropped
// when the page is written; it never reaches the file store.
func (a app) tryDraft(w http.ResponseWriter, r *http.Request) {
	d, ok := a.draftID(w, r)
	if !ok {
		return
	}

	decl, err := d.Declaration()
	if err != nil {
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "not-yet"})

		return
	}

	name, pdf, err := receivePDF(r)

	switch {
	case errors.Is(err, errNoFile):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "no-file"})

		return
	case errors.Is(err, errTooBig):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "too-big"})

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	trial, err := shapebus.Try(r.Context(), decl, pdf)

	switch {
	case errors.Is(err, ledgerbus.ErrPDFUnavailable):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "pdf-unavailable"})

		return
	case errors.Is(err, ledgerbus.ErrPDFScan):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "pdf-scan"})

		return
	case errors.Is(err, ledgerbus.ErrPDFPassword):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "pdf-password"})

		return
	case errors.Is(err, ledgerbus.ErrPDFUnreadable):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, draftView{Draft: d, Problem: "pdf-unreadable"})

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	tv := trialView{Trial: trial, Draft: parts(trial.Draft), Now: parts(trial.Now)}

	a.renderDraft(w, r, http.StatusOK, draftView{Draft: d, Trial: &tv, TrialFile: name})
}

var (
	errNoFile = errors.New("no file was chosen")
	errTooBig = errors.New("the file is too big")
)

// receivePDF reads the form's "file" part into memory, as long as it is
// no larger than a statement may be.
func receivePDF(r *http.Request) (string, []byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, errNoFile
	}

	for {
		part, err := mr.NextPart()

		switch {
		case errors.Is(err, io.EOF):
			return "", nil, errNoFile
		case err != nil:
			return "", nil, tooBig(err)
		}

		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()

			continue
		}

		var buf bytes.Buffer

		_, err = io.Copy(&buf, io.LimitReader(part, shapebus.MaxFile+1))
		part.Close()

		switch {
		case err != nil:
			return "", nil, tooBig(err)
		case buf.Len() > shapebus.MaxFile:
			return "", nil, errTooBig
		case buf.Len() == 0:
			return "", nil, errNoFile
		}

		return part.FileName(), buf.Bytes(), nil
	}
}

// tooBig is a body cut off by the muxer's limit, said as the file being too
// big, which is what it is.
func tooBig(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return errTooBig
	}

	return err
}

func (a app) renderDraft(w http.ResponseWriter, r *http.Request, status int, v draftView) {
	v.Problems = v.Draft.Problems()

	if decl, err := v.Draft.Declaration(); err == nil {
		v.File = decl.ID + ".json"
		v.Copy = shapebus.Format(decl)
	}

	a.cfg.Render.Render(w, r, status, "admin-draft", v)
}
