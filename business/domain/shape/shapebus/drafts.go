package shapebus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/shapes"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/pdftext"
)

// Drafts (docs/shapes.md, 2) are declarations the site's administrator is
// writing, kept on the site until one reads its documents well enough to
// be copied out as a pull request, where it becomes built in. A draft is
// tried, never used: trying one reads a PDF the administrator uploads for
// the purpose, by the draft and by what reads it now, side by side, and
// keeps nothing of it. No import anybody makes -- the administrator's
// included -- is ever read by a draft, so that a layout is only ever read
// by a declaration a person reviewed.
//
// The administrator's page asks who is asking (adminapp), as it does for
// the rest of that page; these methods do not.

// MaxDraft is the most a draft's text may be. A declaration is a page of
// JSON; this is room for long notes.
const MaxDraft = 32 << 10

// MaxFile is the largest PDF a draft is tried on, as large as a statement
// may be (ledgerbus.MaxFile).
const MaxFile = ledgerbus.MaxFile

// The errors a page tells apart.
var (
	ErrNotFound = errors.New("there is no such draft")

	// ErrTooLong is a draft longer than MaxDraft.
	ErrTooLong = errors.New("the draft is too long")
)

// Draft is a declaration being written.
type Draft struct {
	ID        types.ID
	Text      string
	CreatedBy types.ID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Declaration is the draft read, or what is wrong with it.
func (d Draft) Declaration() (shapes.Declaration, error) {
	return shapes.Parse([]byte(d.Text))
}

// Problems is what is wrong with the draft as a declaration, a line
// each, or nothing. A draft is kept whatever is wrong with it -- it is
// being written -- and only tried once nothing is.
func (d Draft) Problems() []string {
	_, err := d.Declaration()
	if err == nil {
		return nil
	}

	return strings.Split(err.Error(), "\n")
}

func checkText(text string) error {
	if len(text) > MaxDraft {
		return ErrTooLong
	}

	return nil
}

// CreateDraft keeps a new draft.
func (b *Business) CreateDraft(ctx context.Context, now time.Time, actor types.ID, text string) (Draft, error) {
	if err := checkText(text); err != nil {
		return Draft{}, err
	}

	d := Draft{ID: types.NewID(), Text: text, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}

	return d, b.store.CreateDraft(ctx, d)
}

// SaveDraft replaces a draft's text.
func (b *Business) SaveDraft(ctx context.Context, now time.Time, id types.ID, text string) (Draft, error) {
	d, err := b.store.DraftByID(ctx, id)
	if err != nil {
		return Draft{}, err
	}

	if err := checkText(text); err != nil {
		return d, err
	}

	d.Text, d.UpdatedAt = text, now

	return d, b.store.UpdateDraft(ctx, d)
}

// Draft is one draft.
func (b *Business) Draft(ctx context.Context, id types.ID) (Draft, error) {
	return b.store.DraftByID(ctx, id)
}

// Drafts is every draft, the most recently changed first.
func (b *Business) Drafts(ctx context.Context) ([]Draft, error) {
	return b.store.Drafts(ctx)
}

// RemoveDraft throws a draft away.
func (b *Business) RemoveDraft(ctx context.Context, id types.ID) error {
	return b.store.RemoveDraft(ctx, id)
}

// Skeleton is the text a new draft starts from: the fields every
// declaration has, and from a sighting -- if one is given -- the words its
// documents all had, to recognize them by. The words with a "#" in them
// stood for numbers, and those of a column heading line or a period are
// not phrases a document prints as they are kept, so they are left for
// the person to write.
func Skeleton(now time.Time, from *Sighting) string {
	contains := []string{}

	if from != nil {
		for _, w := range from.Frame {
			if !strings.ContainsAny(w, "#|…") && !strings.HasPrefix(w, "period ") {
				contains = append(contains, w)
			}
		}
	}

	notes := []string{"What a reviewer needs to know about the layout: what kind of document it is, how its rows are laid out, and how it is checked."}
	if from != nil {
		notes = append(notes, fmt.Sprintf("Started from the layout seen as %s (%s), in %d files.", from.Signature, from.Producer, from.Files))
	}

	d := shapes.Declaration{
		FirstSeen: now.Format("2006-01"),
		Version:   1,
		Notes:     notes,
		Match:     shapes.Match{Format: "pdf", Contains: contains},
		Layout:    pdfsource.General,
		CheckedBy: []string{"balances"},
	}

	return Format(d)
}

// Format is a declaration as its file is written: indented JSON, its
// fields in the order the built-in files have them, and of its layout only
// what differs from the general reader's, which is all a declaration's
// file says of it.
func Format(d shapes.Declaration) string {
	o := struct {
		ID        string            `json:"id"`
		Name      string            `json:"name"`
		FirstSeen string            `json:"first_seen"`
		Version   int               `json:"version"`
		Notes     []string          `json:"notes"`
		Match     shapes.Match      `json:"match"`
		Layout    map[string]string `json:"layout"`
		CheckedBy []string          `json:"checked_by"`
		Fixture   string            `json:"fixture"`
	}{d.ID, d.Name, d.FirstSeen, d.Version, d.Notes, d.Match, changed(d.Layout), d.CheckedBy, d.Fixture}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(o); err != nil {
		return ""
	}

	return buf.String()
}

// changed is the patterns of a layout that differ from General's, by
// their JSON names.
func changed(l pdfsource.Layout) map[string]string {
	general, _ := json.Marshal(pdfsource.General)
	this, _ := json.Marshal(l)

	var g, t map[string]string

	_ = json.Unmarshal(general, &g)
	_ = json.Unmarshal(this, &t)

	out := map[string]string{}

	for k, v := range t {
		if g[k] != v {
			out[k] = v
		}
	}

	// An emptied pattern is left out of t by omitempty, and says "match
	// nothing", which is a change.
	for k := range g {
		if _, ok := t[k]; !ok {
			out[k] = ""
		}
	}

	return out
}

// --- trying -----------------------------------------------------------------

// Trial is a PDF read two ways: by a draft, and as it is read now -- by
// the built-in declaration that recognizes it, or the general reader.
type Trial struct {
	Producer  string
	Signature string

	Draft, Now Reading

	// Same is whether both readings found the same rows, part for part.
	Same bool
}

// Reading is one way a document was read.
type Reading struct {
	// Layout is the declaration it was read by, "" for the general reader.
	Layout string

	// Matched is whether the declaration recognizes the document: a draft
	// is read on the document whether or not, so that its patterns can be
	// tried before its phrases are right.
	Matched bool

	// NoRows is a document in which this reading found no row.
	NoRows bool

	Parts     []Part
	Structure importbus.Structure
}

// Part is one account's part of a reading -- the only one, for most
// documents.
type Part struct {
	Last4 string

	Records []importbus.Record

	// Check is what checking the rows against the document's own figures
	// found, and Accepted whether it is a way the reading's layout accepts:
	// one of a declaration's CheckedBy, or any, for the general reader.
	Check    ledgerbus.Check
	Accepted bool
}

// Try reads a PDF by a draft and as it is read now. The PDF is only read:
// nothing of it is kept.
func Try(ctx context.Context, d shapes.Declaration, pdf []byte) (Trial, error) {
	if len(pdf) > MaxFile {
		return Trial{}, ledgerbus.ErrPDFUnreadable
	}

	text, err := pdftext.Extract(ctx, pdf)

	switch {
	case errors.Is(err, pdftext.ErrUnavailable):
		return Trial{}, ledgerbus.ErrPDFUnavailable
	case errors.Is(err, pdftext.ErrNoText):
		return Trial{}, ledgerbus.ErrPDFScan
	case errors.Is(err, pdftext.ErrPassword):
		return Trial{}, ledgerbus.ErrPDFPassword
	case err != nil:
		return Trial{}, ledgerbus.ErrPDFUnreadable
	}

	producer, _ := pdftext.Producer(ctx, pdf)

	var t Trial

	t.Producer = importbus.Word(producer)

	// As it is read now.
	if found := shapes.Recognize("pdf", producer, text); len(found) == 1 {
		t.Now = read(text, found[0])
		t.Now.Matched = true
	} else {
		t.Now = read(text, shapes.Declaration{Layout: pdfsource.General})
	}

	if len(t.Now.Structure.Frame) > 0 || t.Producer != "" {
		t.Signature = importbus.Sign("pdf", t.Producer, t.Now.Structure)
	}

	t.Draft = read(text, d)
	t.Draft.Matched = d.Matches("pdf", producer, text)
	t.Same = slices.EqualFunc(t.Draft.Parts, t.Now.Parts, func(a, b Part) bool {
		return a.Last4 == b.Last4 && slices.EqualFunc(a.Records, b.Records, sameRow)
	})

	return t, nil
}

func sameRow(a, b importbus.Record) bool {
	return a.Date.Equal(b.Date) && a.Amount == b.Amount && a.Description == b.Description && a.Pending == b.Pending &&
		a.CheckNumber == b.CheckNumber
}

// read reads a document by a declaration, or by the general reader if it
// has no name, and checks each part.
func read(text string, d shapes.Declaration) Reading {
	r := Reading{Layout: d.Name}

	res, err := pdfsource.ReadAs(text, d.Layout)
	r.Structure = res.Structure

	if err != nil {
		r.NoRows = true

		return r
	}

	parts := res.Accounts
	if len(parts) == 0 {
		parts = []importbus.Account{{Result: res}}
	}

	for _, a := range parts {
		c := ledgerbus.Verify(a.Result.Records, a.Result.Opening, a.Result.Closing, a.Result.Total, false)

		accepted := c.OK
		if d.Name != "" {
			accepted = c.OK && slices.Contains(d.CheckedBy, string(c.Method))
		}

		r.Parts = append(r.Parts, Part{Last4: a.Last4, Records: a.Result.Records, Check: c, Accepted: accepted})
	}

	return r
}
