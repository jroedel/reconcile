package pdftext_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/pdftext"
	"github.com/jroedel/reconcile/foundation/pdftext/pdftexttest"
)

func needPoppler(t *testing.T) {
	t.Helper()

	if !pdftext.Available() {
		t.Skip("pdftotext is not installed here; CI installs it")
	}
}

// A table drawn at positions comes back as lines with its columns apart,
// pages split by form feeds, and the typographic marks a printout uses
// intact.
func TestExtractKeepsTheLayout(t *testing.T) {
	needPoppler(t)

	var p1, p2 pdftexttest.Page

	p1 = append(p1, pdftexttest.Row(60, 50, "Date", 150, "Description", 450, "Amount")...)
	for i, payee := range []string{"Corner Hardware", "Café Lumen", "Parish Office Supply"} {
		p1 = append(p1, pdftexttest.Row(80+float64(i)*20, 50, "09/0"+string(rune('1'+i))+"/2026", 150, payee, 450, "12.50")...)
	}

	p2 = append(p2, pdftexttest.Row(60, 150, "Long description cut sho…")...)

	text, err := pdftext.Extract(t.Context(), pdftexttest.Draw(p1, p2))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	pages := strings.Split(text, "\f")
	if len(pages) < 2 {
		t.Fatalf("got %d pages, want 2:\n%s", len(pages), text)
	}

	for _, want := range []string{"Corner Hardware", "Café Lumen", "12.50"} {
		if !strings.Contains(pages[0], want) {
			t.Errorf("page 1 lacks %q:\n%s", want, pages[0])
		}
	}

	if !strings.Contains(pages[1], "cut sho…") {
		t.Errorf("page 2 lost the ellipsis:\n%s", pages[1])
	}

	for line := range strings.SplitSeq(pages[0], "\n") {
		if strings.Contains(line, "Corner Hardware") && !strings.Contains(line, "  12.50") {
			t.Errorf("the amount is not apart from the description: %q", line)
		}
	}
}

// A page with nothing on it is what a scan looks like to pdftotext.
func TestExtractSaysAScanHasNoText(t *testing.T) {
	needPoppler(t)

	_, err := pdftext.Extract(t.Context(), pdftexttest.Draw(pdftexttest.Page{}, pdftexttest.Page{}))
	if !errors.Is(err, pdftext.ErrNoText) {
		t.Errorf("Extract = %v, want ErrNoText", err)
	}
}

func TestExtractRefusesWhatIsNotAPDF(t *testing.T) {
	needPoppler(t)

	_, err := pdftext.Extract(t.Context(), []byte("Date,Description,Amount\n"))
	if !errors.Is(err, pdftext.ErrUnreadable) {
		t.Errorf("Extract = %v, want ErrUnreadable", err)
	}
}

// Without poppler a PDF is refused with a reason a page can say, rather
// than an exec error.
func TestExtractWithoutPoppler(t *testing.T) {
	t.Setenv("PATH", "")
	pdftext.SetCandidates(t, nil)

	if pdftext.Available() {
		t.Fatal("Available with no PATH and no candidates")
	}

	if _, err := pdftext.Extract(t.Context(), []byte("%PDF-1.4")); !errors.Is(err, pdftext.ErrUnavailable) {
		t.Errorf("Extract = %v, want ErrUnavailable", err)
	}
}
