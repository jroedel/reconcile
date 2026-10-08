package translationbus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
)

var (
	admin = translationbus.Translator{ID: types.NewID(), SiteAdmin: true}

	waitingN  = translationbus.Source{EN: "{n} receipts waiting for a match"}
	statement = translationbus.Source{EN: "Statement"}
)

func TestClaudesTranslationsAreShownAtOnce(t *testing.T) {
	bus, _, now := setup(t)

	if err := bus.Register(t.Context(), []translationbus.Use{
		{Source: upload, Pages: []string{"inbox.html"}},
		{Source: upload, Pages: []string{"project.html", "inbox.html"}},
		{Source: waitingN},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := bus.Write(t.Context(), *now, admin, types.Spanish, []translationbus.Written{
		{Source: upload, Text: "  Subir comprobantes "},
		{Source: waitingN, Text: "{n} comprobantes esperando"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range got {
		if r.Outcome != translationbus.Created {
			t.Errorf("%q: %s %s", r.EN, r.Outcome, r.Problem)
		}
	}

	if es := bus.Translate(types.Spanish, "", upload.EN); es != "Subir comprobantes" {
		t.Errorf("the page shows %q", es)
	}

	// The same again changes nothing; a new text is an update.
	got, _ = bus.Write(t.Context(), *now, admin, types.Spanish, []translationbus.Written{
		{Source: upload, Text: "Subir comprobantes"},
		{Source: waitingN, Text: "Hay {n} comprobantes esperando"},
	})
	if got[0].Outcome != translationbus.Unchanged || got[1].Outcome != translationbus.Updated {
		t.Errorf("again: %+v", got)
	}

	list, total, err := bus.List(t.Context(), admin, types.Spanish, translationbus.Draft, 10, 0)
	if err != nil || total != 2 || list[1].Origin != translationbus.ByClaude || list[1].UpdatedBy != admin.ID {
		t.Fatalf("the drafts: %+v, %d, %v", list, total, err)
	}

	if !slices.Equal(list[0].Pages, []string{"inbox.html", "project.html"}) {
		t.Errorf("the pages of %q: %v", list[0].EN, list[0].Pages)
	}
}

func TestWhatClaudeMayNotWrite(t *testing.T) {
	bus, db, now := setup(t)

	if err := bus.Register(t.Context(), translationbus.Uses(upload, waitingN, statement)); err != nil {
		t.Fatal(err)
	}

	// A person approved this one; it is theirs.
	translate(t, db, statement, types.Spanish, "Estado de cuenta", translationbus.Approved)

	if err := bus.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}

	got, err := bus.Write(t.Context(), *now, admin, types.Spanish, []translationbus.Written{
		{Source: waitingN, Text: "comprobantes esperando"},
		{Source: waitingN, Text: "{n} {count} comprobantes"},
		{Source: upload, Text: "<b>Subir</b> comprobantes"},
		{Source: upload, Text: "   "},
		{Source: upload, Text: strings.Repeat("x", 4*len(upload.EN)+101)},
		{Source: translationbus.Source{EN: "Never said"}, Text: "Nunca"},
		{Source: translationbus.Source{Context: "verb", EN: "Upload receipts"}, Text: "Subir"},
		{Source: statement, Text: "Extracto"},
		{Source: statement, Text: "Estado de cuenta"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"placeholders", "placeholders", "markup", "empty", "length", "unknown", "unknown", "approved", ""}
	for i, r := range got {
		if r.Problem != want[i] {
			t.Errorf("%d, %q: %s %q, want %q", i, r.EN, r.Outcome, r.Problem, want[i])
		}
	}

	if got[8].Outcome != translationbus.Unchanged {
		t.Errorf("the approved text sent unchanged: %s", got[8].Outcome)
	}

	if es := bus.Translate(types.Spanish, "", statement.EN); es != "Estado de cuenta" {
		t.Errorf("the approved translation became %q", es)
	}

	// Somebody who is not a translator may do none of it.
	nobody := translationbus.Translator{ID: types.NewID()}

	if _, err := bus.Write(t.Context(), *now, nobody, types.Spanish, nil); !errors.Is(err, translationbus.ErrForbidden) {
		t.Errorf("a stranger writes: %v", err)
	}

	if _, err := bus.Pending(t.Context(), nobody, types.Spanish, 10); !errors.Is(err, translationbus.ErrForbidden) {
		t.Errorf("a stranger reads: %v", err)
	}

	if _, err := bus.Write(t.Context(), *now, admin, types.English, nil); !errors.Is(err, translationbus.ErrForbidden) {
		t.Errorf("English is not translated: %v", err)
	}

	if _, err := bus.Write(t.Context(), *now, admin, types.Spanish, make([]translationbus.Written, translationbus.MaxBatch+1)); !errors.Is(err, translationbus.ErrInvalid) {
		t.Errorf("too many at once: %v", err)
	}
}

func TestWhatClaudeIsAskedFor(t *testing.T) {
	bus, db, now := setup(t)

	if err := bus.Register(t.Context(), translationbus.Uses(upload, waitingN, statement)); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Write(t.Context(), *now, admin, types.Spanish, []translationbus.Written{
		{Source: statement, Text: "Estado de cuenta"},
		{Source: waitingN, Text: "{n} comprobantes"},
	}); err != nil {
		t.Fatal(err)
	}

	// A reviewer sent one back.
	if _, err := db.ExecContext(t.Context(), `UPDATE ui_translations SET note = 'Say "esperando"' WHERE en = ? AND lang = 'es'`, waitingN.EN); err != nil {
		t.Fatal(err)
	}

	w, err := bus.Pending(t.Context(), admin, types.Spanish, 10)
	if err != nil {
		t.Fatal(err)
	}

	if w.Remaining != 2 || len(w.Items) != 2 || w.Items[0].EN != waitingN.EN || w.Items[0].Note == "" || w.Items[1].EN != upload.EN {
		t.Errorf("waiting: %d, %+v", w.Remaining, w.Items)
	}

	if !slices.Contains(w.Glossary, translationbus.Term{EN: "Statement", Text: "Estado de cuenta"}) {
		t.Errorf("the glossary: %+v", w.Glossary)
	}

	// The answer to a note clears it, even with the same words.
	if got, _ := bus.Write(t.Context(), *now, admin, types.Spanish, []translationbus.Written{{Source: waitingN, Text: "{n} comprobantes"}}); got[0].Outcome != translationbus.Updated {
		t.Errorf("answering a note: %s", got[0].Outcome)
	}

	// A string the binary no longer says is asked for no more.
	*now = now.Add(time.Hour)

	if err := bus.Register(t.Context(), translationbus.Uses(statement, waitingN)); err != nil {
		t.Fatal(err)
	}

	if w, _ := bus.Pending(t.Context(), admin, types.Spanish, 10); w.Remaining != 0 {
		t.Errorf("a string no longer used is still asked for: %+v", w.Items)
	}

	if w, _ := bus.Pending(t.Context(), admin, types.Portuguese, 1); w.Remaining != 2 || len(w.Items) != 1 {
		t.Errorf("Portuguese, one at a time: %d, %d", w.Remaining, len(w.Items))
	}
}
