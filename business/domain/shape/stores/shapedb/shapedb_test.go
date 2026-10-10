package shapedb_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/business/domain/shape/stores/shapedb"
	"github.com/jroedel/reconcile/business/types"
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

// A draft is kept whatever its text, changed in place, listed the most
// recently changed first, and gone when removed; one that is not there is
// ErrNotFound however it is asked for.
func TestDrafts(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := shapedb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	shapes := shapebus.NewBusiness(nil, shapedb.NewStore(db))
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	admin := types.NewID()

	a, err := shapes.CreateDraft(ctx, now, admin, "not yet a declaration")
	if err != nil {
		t.Fatal(err)
	}

	b, err := shapes.CreateDraft(ctx, now.Add(time.Minute), admin, "{}")
	if err != nil {
		t.Fatal(err)
	}

	if len(a.Problems()) == 0 {
		t.Error("a draft that is not a declaration has no problems")
	}

	if _, err := shapes.SaveDraft(ctx, now.Add(2*time.Minute), a.ID, "changed"); err != nil {
		t.Fatal(err)
	}

	got, err := shapes.Drafts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].ID != a.ID || got[0].Text != "changed" || !got[0].UpdatedAt.Equal(now.Add(2*time.Minute)) ||
		!got[0].CreatedAt.Equal(now) || got[0].CreatedBy != admin || got[1].ID != b.ID {
		t.Fatalf("drafts: %+v", got)
	}

	if _, err := shapes.SaveDraft(ctx, now, a.ID, strings.Repeat("x", shapebus.MaxDraft+1)); !errors.Is(err, shapebus.ErrTooLong) {
		t.Errorf("a draft too long: %v", err)
	}

	if err := shapes.RemoveDraft(ctx, a.ID); err != nil {
		t.Fatal(err)
	}

	gone := a.ID
	if _, err := shapes.Draft(ctx, gone); !errors.Is(err, shapebus.ErrNotFound) {
		t.Errorf("a removed draft: %v", err)
	}

	if _, err := shapes.SaveDraft(ctx, now, gone, "x"); !errors.Is(err, shapebus.ErrNotFound) {
		t.Errorf("saving a removed draft: %v", err)
	}

	if err := shapes.RemoveDraft(ctx, gone); !errors.Is(err, shapebus.ErrNotFound) {
		t.Errorf("removing a removed draft: %v", err)
	}
}
