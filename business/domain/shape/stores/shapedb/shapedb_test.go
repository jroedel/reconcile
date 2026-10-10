package shapedb_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/domain/shape/stores/shapedb"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// A layout's files are counted once each however often they are previewed,
// a file is balanced once it ever was, and the sections of every file are
// kept together; a declared shape is no sighting at all.
func TestSightings(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := shapedb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	if err := shapedb.Init(ctx, db); err != nil {
		t.Fatalf("Init is not idempotent: %v", err)
	}

	if err := sqldb.CheckSchema(ctx, db, shapedb.Expected); err != nil {
		t.Fatal(err)
	}

	shapes := shapebus.NewBusiness(nil, shapedb.NewStore(db))
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	shape := func(sections ...string) importbus.Shape {
		s := importbus.Shape{Format: "pdf", Producer: "an engine #", Structure: importbus.Structure{
			Frame:    []string{"beginning balance", "ending balance"},
			Sections: sections,
		}}
		s.Signature = importbus.Sign(s.Format, s.Producer, s.Structure)

		return s
	}

	for _, seen := range []struct {
		shape    importbus.Shape
		file     string
		balanced bool
	}{
		{shape("deposits"), "aaa", false},
		{shape("deposits"), "aaa", true},
		{shape("deposits"), "aaa", false},
		{shape("withdrawals", "deposits"), "bbb", false},
		{importbus.Shape{Format: "pdf", Signature: "declared", Declared: "a declaration"}, "ccc", true},
	} {
		if err := shapes.Seen(ctx, now, seen.shape, seen.file, seen.balanced); err != nil {
			t.Fatal(err)
		}

		now = now.Add(time.Minute)
	}

	got, err := shapes.Sightings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("%d sightings: %+v", len(got), got)
	}

	s := got[0]
	if s.Signature != shape().Signature || s.Files != 2 || s.Balanced != 1 {
		t.Errorf("signature %s, %d files, %d balanced", s.Signature, s.Files, s.Balanced)
	}

	if !slices.Equal(s.Sections, []string{"deposits", "withdrawals"}) || !slices.Equal(s.Frame, []string{"beginning balance", "ending balance"}) {
		t.Errorf("sections %q, frame %q", s.Sections, s.Frame)
	}

	if !s.First.Equal(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)) || !s.Last.Equal(time.Date(2026, 10, 9, 12, 3, 0, 0, time.UTC)) {
		t.Errorf("first %s, last %s", s.First, s.Last)
	}
}
