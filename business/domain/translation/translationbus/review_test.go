package translationbus_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
)

// problem is the review problem err carries, or "".
func problem(err error) string {
	if r, ok := errors.AsType[translationbus.Refusal](err); ok {
		return r.Problem
	}

	return ""
}

// The administrator names translators, a language at a time; a translator
// may look over and write their languages and no other, and may name
// nobody.
func TestTranslatorsAreNamedByTheAdministrator(t *testing.T) {
	bus, _, now := setup(t)
	ctx := t.Context()

	ana := translationbus.Translator{ID: types.NewID()}

	if ok, _ := bus.MayTranslateAny(ctx, ana); ok {
		t.Fatal("somebody nobody named may translate")
	}

	if err := bus.AddTranslator(ctx, *now, ana, ana.ID, types.Spanish); !errors.Is(err, translationbus.ErrForbidden) {
		t.Errorf("a translator naming themselves: %v", err)
	}

	if err := bus.AddTranslator(ctx, *now, admin, ana.ID, types.English); !errors.Is(err, translationbus.ErrInvalid) {
		t.Errorf("a translator of English: %v", err)
	}

	for range 2 { // the second is no error, and no second grant
		if err := bus.AddTranslator(ctx, *now, admin, ana.ID, types.Portuguese); err != nil {
			t.Fatal(err)
		}
	}

	if langs, _ := bus.Languages(ctx, ana); !slices.Equal(langs, []types.Lang{types.Portuguese}) {
		t.Errorf("her languages: %v", langs)
	}

	if ok, _ := bus.MayTranslate(ctx, ana, types.Spanish); ok {
		t.Error("a translator of Portuguese may translate Spanish")
	}

	if _, err := bus.Counts(ctx, ana, types.Spanish); !errors.Is(err, translationbus.ErrForbidden) {
		t.Errorf("her Spanish counts: %v", err)
	}

	grants, err := bus.Translators(ctx, admin)
	if err != nil || len(grants) != 1 || grants[0].UserID != ana.ID || grants[0].GrantedBy != admin.ID {
		t.Fatalf("the translators: %+v %v", grants, err)
	}

	if _, err := bus.Translators(ctx, ana); !errors.Is(err, translationbus.ErrForbidden) {
		t.Errorf("a translator listing translators: %v", err)
	}

	if err := bus.RemoveTranslator(ctx, admin, ana.ID, types.Portuguese); err != nil {
		t.Fatal(err)
	}

	if ok, _ := bus.MayTranslateAny(ctx, ana); ok {
		t.Error("a removed translator may still translate")
	}

	// The administrator is never listed, and translates every language.
	if langs, _ := bus.Languages(ctx, admin); !slices.Equal(langs, types.Translated) {
		t.Errorf("the administrator's languages: %v", langs)
	}
}

