// Package shapebus is the layouts of statements this site has been sent
// that no declaration describes: the sightings (docs/shapes.md, 1).
//
// Every file imported is recognized first (importbus.Shape). One whose
// layout a declaration describes is read as it says; any other is read by
// the general reader, and its layout is recorded here -- its signature, its
// format and producer, and the words its structure uses -- with how many
// files of it arrived and how many of those the general reader balanced.
// That is what a developer needs to write a declaration for it, and it is
// all that is kept: never a row, an amount, a name or a number, and never
// which account or organization a file was for. The words themselves are
// only ever ones the reader's own patterns matched (importbus.Structure),
// which is what makes them safe to show the site's administrator, who sees
// nothing of anybody's money.
package shapebus

import (
	"context"
	"log/slog"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
)

// Sighting is one layout no declaration describes, as seen so far.
type Sighting struct {
	Signature string
	Format    string
	Producer  string

	// Frame is the words of the layout every file of it had; Sections the
	// headings any file of it had (importbus.Structure).
	Frame, Sections []string

	// Files is how many different files of it arrived, and Balanced how
	// many of those the general reader read whole, by the document's own
	// figures.
	Files, Balanced int

	First, Last time.Time
}

// Storer keeps sightings.
type Storer interface {
	// Seen records one file of a layout, by the file's content hash so that
	// previewing it again counts nothing: the layout, made if it is new and
	// its sections joined with those already seen, and the file, balanced
	// if it was ever balanced.
	Seen(ctx context.Context, s Sighting, file string, balanced bool) error

	// Sightings is every layout seen, the most files first.
	Sightings(ctx context.Context) ([]Sighting, error)
}

// Business is the set of operations on sightings.
type Business struct {
	log   *slog.Logger
	store Storer
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer) *Business {
	return &Business{log: log, store: store}
}

// Seen records a file of a shape no declaration describes; a declared one,
// or one with no signature, is not a sighting and records nothing. file is
// the file's content hash.
func (b *Business) Seen(ctx context.Context, now time.Time, sh importbus.Shape, file string, balanced bool) error {
	if !sh.New() || sh.Signature == "" {
		return nil
	}

	s := Sighting{
		Signature: sh.Signature,
		Format:    sh.Format,
		Producer:  sh.Producer,
		Frame:     sh.Structure.Frame,
		Sections:  sh.Structure.Sections,
		First:     now,
		Last:      now,
	}

	return b.store.Seen(ctx, s, file, balanced)
}

// Sightings is every layout seen. It is the site administrator's to read,
// which the page that shows it checks, as the rest of that page does
// (adminapp).
func (b *Business) Sightings(ctx context.Context) ([]Sighting, error) {
	return b.store.Sightings(ctx)
}
