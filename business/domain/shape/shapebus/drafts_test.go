package shapebus_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/shapes"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/shape/shapebus"
	"github.com/jroedel/reconcile/foundation/pdftext"
)

func builtin(t *testing.T, id string) shapes.Declaration {
	t.Helper()

	for _, d := range shapes.Builtin() {
		if d.ID == id {
			return d
		}
	}

	t.Fatalf("no built-in declaration %q", id)

	return shapes.Declaration{}
}

// What Format writes is read back as the same declaration: a built-in one
// copied out of a draft is the file it would be merged as.
func TestFormat(t *testing.T) {
	for _, d := range shapes.Builtin() {
		d.Layout.In = ""
		d.Layout.Out = `(?i)\bmoney out\b`

		got, err := shapes.Parse([]byte(shapebus.Format(d)))
		if err != nil {
			t.Fatalf("%s: %v\n%s", d.ID, err, shapebus.Format(d))
		}

		if got.Layout != d.Layout || got.ID != d.ID || got.Fixture != d.Fixture || !slices.Equal(got.CheckedBy, d.CheckedBy) ||
			!slices.Equal(got.Match.Contains, d.Match.Contains) || !slices.Equal(got.Notes, d.Notes) {
			t.Errorf("%s: read back as %+v", d.ID, got)
		}
	}
}

// A draft started from a sighting has the sighting's plain words to be
// recognized by, and not those that stood for numbers or a period; it is
// not a declaration until a person names it.
func TestSkeleton(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	s := shapebus.Sighting{
		Signature: "abcd1234",
		Producer:  "an engine #",
		Frame:     []string{"beginning balance", "date|description|amount", "period m #, # through m #, #", "account # of #"},
		Files:     3,
	}

	d := shapebus.Draft{Text: shapebus.Skeleton(now, &s)}

	problems := strings.Join(d.Problems(), "\n")
	if !strings.Contains(problems, "id") || !strings.Contains(problems, "no name") || strings.Contains(problems, "phrase") {
		t.Errorf("problems:\n%s", problems)
	}

	for _, want := range []string{`"contains": [`, `"beginning balance"`, `"first_seen": "2026-10"`, "abcd1234", `"layout": {}`} {
		if !strings.Contains(d.Text, want) {
			t.Errorf("no %s in\n%s", want, d.Text)
		}
	}

	for _, unwanted := range []string{"date|description", "through", "account #"} {
		if strings.Contains(d.Text, unwanted) {
			t.Errorf("%s in\n%s", unwanted, d.Text)
		}
	}
}

// A draft is tried on a document beside the reading it has now: one that
// reads it as its built-in declaration does finds the same rows, proven;
// one whose noise takes in a kind of row finds fewer, which do not
// balance.
func TestTry(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	pdf := pdfsourcetest.Consolidated()
	current := builtin(t, "us-consolidated-checking-a")

	same := current
	same.ID, same.Name = "a-draft", "A draft"

	tr, err := shapebus.Try(t.Context(), same, pdf)
	if err != nil {
		t.Fatal(err)
	}

	if tr.Now.Layout != current.Name || !tr.Now.Matched || !tr.Draft.Matched || !tr.Same || tr.Signature == "" {
		t.Fatalf("now %q matched %v, draft matched %v, same %v, signature %q",
			tr.Now.Layout, tr.Now.Matched, tr.Draft.Matched, tr.Same, tr.Signature)
	}

	if len(tr.Draft.Parts) != 2 || tr.Draft.Parts[0].Last4 != pdfsourcetest.First {
		t.Fatalf("parts %+v", tr.Draft.Parts)
	}

	for _, p := range tr.Draft.Parts {
		if !p.Accepted || p.Check.Method != ledgerbus.ByBalances && p.Check.Method != ledgerbus.ByTotals {
			t.Errorf("%s: %+v, accepted %v", p.Last4, p.Check, p.Accepted)
		}
	}

	lost := same
	lost.Layout.Noise = `(?i)remote online deposit`
	lost.Match.Contains = []string{"nothing the document says"}

	tr, err = shapebus.Try(t.Context(), lost, pdf)
	if err != nil {
		t.Fatal(err)
	}

	if tr.Same || tr.Draft.Matched || !tr.Now.Matched {
		t.Errorf("same %v, draft matched %v, now matched %v", tr.Same, tr.Draft.Matched, tr.Now.Matched)
	}

	if !slices.ContainsFunc(tr.Draft.Parts, func(p shapebus.Part) bool { return !p.Accepted }) {
		t.Errorf("without its deposits every part was accepted: %+v", tr.Draft.Parts)
	}
}
