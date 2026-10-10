package pdfsource_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/foundation/pdftext"
)

func consolidatedText(t *testing.T) string {
	t.Helper()

	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}

	text, err := pdftext.Extract(t.Context(), pdfsourcetest.Consolidated())
	if err != nil {
		t.Fatal(err)
	}

	return text
}

// What a statement's layout is made of comes back with its rows: the
// labels and headings the reader recognized, in the words it matched, and
// nothing else -- no payee, no account's number, no amount, no digit.
func TestTheWordsOfALayout(t *testing.T) {
	res, err := pdfsource.Read(consolidatedText(t))
	if err != nil {
		t.Fatal(err)
	}

	st := res.Structure

	for _, want := range []string{"period m #, # through m #, #", "account number", "beginning balance", "ending balance", "date | description | amount"} {
		if !slices.Contains(st.Frame, want) {
			t.Errorf("the frame lacks %q: %q", want, st.Frame)
		}
	}

	for _, want := range []string{"deposits", "checks paid", "withdrawals", "daily ending balance", "summary"} {
		if !slices.Contains(st.Sections, want) {
			t.Errorf("the sections lack %q: %q", want, st.Sections)
		}
	}

	leak := regexp.MustCompile(`(?i)\d|example|remote|payroll|primary`)

	for _, w := range slices.Concat(st.Frame, st.Sections) {
		if leak.MatchString(w) {
			t.Errorf("%q is not a word of the layout's", w)
		}
	}

	// Each account's part has its rows and none of the words.
	for _, a := range res.Accounts {
		if len(a.Result.Structure.Frame) > 0 {
			t.Errorf("account %s carries a structure of its own", a.Last4)
		}
	}
}

// A section the general reader does not know by its heading is read by a
// layout that says what it is: here, a statement whose checks paid are
// headed "Cheques cleared". The general reader drops those rows -- they
// start with a number, which is only a row in a section of checks -- and a
// layout that names the heading reads all of them, as the general reader
// reads the statement that calls them checks paid.
func TestALayoutThatNamesASection(t *testing.T) {
	text := consolidatedText(t)
	renamed := strings.ReplaceAll(text, "CHECKS PAID", "CHEQUES CLEARED")

	rows := func(res importbus.Result) []string {
		var out []string

		for _, a := range res.Accounts {
			for _, r := range a.Result.Records {
				out = append(out, fmt.Sprintf("%s %s %s %s", a.Last4, r.Date.Format("01-02"), r.Amount, r.Description))
			}
		}

		return out
	}

	want, err := pdfsource.Read(text)
	if err != nil {
		t.Fatal(err)
	}

	general, err := pdfsource.Read(renamed)
	if err != nil {
		t.Fatal(err)
	}

	l := pdfsource.General
	l.Name = "cheques cleared"
	l.Out = `(?i)\b(withdrawals?|cheques cleared)\b`
	l.Checks = `(?i)\bcheques cleared\b`

	declared, err := pdfsource.ReadAs(renamed, l)
	if err != nil {
		t.Fatal(err)
	}

	if len(rows(general)) >= len(rows(want)) {
		t.Fatalf("the general reader read %d rows of the renamed statement, and %d of the original", len(rows(general)), len(rows(want)))
	}

	if !slices.Equal(rows(declared), rows(want)) {
		t.Errorf("read by the layout:\n%s\nwant:\n%s", strings.Join(rows(declared), "\n"), strings.Join(rows(want), "\n"))
	}

	if !slices.Contains(declared.Structure.Sections, "cheques cleared") {
		t.Errorf("sections %q", declared.Structure.Sections)
	}

	// The frame is the same, so the layout is: a section's heading is not
	// part of what it is recognized by.
	if importbus.Sign("pdf", "", declared.Structure) != importbus.Sign("pdf", "", want.Structure) {
		t.Errorf("frames %q and %q", declared.Structure.Frame, want.Structure.Frame)
	}

	l.Checks = `(?i)(cheques`
	if _, err := pdfsource.ReadAs(renamed, l); err == nil || !strings.Contains(err.Error(), "checks pattern") {
		t.Errorf("a pattern that is not one: %v", err)
	}
}
