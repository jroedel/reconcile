package ledgerbus

import (
	"context"
	"errors"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
)

// Every file is recognized before it is read further (docs/shapes.md, 1):
// its format, and for a PDF the program that wrote it and the words its
// layout uses, make a signature. A layout a declaration describes is read
// as the declaration says, and the preview names it. Any other is new: it
// is read by the general reader, and recorded as a sighting (shapebus) --
// its words, never its contents -- for whoever teaches the site layouts.
//
// No declaration exists yet; until they do, every layout is new.
//
// A PDF in a new layout is imported only when its own figures prove the
// general reader read it whole: a balance on its rows, its opening and
// closing balance, or the total of its rows. The general reader has only
// ever been shown some layouts, and a document it misreads without
// noticing -- a column taken for another, a section's rows dropped -- is
// caught by nothing but the document's own arithmetic. Balances a person
// types are a check of their own on top, and do not stand in for it: the
// person is typing from the same document the reader may have misread,
// and a declaration that knows the layout is what should allow that.
// A CSV is new until a person maps its columns, which is the person's word
// on its layout, and OFX is a standard; for neither does this change what
// can be imported.

// Shapes is where sightings go (shapebus).
type Shapes interface {
	Seen(ctx context.Context, now time.Time, s importbus.Shape, file string, balanced bool) error
}

// ErrUnproven is a PDF in a layout no declaration describes, whose own
// figures do not prove it was read whole.
var ErrUnproven = errors.New("the PDF's layout is new here, and it states nothing to check that it was read whole")

// recognize is a file's shape. With no declarations yet it is always new.
func recognize(f Format, producer string, st importbus.Structure) importbus.Shape {
	sh := importbus.Shape{Format: string(f), Producer: importbus.Word(producer), Structure: st}
	if len(st.Frame) > 0 || sh.Producer != "" {
		sh.Signature = importbus.Sign(sh.Format, sh.Producer, st)
	}

	return sh
}

// Unproven reports a PDF in a new layout that its own figures do not prove
// was read whole, which is not imported.
func (d Draft) Unproven() bool {
	return d.Format == PDF && d.Shape.New() && !d.Proven
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
