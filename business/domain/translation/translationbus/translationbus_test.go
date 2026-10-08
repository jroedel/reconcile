package translationbus_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/translation/stores/translationdb"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

func setup(t *testing.T) (*translationbus.Business, *sql.DB, *time.Time) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for range 2 { // twice, to prove Init is idempotent
		if err := translationdb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, translationdb.Expected); err != nil {
		t.Fatal(err)
	}

	now := time.UnixMilli(1_790_000_000_000)

	return translationbus.NewBusiness(translationdb.NewStore(db), func() time.Time { return now }), db, &now
}

// translate stands in for the API that arrives later: it writes a
// translation the way that API will.
func translate(t *testing.T, db *sql.DB, src translationbus.Source, lang types.Lang, text string, status translationbus.Status) {
	t.Helper()

	res, err := db.ExecContext(t.Context(), `
UPDATE ui_translations SET text = ?, status = ? WHERE context = ? AND en = ? AND lang = ?`,
		text, string(status), src.Context, src.EN, string(lang))
	if err != nil {
		t.Fatal(err)
	}

	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("no pending %s translation of %q to fill", lang, src.EN)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

var upload = translationbus.Source{EN: "Upload receipts"}

func TestRegisterOpensAPendingTranslationInEveryOtherLanguage(t *testing.T) {
	bus, db, _ := setup(t)

	if err := bus.Register(t.Context(), translationbus.Uses(upload, upload)); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT count(*) FROM ui_strings`); n != 1 {
		t.Errorf("%d strings for one, registered twice", n)
	}

	if n := count(t, db, `SELECT count(*) FROM ui_translations WHERE status = 'pending'`); n != len(types.Translated) {
		t.Errorf("%d pending translations, want one per language (%d)", n, len(types.Translated))
	}

	if n := count(t, db, `SELECT count(*) FROM ui_translations WHERE lang = 'en'`); n != 0 {
		t.Error("English was opened for translation")
	}
}

// A restart registers everything again. What is translated stays translated,
// and last_seen moves so that a string the code no longer says can be found.
func TestRegisterAgainKeepsTranslationsAndMovesLastSeen(t *testing.T) {
	bus, db, now := setup(t)

	if err := bus.Register(t.Context(), translationbus.Uses(upload)); err != nil {
		t.Fatal(err)
	}

	translate(t, db, upload, types.Spanish, "Subir recibos", translationbus.Approved)

	*now = now.Add(24 * time.Hour)

	if err := bus.Register(t.Context(), translationbus.Uses(upload)); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT count(*) FROM ui_translations WHERE lang = 'es' AND status = 'approved' AND text = 'Subir recibos'`); n != 1 {
		t.Error("registering again lost the translation")
	}

	if n := count(t, db, `SELECT count(*) FROM ui_strings WHERE last_seen = ? AND first_seen < last_seen`, now.UnixMilli()); n != 1 {
		t.Error("last_seen did not move, or first_seen did")
	}
}

func TestTranslate(t *testing.T) {
	bus, db, _ := setup(t)

	verb := translationbus.Source{Context: "verb", EN: "Close"}
	month := translationbus.Source{Context: "month-end", EN: "Close"}
	waiting := translationbus.Source{EN: "Waiting for a match"}

	if err := bus.Register(t.Context(), translationbus.Uses(upload, verb, month, waiting)); err != nil {
		t.Fatal(err)
	}

	translate(t, db, upload, types.Spanish, "Subir recibos", translationbus.Approved)
	translate(t, db, upload, types.Portuguese, "Enviar recibos", translationbus.Draft)
	translate(t, db, verb, types.Spanish, "Cerrar", translationbus.Approved)
	translate(t, db, month, types.Spanish, "Cierre", translationbus.Approved)

	// Before Reload, everything is English: the catalogue is what was read.
	if got := bus.Translate(types.Spanish, "", upload.EN); got != upload.EN {
		t.Errorf("before Reload = %q, want the English", got)
	}

	if err := bus.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		lang types.Lang
		src  translationbus.Source
		want string
	}{
		{"approved", types.Spanish, upload, "Subir recibos"},
		{"a draft is shown too", types.Portuguese, upload, "Enviar recibos"},
		{"pending falls back to English", types.Spanish, waiting, "Waiting for a match"},
		{"English is itself", types.English, upload, "Upload receipts"},
		{"one context", types.Spanish, verb, "Cerrar"},
		{"another context, same English", types.Spanish, month, "Cierre"},
		{"a string never registered", types.Spanish, translationbus.Source{EN: "Nowhere"}, "Nowhere"},
	} {
		if got := bus.Translate(tt.lang, tt.src.Context, tt.src.EN); got != tt.want {
			t.Errorf("%s: Translate = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRegisterRefusesAStringWithNoEnglish(t *testing.T) {
	bus, _, _ := setup(t)

	if err := bus.Register(t.Context(), translationbus.Uses(translationbus.Source{Context: "verb", EN: "  "})); err == nil {
		t.Fatal("a string with no English was registered")
	}
}
