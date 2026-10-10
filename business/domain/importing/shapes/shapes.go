// Package shapes is the declarations of statement layouts: for one bank's
// layout, how to recognize a document in it, what its headings and labels
// say, and how it proves it was read whole (docs/shapes.md, 2).
//
// A declaration is data, not code. The PDF reader's rules are the same for
// every document (pdfsource); what one layout calls its sections and its
// balances is said in a declaration's JSON, and whatever it does not say is
// the general reader's (pdfsource.General). The built-in ones are in
// declarations/, one file each, embedded in the binary and changed only by
// pull request, each with a drawn document of its layout to be tested on
// (pdfsourcetest.Fixtures).
//
// They are named generically -- "us-consolidated-checking-a" -- and not
// after their bank. Which bank a layout is matters to nobody reading it,
// and this repository keeps every bank and card issuer out of its files;
// a declaration recognizes its documents by the words of their layout,
// never by the bank's name.
package shapes

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
)

// Declaration is one layout, said.
type Declaration struct {
	// ID is its file's name, lower case and hyphens; Name what the preview
	// says a document was read as.
	ID   string `json:"id"`
	Name string `json:"name"`

	// FirstSeen is the month a document in the layout was first read
	// ("2026-10"), and Version counts its changes since.
	FirstSeen string `json:"first_seen"`
	Version   int    `json:"version"`

	// Notes is what a reviewer needs to know about the layout, a paragraph
	// each.
	Notes []string `json:"notes"`

	Match Match `json:"match"`

	// Layout is the reader's words for it: General's, with whatever the
	// declaration changes. An empty pattern matches nothing.
	Layout pdfsource.Layout `json:"layout"`

	// CheckedBy is how its documents prove they were read whole -- by
	// "balances" on their rows or at the end of each day, by opening and
	// closing "totals", or by the "sum" of the rows they state -- and a
	// document recognized as the layout and not proven one of these ways
	// is not imported: either it was misread or the layout has changed.
	CheckedBy []string `json:"checked_by"`

	// Fixture names the drawn document it is tested on
	// (pdfsourcetest.Fixtures).
	Fixture string `json:"fixture"`
}

// Match is how a document is recognized as the layout.
type Match struct {
	// Format is "pdf": only PDFs are declared so far.
	Format string `json:"format"`

	// Producer, if given, is a pattern the PDF's producer must match
	// (pdftext.Producer). A page printed from a browser has the browser's,
	// which says nothing of the layout, and leaves it out.
	Producer string `json:"producer,omitempty"`

	// Contains is phrases the first pages must all have, and Lacks
	// phrases they must not, compared ignoring case and spacing. They are
	// the layout's own words -- its headings and labels -- and never a
	// bank's name.
	Contains []string `json:"contains"`
	Lacks    []string `json:"lacks,omitempty"`
}

// MatchPages is how many pages a document is recognized by: enough for
// its summary and its first headings, wherever its cover puts them.
const MatchPages = 3

// The ways a document proves itself, as CheckedBy names them; they are
// ledgerbus.Method's values.
var checks = []string{"balances", "totals", "sum"}

var id = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Parse reads a declaration and says what is wrong with it, if anything.
// A field it does not know is wrong, so that a misspelt one is not quietly
// the general reader's.
func Parse(data []byte) (Declaration, error) {
	d := Declaration{Layout: pdfsource.General}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&d); err != nil {
		return Declaration{}, fmt.Errorf("reading the declaration: %w", err)
	}

	d.Layout.Name = d.Name

	var problems []error

	problem := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if !id.MatchString(d.ID) {
		problem("its id %q is not lower-case words joined by hyphens", d.ID)
	}

	if strings.TrimSpace(d.Name) == "" {
		problem("it has no name")
	}

	if !regexp.MustCompile(`^\d{4}-\d{2}$`).MatchString(d.FirstSeen) {
		problem("first_seen %q is not a month, such as 2026-10", d.FirstSeen)
	}

	if d.Version < 1 {
		problem("its version is not 1 or more")
	}

	if d.Match.Format != "pdf" {
		problem("its format %q is not pdf, the only one declared so far", d.Match.Format)
	}

	if _, err := regexp.Compile(d.Match.Producer); err != nil {
		problem("its producer pattern: %w", err)
	}

	if len(d.Match.Contains) == 0 {
		problem("it contains no phrase to recognize a document by")
	}

	for _, p := range slices.Concat(d.Match.Contains, d.Match.Lacks) {
		if normalize(p) == "" {
			problem("one of its phrases is empty")
		}
	}

	if err := d.Layout.Check(); err != nil {
		problem("%w", err)
	}

	if len(d.CheckedBy) == 0 {
		problem("it does not say how its documents are checked")
	}

	for _, c := range d.CheckedBy {
		if !slices.Contains(checks, c) {
			problem("checked_by %q is none of %s", c, strings.Join(checks, ", "))
		}
	}

	if err := errors.Join(problems...); err != nil {
		return Declaration{}, fmt.Errorf("the declaration %q: %w", d.ID, err)
	}

	return d, nil
}

// normalize is a phrase or a document as they are compared: lower case,
// with every run of spaces one space.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// first is the text of a document's first pages, normalized.
func first(text string) string {
	pages := strings.SplitN(text, "\f", MatchPages+1)

	return normalize(strings.Join(pages[:min(len(pages), MatchPages)], " "))
}

// Matches reports whether a document is in the layout, by its format, its
// producer and the text of its first pages.
func (d Declaration) Matches(format, producer, text string) bool {
	if format != d.Match.Format {
		return false
	}

	if d.Match.Producer != "" && !regexp.MustCompile(d.Match.Producer).MatchString(producer) {
		return false
	}

	head := first(text)

	for _, p := range d.Match.Contains {
		if !strings.Contains(head, normalize(p)) {
			return false
		}
	}

	for _, p := range d.Match.Lacks {
		if strings.Contains(head, normalize(p)) {
			return false
		}
	}

	return true
}

//go:embed declarations/*.json
var embedded embed.FS

// builtin is every built-in declaration, read once. One that cannot be read
// stops the program at its start: it was reviewed and merged, and its test
// says what is wrong with it long before then.
var builtin = func() []Declaration {
	out, err := load(embedded)
	if err != nil {
		panic(err)
	}

	return out
}()

// load reads the declarations in a directory, each named for its id and
// each with a fixture.
func load(fsys fs.FS) ([]Declaration, error) {
	names, err := fs.Glob(fsys, "declarations/*.json")
	if err != nil {
		return nil, err
	}

	var out []Declaration

	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}

		d, err := Parse(data)

		switch {
		case err != nil:
			return nil, fmt.Errorf("%s: %w", name, err)
		case d.ID+".json" != path.Base(name):
			return nil, fmt.Errorf("%s: its id is %q", name, d.ID)
		case d.Fixture == "":
			return nil, fmt.Errorf("%s: it names no fixture to be tested on", name)
		}

		out = append(out, d)
	}

	return out, nil
}

// Builtin is every built-in declaration, by id.
func Builtin() []Declaration {
	return slices.Clone(builtin)
}

// Recognize is the built-in declarations a document matches. One is the
// layout it is in; none is a layout nobody has declared; more than one is
// two declarations that overlap, which their fixtures' tests are there to
// prevent, and is read as if none matched.
func Recognize(format, producer, text string) []Declaration {
	var out []Declaration

	for _, d := range builtin {
		if d.Matches(format, producer, text) {
			out = append(out, d)
		}
	}

	return out
}
