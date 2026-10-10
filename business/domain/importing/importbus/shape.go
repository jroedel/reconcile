package importbus

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
)

// Structure is the words a file uses for its own structure, as against its
// contents: what tells one bank's layout from another's, and nothing about
// whose money is in it (docs/shapes.md, 1). Each reader fills it with the
// words its own rules recognized -- a heading it matched, a label it read
// a balance after -- and never a whole line, so that a cardholder's name
// printed beside a heading cannot come with it. Every digit is taken out.
type Structure struct {
	// Frame is what every file of the layout says, whatever its month: its
	// column headings, its labels for the balances, totals and account
	// number, the form of its period. The layout's signature is made of
	// these.
	Frame []string

	// Sections is what some months say and others do not -- a heading for
	// the checks paid, in a month with checks -- kept for whoever teaches
	// the layout, and no part of its signature, so that a month without
	// checks is the same layout as one with.
	Sections []string
}

// Shape is what a file was recognized as.
type Shape struct {
	// Format is "csv", "ofx" or "pdf".
	Format string

	// Producer is the program that wrote a PDF, as its metadata says, with
	// its version's digits taken out: a bank's composition engine, or the
	// browser that printed a page.
	Producer string

	Structure Structure

	// Signature is the layout's identity, from the three above (Sign).
	Signature string

	// Declared is the name of the declaration that describes the layout,
	// or empty for a layout none does: read by the general reader, and a
	// sighting (shapebus).
	Declared string
}

// New reports a shape no declaration describes.
func (s Shape) New() bool { return s.Declared == "" }

// Sign makes a shape's signature: a hash of its format, its producer and
// its frame, in no particular order. Sixteen hex digits, which is plenty
// for the layouts one site will see, and short enough to read aloud.
func Sign(format, producer string, s Structure) string {
	frame := slices.Clone(s.Frame)
	slices.Sort(frame)
	frame = slices.Compact(frame)

	h := sha256.Sum256([]byte(format + "\n" + producer + "\n" + strings.Join(frame, "\n")))

	return hex.EncodeToString(h[:8])
}

var (
	digitRuns = regexp.MustCompile(`\d+`)
	spaceRuns = regexp.MustCompile(`\s+`)
)

// Word is how one structural word is kept: lower case, every run of digits
// a "#", its spaces collapsed, and cut at 80 characters. Digits go because
// a layout's words carry none that matter and an account's number is all
// digits; case and spacing go because a bank changes them without changing
// anything else.
func Word(s string) string {
	s = digitRuns.ReplaceAllString(strings.ToLower(s), "#")
	s = strings.TrimSpace(spaceRuns.ReplaceAllString(s, " "))

	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}

	return s
}

// Add puts a word in a list once, as Word keeps it.
func Add(list *[]string, s string) {
	if w := Word(s); w != "" && !slices.Contains(*list, w) {
		*list = append(*list, w)
	}
}
