// Package pdftexttest draws PDFs for tests: pages of text at positions, in
// the one font every PDF reader carries.
//
// No real statement may enter this repository, and a PDF checked in as a
// fixture is a file nobody reviews. So tests draw theirs when they run, from
// lines a reader can see in the test: invented payees and amounts, laid out
// the way a bank's statement or a browser's printout lays them out, and read
// back by the same pdftotext the server runs.
package pdftexttest

import (
	"bytes"
	"fmt"
	"strings"
)

// Text is one run of text: X points from the left edge and Y from the top,
// on a US Letter page (612 by 792 points).
type Text struct {
	X, Y float64
	S    string
}

// Page is what one page says.
type Page []Text

// Row is a line of a page at y: each string at its x, in order. A
// convenience for drawing a table, which is most of a statement.
func Row(y float64, at ...any) []Text {
	var out []Text

	for i := 0; i+1 < len(at); i += 2 {
		out = append(out, Text{X: toFloat(at[i]), Y: y, S: at[i+1].(string)})
	}

	return out
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}

	panic(fmt.Sprintf("pdftexttest: %v is not a position", v))
}

// Producer is what every drawn PDF's metadata says wrote it.
const Producer = "pdftexttest 1.0"

// Draw makes a PDF of the pages, in 9-point Helvetica.
//
// The smallest file a PDF reader accepts: a catalog, a page tree, one font,
// and a page and its content stream for each page, with the cross-reference
// table that says where each object starts -- and the metadata's producer,
// which is how a layout is told apart (pdftext.Producer).
func Draw(pages ...Page) []byte {
	var objects []string

	add := func(s string) int {
		objects = append(objects, s)

		return len(objects)
	}

	catalog := add("") // filled in once the page tree's number is known
	tree := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	info := add("<< /Producer (" + Producer + ") >>")

	var kids []string

	for _, p := range pages {
		var content strings.Builder

		for _, t := range p {
			fmt.Fprintf(&content, "BT /F1 9 Tf %.1f %.1f Td (%s) Tj ET\n", t.X, 792-t.Y, winAnsi(t.S))
		}

		stream := add(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()))
		page := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			tree, font, stream))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}

	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", tree)
	objects[tree-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))

	var buf bytes.Buffer

	buf.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)

	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}

	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, catalog, info, xref)

	return buf.Bytes()
}

// winAnsi writes s in the font's encoding, as the inside of a PDF string:
// Latin-1 as itself, the few typographic marks a statement uses at their
// places in Windows-1252, and the string's own delimiters escaped.
func winAnsi(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x80:
			b.WriteRune(r)
		case r == '…':
			b.WriteString(`\205`)
		case r == '–':
			b.WriteString(`\226`)
		case r == '—':
			b.WriteString(`\227`)
		case r == '’':
			b.WriteString(`\222`)
		case r >= 0xA0 && r <= 0xFF:
			fmt.Fprintf(&b, `\%03o`, r)
		default:
			b.WriteByte('?')
		}
	}

	return b.String()
}
