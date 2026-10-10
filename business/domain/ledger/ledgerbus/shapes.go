package ledgerbus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/shapes"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
)

// Every file is recognized before it is read further (docs/shapes.md, 1):
// its format, and for a PDF the program that wrote it and the words its
// layout uses, make a signature. A layout a declaration describes is read
// as the declaration says, and the preview names it. Any other is new: it
// is read by the general reader, and recorded as a sighting (shapebus) --
// its words, never its contents -- for whoever teaches the site layouts.
//
// The declarations are built in (business/domain/importing/shapes), and
// only PDFs are declared so far.
//
// A PDF in a new layout is imported only when its own figures prove the
// general reader read it whole: a balance on its rows, its opening and
// closing balance, or the total of its rows. The general reader has only
// ever been shown some layouts, and a document it misreads without
// noticing -- a column taken for another, a section's rows dropped -- is
// caught by nothing but the document's own arithmetic. Balances a person
// types are a check of their own on top, and do not stand in for it: the
// person is typing from the same document the reader may have misread.
// A PDF in a declared layout is held to more: proven the way its
// declaration says its documents always are (Declaration.CheckedBy). One
// that proves itself some other way, or not at all, was misread, or is
// from a bank that has changed its layout, and is not imported either.
//
// A CSV is new until a person maps its columns, which is the person's word
// on its layout, and OFX is a standard; for neither does this change what
// can be imported.

// Shapes is where sightings go (shapebus).
type Shapes interface {
	Seen(ctx context.Context, now time.Time, s importbus.Shape, file string, balanced bool) error
}

// ErrUnproven is a PDF whose own figures do not prove it was read whole,
// or not the way its layout's declaration says (Draft.Unproven).
var ErrUnproven = errors.New("the PDF's own figures do not prove it was read whole")

// recognize is a file's shape, new until a declaration says otherwise.
func recognize(f Format, producer string, st importbus.Structure) importbus.Shape {
	sh := importbus.Shape{Format: string(f), Producer: importbus.Word(producer), Structure: st}
	if len(st.Frame) > 0 || sh.Producer != "" {
		sh.Signature = importbus.Sign(sh.Format, sh.Producer, st)
	}

	return sh
}

// readLayout reads a PDF's text by the declaration of its layout, if one
// built-in declaration describes it, or else by the general reader, and
// recognizes its shape.
func (b *Business) readLayout(d *Draft, producer, text string) (importbus.Result, error) {
	found := shapes.Recognize(string(PDF), producer, text)
	if len(found) > 1 {
		var ids []string
		for _, f := range found {
			ids = append(ids, f.ID)
		}

		b.log.Error("a PDF statement matches several declarations, and is read as if it matched none",
			"file_id", d.File.ID.String(), "declarations", strings.Join(ids, " "))
	}

	if len(found) != 1 {
		res, err := pdfsource.Read(text)
		d.Shape = recognize(PDF, producer, res.Structure)

		return res, err
	}

	res, err := pdfsource.ReadAs(text, found[0].Layout)
	d.Shape = recognize(PDF, producer, res.Structure)
	d.Shape.Declared, d.Declaration = found[0].Name, found[0]

	return res, err
}

// Unproven reports a PDF that is not imported: in a new layout, one that
// its own figures do not prove was read whole; in a declared one, one they
// do not prove the way its declaration says.
func (d Draft) Unproven() bool {
	switch {
	case d.Format != PDF:
		return false
	case !d.Proven:
		return true
	case d.Shape.New():
		return false
	}

	return !slices.Contains(d.Declaration.CheckedBy, string(d.ProvenBy))
}

// seen records the draft's layout if it is new. A sighting is the site's
// housekeeping, not the person's import: one that cannot be written is
// logged, and the preview goes on.
func (b *Business) seen(ctx context.Context, d Draft) {
	if b.shapes == nil || !d.Shape.New() || d.Shape.Signature == "" {
		return
	}

	if err := b.shapes.Seen(ctx, time.Now(), d.Shape, d.File.SHA256, d.Proven); err != nil && ctx.Err() == nil {
		b.log.Error("a statement's layout could not be recorded", "signature", d.Shape.Signature, "error", err)
	}
}
