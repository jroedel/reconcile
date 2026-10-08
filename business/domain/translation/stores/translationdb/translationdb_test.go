package translationdb_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/business/domain/translation/stores/translationdb"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// before is the schema as it stood before pages, origin and note, written
// out rather than derived (CLAUDE.md): every database on a server that ran
// the release before this one looks like this.
const before = `
CREATE TABLE IF NOT EXISTS ui_strings (
    context     TEXT    NOT NULL,
    en          TEXT    NOT NULL,
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    PRIMARY KEY (context, en)
) STRICT;

CREATE TABLE IF NOT EXISTS ui_translations (
    context     TEXT    NOT NULL,
    en          TEXT    NOT NULL,
    lang        TEXT    NOT NULL,
    text        TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL CHECK (status IN ('pending', 'draft', 'approved')),

    -- The user who wrote or approved it; NULL for a pending row, and for one
    -- written through the API until it records who holds the key.
    updated_by  TEXT,
    updated_at  INTEGER NOT NULL,

    PRIMARY KEY (context, en, lang),
    FOREIGN KEY (context, en) REFERENCES ui_strings (context, en) ON DELETE CASCADE
) STRICT;
`

// A database from before the later columns gets them, keeps its rows, and
// can be written through.
func TestInitOverTheSchemaBefore(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.ExecContext(t.Context(), before); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(t.Context(), `
INSERT INTO ui_strings (context, en, first_seen, last_seen) VALUES ('', 'Receipts', 1, 1);
INSERT INTO ui_translations (context, en, lang, text, status, updated_at) VALUES ('', 'Receipts', 'es', 'Recibos', 'draft', 1);`); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := translationdb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, translationdb.Expected); err != nil {
		t.Fatal(err)
	}

	store := translationdb.NewStore(db)
	src := translationbus.Source{EN: "Receipts"}

	got, err := store.Get(t.Context(), src, types.Spanish)
	if err != nil || got.Text != "Recibos" || got.Origin != "" || got.Note != "" || len(got.Pages) != 0 {
		t.Fatalf("the old row: %+v, %v", got, err)
	}

	got.Text, got.Origin = "Comprobantes", translationbus.ByClaude
	if err := store.Put(t.Context(), got); err != nil {
		t.Fatal(err)
	}

	if err := store.Register(t.Context(), []translationbus.Use{{Source: src, Pages: []string{"inbox.html"}}}, types.Translated, got.UpdatedAt); err != nil {
		t.Fatal(err)
	}

	if got, _ := store.Get(t.Context(), src, types.Spanish); got.Text != "Comprobantes" || got.Origin != translationbus.ByClaude || len(got.Pages) != 1 {
		t.Errorf("written through: %+v", got)
	}
}