// Looking over Claude's drafts: kept as they are, changed, or sent back,
// each moving between the queues, and each refused when the words are not
// what the reviewer was shown.
func TestLookingOverClaudesTranslations(t *testing.T) {
	bus, _, now := setup(t)
	ctx := t.Context()

	if err := bus.Register(ctx, translationbus.Uses(upload, waitingN, statement)); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Write(ctx, *now, admin, types.Spanish, []translationbus.Written{
		{Source: upload, Text: "Subir comprobantes"},
		{Source: waitingN, Text: "{n} comprobantes esperando"},
	}); err != nil {
		t.Fatal(err)
	}

	counts := func() translationbus.Counts {
		t.Helper()

		c, err := bus.Counts(ctx, admin, types.Spanish)
		if err != nil {
			t.Fatal(err)
		}

		return c
	}

	if c := counts(); c != (translationbus.Counts{ToCheck: 2, Untouched: 1}) {
		t.Fatalf("before: %+v", c)
	}

	// Claude rewrote it while the reviewer read the old words.
	if err := bus.Keep(ctx, *now, admin, types.Spanish, upload, "Subir recibos", "Subir recibos"); problem(err) != translationbus.ProblemChanged {
		t.Errorf("keeping words no longer there: %v", err)
	}

	// Looks right: approved, still Claude's.
	if err := bus.Keep(ctx, *now, admin, types.Spanish, upload, "Subir comprobantes", "Subir comprobantes"); err != nil {
		t.Fatal(err)
	}

	got, _, _ := bus.Queue(ctx, admin, types.Spanish, translationbus.Done, 0)
	if len(got) != 1 || got[0].Origin != translationbus.ByClaude {
		t.Fatalf("approved: %+v", got)
	}

	// Sent back: a draft again, with the note, first in Claude's list and
	// still on the pages.
	if err := bus.SendBack(ctx, *now, admin, types.Spanish, waitingN, "{n} comprobantes esperando", "  "); problem(err) != translationbus.ProblemNote {
		t.Errorf("no note: %v", err)
	}

	if err := bus.SendBack(ctx, *now, admin, types.Spanish, waitingN, "{n} comprobantes esperando", "Say what they wait for."); err != nil {
		t.Fatal(err)
	}

	if err := bus.SendBack(ctx, *now, admin, types.Spanish, statement, "", "There is nothing here."); problem(err) != translationbus.ProblemNothing {
		t.Errorf("sending back nothing: %v", err)
	}

	if c := counts(); c != (translationbus.Counts{Done: 1, SentBack: 1, Untouched: 1}) {
		t.Errorf("after: %+v", c)
	}

	w, err := bus.Pending(ctx, admin, types.Spanish, 10)
	if err != nil || len(w.Items) != 2 || w.Items[0].Source != waitingN || w.Items[0].Note != "Say what they wait for." {
		t.Fatalf("Claude's list: %+v %v", w.Items, err)
	}

	if es := bus.Translate(types.Spanish, "", waitingN.EN); es != "{n} comprobantes esperando" {
		t.Errorf("the page shows %q", es)
	}

	// Changed by hand: the person's, approved, on the pages at once, and
	// held to the same rules.
	if err := bus.Keep(ctx, *now, admin, types.Spanish, waitingN, "{n} comprobantes esperando", "{count} comprobantes por asociar"); problem(err) != translationbus.ProblemPlaceholders {
		t.Errorf("a dropped placeholder: %v", err)
	}

	if err := bus.Keep(ctx, *now, admin, types.Spanish, waitingN, "{n} comprobantes esperando", "{n} comprobantes por asociar"); err != nil {
		t.Fatal(err)
	}

	if es := bus.Translate(types.Spanish, "", waitingN.EN); es != "{n} comprobantes por asociar" {
		t.Errorf("the page shows %q", es)
	}

	// And translated by hand from nothing.
	if err := bus.Keep(ctx, *now, admin, types.Spanish, statement, "", "Estado de cuenta"); err != nil {
		t.Fatal(err)
	}

	done, total, _ := bus.Queue(ctx, admin, types.Spanish, translationbus.Done, 0)
	if total != 3 {
		t.Fatalf("approved: %d", total)
	}

	for _, d := range done {
		if want := map[string]translationbus.Origin{upload.EN: translationbus.ByClaude, waitingN.EN: translationbus.ByPerson, statement.EN: translationbus.ByPerson}[d.EN]; d.Origin != want || d.Note != "" {
			t.Errorf("%q: %s %q", d.EN, d.Origin, d.Note)
		}
	}

	// Claude may not change any of them now.
	res, _ := bus.Write(ctx, *now, admin, types.Spanish, []translationbus.Written{{Source: statement, Text: "Extracto"}})
	if res[0].Problem != translationbus.ProblemApproved {
		t.Errorf("Claude over a person's words: %+v", res[0])
	}
}

// All of these look right: the drafts still as shown are approved; one
// changed or sent back meanwhile is left.
func TestApprovingAPageAtOnce(t *testing.T) {
	bus, _, now := setup(t)
	ctx := t.Context()

	if err := bus.Register(ctx, translationbus.Uses(upload, waitingN, statement)); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Write(ctx, *now, admin, types.Portuguese, []translationbus.Written{
		{Source: upload, Text: "Enviar comprovantes"},
		{Source: waitingN, Text: "{n} comprovantes aguardando"},
		{Source: statement, Text: "Extrato"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := bus.SendBack(ctx, *now, admin, types.Portuguese, statement, "Extrato", "Is this the bank's?"); err != nil {
		t.Fatal(err)
	}

	n, err := bus.ApproveAll(ctx, *now, admin, types.Portuguese, []translationbus.Shown{
		{Source: upload, Text: "Enviar comprovantes"},
		{Source: waitingN, Text: "{n} recibos aguardando"}, // not what is there
		{Source: statement, Text: "Extrato"},               // sent back
		{Source: translationbus.Source{EN: "No such string"}, Text: "x"},
	})
	if err != nil || n != 1 {
		t.Fatalf("approved %d, %v", n, err)
	}

	if c, _ := bus.Counts(ctx, admin, types.Portuguese); c != (translationbus.Counts{Done: 1, ToCheck: 1, SentBack: 1}) {
		t.Errorf("after: %+v", c)
	}
}
