package shapes_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/shapes"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
)

func rows(res importbus.Result) []string {
	parts := res.Accounts
	if len(parts) == 0 {
		parts = []importbus.Account{{Result: res}}
	}

	var out []string

	for _, a := range parts {
		for _, r := range a.Result.Records {
			out = append(out, fmt.Sprintf("%s %s %s %s %v", a.Last4, r.Date.Format("2006-01-02"), r.Amount, r.Description, r.Pending))
		}
	}

	return out
}

// Every built-in declaration has a drawn document of its layout, which it
// alone recognizes, and reads it into the rows the general reader reads
// from it. Whether those rows prove themselves the way the declaration
// says is the ledger's test (ledgerbus, TestEveryDeclarationOnItsFixture).
func TestEveryDeclarationOnItsFixture(t *testing.T) {
	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	builtin := shapes.Builtin()
	if len(builtin) < 2 {
		t.Fatalf("%d built-in declarations", len(builtin))
	}

	for _, d := range builtin {
		t.Run(d.ID, func(t *testing.T) {
			draw, ok := pdfsourcetest.Fixtures[d.Fixture]
			if !ok {
				t.Fatalf("its fixture %q is not drawn in pdfsourcetest.Fixtures", d.Fixture)
			}

			text, err := pdftext.Extract(t.Context(), draw())
			if err != nil {
				t.Fatal(err)
			}

			got := shapes.Recognize("pdf", pdftexttest.Producer, text)
			if len(got) != 1 || got[0].ID != d.ID {
				var ids []string
				for _, g := range got {
					ids = append(ids, g.ID)
				}

				t.Fatalf("its fixture is recognized as %q", ids)
			}

			declared, err := pdfsource.ReadAs(text, d.Layout)
			if err != nil {
				t.Fatal(err)
			}

			general, err := pdfsource.Read(text)
			if err != nil {
				t.Fatal(err)
			}

			if len(rows(declared)) == 0 || !slices.Equal(rows(declared), rows(general)) {
				t.Errorf("read by the declaration:\n%s\nby the general reader:\n%s",
					strings.Join(rows(declared), "\n"), strings.Join(rows(general), "\n"))
			}
		})
	}
}

const valid = `{
  "id": "example-layout",
  "name": "An example layout",
  "first_seen": "2026-10",
  "version": 1,
  "notes": ["Invented, for the test."],
  "match": {"format": "pdf", "contains": ["Lodgements  and   Drawings"], "lacks": ["specimen"]},
  "layout": {"in": "(?i)\\blodgements\\b"},
  "checked_by": ["totals"],
  "fixture": "none"
}`

// A declaration changes the patterns it names and keeps the general
// reader's for the rest.
func TestParse(t *testing.T) {
	d, err := shapes.Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}

	if d.Layout.In != `(?i)\blodgements\b` || d.Layout.Out != pdfsource.General.Out || d.Layout.Name != "An example layout" {
		t.Errorf("layout: in %q, out %q, name %q", d.Layout.In, d.Layout.Out, d.Layout.Name)
	}

	for name, broken := range map[string]string{
		"an unknown field":     strings.Replace(valid, `"notes"`, `"note"`, 1),
		"an id with capitals":  strings.Replace(valid, `"example-layout"`, `"Example-Layout"`, 1),
		"no phrase":            strings.Replace(valid, `"contains": ["Lodgements  and   Drawings"]`, `"contains": []`, 1),
		"an empty phrase":      strings.Replace(valid, `"lacks": ["specimen"]`, `"lacks": ["  "]`, 1),
		"a pattern":            strings.Replace(valid, `(?i)\\blodgements\\b`, `(?i)(lodgements`, 1),
		"a way of checking":    strings.Replace(valid, `["totals"]`, `["eyeballing"]`, 1),
		"another format":       strings.Replace(valid, `"format": "pdf"`, `"format": "csv"`, 1),
		"a month":              strings.Replace(valid, `"2026-10"`, `"October"`, 1),
		"a producer's pattern": strings.Replace(valid, `"format": "pdf",`, `"format": "pdf", "producer": "(",`, 1),
	} {
		if broken == valid {
			t.Fatalf("%s: the test's replacement changed nothing", name)
		}

		if _, err := shapes.Parse([]byte(broken)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A document is recognized by its first pages, ignoring case and spacing,
// and not by a phrase it lacks or one past them.
func TestMatches(t *testing.T) {
	d, err := shapes.Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}

	page := "EXAMPLE SAVINGS\n   LODGEMENTS AND\n DRAWINGS    \n"

	for _, c := range []struct {
		name, format, producer, text string
		want                         bool
	}{
		{"its phrase, across a line", "pdf", "", page, true},
		{"another format", "csv", "", page, false},
		{"a phrase it lacks", "pdf", "", page + "SPECIMEN", false},
		{"its phrase past the first pages", "pdf", "", "a\fb\fc\f" + page, false},
		{"its phrase on the last page counted", "pdf", "", "a\fb\f" + page, true},
	} {
		if got := d.Matches(c.format, c.producer, c.text); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}

	d.Match.Producer = `^Example Engine`
	if d.Matches("pdf", "Another Engine 2.0", page) || !d.Matches("pdf", "Example Engine 2.0", page) {
		t.Error("the producer is not matched")
	}
}
