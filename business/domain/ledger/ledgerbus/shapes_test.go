package ledgerbus_test

import (
	"errors"
	"regexp"
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
)

// A PDF in a layout no declaration describes -- every layout, until there
// are declarations -- imports when its own figures balance, and is refused
// when it states none, even with balances a person typed that agree with
// it. Both layouts are recorded as seen, each file once however often it is
// previewed, with whether it balanced, and with none of its contents.
func TestANewLayout(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "card")

	totalled := w.save(me, "with-total.pdf", printout("$1,246.56"))

	d, err := w.ledger.Prepare(t.Context(), me, acct, totalled, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !d.Shape.New() || d.Shape.Format != "pdf" || d.Shape.Producer != importbus.Word(pdftexttest.Producer) || d.Shape.Signature == "" {
		t.Errorf("shape %+v", d.Shape)
	}

	if !d.Proven || d.Unproven() || !d.Ready() {
		t.Fatalf("a printout with its total: proven %v, ready %v", d.Proven, d.Ready())
	}

	// No total at all: nothing in it shows every row was read.
	bare := w.save(me, "without-total.pdf", printout(""))

	d, err = w.ledger.Prepare(t.Context(), me, acct, bare, nil)
	if err != nil {
		t.Fatal(err)
	}

	if d.Check.Method != ledgerbus.Unchecked || d.Proven || !d.Unproven() || d.Ready() {
		t.Fatalf("a printout without its total: check %+v, proven %v, ready %v", d.Check, d.Proven, d.Ready())
	}

	if _, err := w.ledger.Import(t.Context(), now, me, acct, bare, ledgerbus.Options{Invert: true}); !errors.Is(err, ledgerbus.ErrUnproven) {
		t.Fatalf("imported a new layout that proves nothing: %v", err)
	}

	// Balances typed beside it agree with its rows, and are not the
	// document's word on itself.
	typed := ledgerbus.Options{
		Invert:  true,
		Opening: importbus.Balance{Amount: 0, Known: true},
		Closing: importbus.Balance{Amount: money.Amount(124656), Known: true},
	}

	d, err = w.ledger.Prepare(t.Context(), me, acct, bare, &typed)
	if err != nil {
		t.Fatal(err)
	}

	if d.Check != (ledgerbus.Check{Method: ledgerbus.ByTotals, OK: true}) || d.Ready() {
		t.Fatalf("typed balances: check %+v, ready %v", d.Check, d.Ready())
	}

	if _, err := w.ledger.Import(t.Context(), now, me, acct, bare, typed); !errors.Is(err, ledgerbus.ErrUnproven) {
		t.Fatalf("imported a new layout on typed balances: %v", err)
	}

	if _, err := w.ledger.Import(t.Context(), now, me, acct, totalled, ledgerbus.Options{Invert: true}); err != nil {
		t.Fatalf("the printout with its total: %v", err)
	}

	got, err := w.shapes.Sightings(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// Two layouts: the total's label is part of what a layout is.
	if len(got) != 2 {
		t.Fatalf("%d sightings: %+v", len(got), got)
	}

	leak := regexp.MustCompile(`(?i)\d|pat example|corner|hilltop|café|parish|example\.invalid`)

	for _, s := range got {
		balanced := slices.Contains(s.Frame, "total activity")

		if s.Files != 1 || s.Balanced != map[bool]int{true: 1, false: 0}[balanced] {
			t.Errorf("%s: %d files, %d balanced, frame %q", s.Signature, s.Files, s.Balanced, s.Frame)
		}

		for _, word := range slices.Concat(s.Frame, s.Sections) {
			if leak.MatchString(word) {
				t.Errorf("%q is not a word of the layout's", word)
			}
		}
	}
}
