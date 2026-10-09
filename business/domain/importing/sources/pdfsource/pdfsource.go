// Package pdfsource reads a statement from the text of a PDF: a bank's
// statement as it composed it, or a bank's page of transactions a browser
// printed to PDF (docs/pdf-statements.md).
//
// It is one reader for every bank, not one per bank, and it reads the text
// pdftotext laid out (foundation/pdftext), never the PDF. What every such
// document shares is the table: a row starts with a date and ends with one
// or two amounts, set apart from what is before them by a run of spaces.
// Around the table is everything else, and most of this package is telling
// the two apart:
//
//   - Page furniture: what a page repeats at its top or foot -- the site's
//     name, when it was printed, the page's address, "Page 2 of 5" -- and
//     the column headings. Dropped. A printed page's address can carry the
//     account's identifier, which must never reach the ledger.
//   - Continuations: a line with no date and no amount, at the description's
//     column, continues the row above it. That is a long description
//     wrapped, and also a printout that broke a row across a page and put
//     its description at the top of the next one.
//   - What the document states about itself: the opening and closing
//     balance, or the total of its rows, which the ledger checks the rows
//     against (ledgerbus.verify).
//
// When a row prints an amount and the balance after it, the amount's sign
// is taken from the balances, never from where the figure sat: a debit
// column and a credit column are only whitespace once the text is out of
// the PDF. eumaeus' statement reader works the same way, for the same
// reason.
//
// Like the other sources it knows nothing of accounts: whether the
// document's signs are the bank's way round or the account holder's is the
// ledger's to decide, from the account's kind (ledgerbus.Options.Invert).
package pdfsource

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// ErrNoRows is a document in which no transaction could be found: not a
// statement, or a statement laid out in a way this cannot read.
var ErrNoRows = errors.New("no transaction could be found in the PDF")

// maxContinuations is how many lines may continue one row. A wrapped
// description is two or three lines; more is a paragraph of the bank's that
// happens to start where descriptions do.
const maxContinuations = 3

// Read reads the text of a statement PDF into records, with the balances
// and the total it states.
func Read(text string) (importbus.Result, error) {
	doc := split(text)
	doc.dayFirst = dayFirst(doc.lines)
	start, end, stated := period(doc.lines)
	doc.start, doc.end = start, end
	doc.furniture = furniture(doc.pages)

	r := reader{doc: doc}
	if stated {
		r.res.Start, r.res.End = start, end
	}
	r.read()

	if len(r.res.Records) == 0 {
		return r.res, ErrNoRows
	}

	fromBalances(r.res.Records, r.cols, r.res.Opening)

	return r.res, nil
}

// --- the document -----------------------------------------------------------

// line is one line of the text, with where it is.
type line struct {
	page, n int
	text    string
}

type document struct {
	lines []line
	pages [][]line

	dayFirst   bool
	start, end time.Time
	furniture  map[string]bool
}

// split cuts the text into pages at pdftotext's form feeds, and the pages
// into lines.
func split(text string) document {
	var doc document

	n := 0

	for i, page := range strings.Split(text, "\f") {
		var lines []line

		for s := range strings.SplitSeq(page, "\n") {
			n++
			lines = append(lines, line{page: i + 1, n: n, text: strings.TrimRight(s, " \t\r")})
		}

		// pdftotext ends with a form feed, which makes an empty last page.
		if strings.TrimSpace(page) == "" {
			continue
		}

		doc.pages = append(doc.pages, lines)
		doc.lines = append(doc.lines, lines...)
	}

	return doc
}

// cell is a run of text set apart from the rest of its line by two spaces
// or more, and the column it starts at, counted in characters.
type cell struct {
	col int
	s   string
}

var gap = regexp.MustCompile(`\s{2,}`)

// cells cuts a line into its runs.
func cells(s string, offset int) []cell {
	var out []cell

	at := 0

	for _, sep := range append(gap.FindAllStringIndex(s, -1), []int{len(s), len(s)}) {
		if part := s[at:sep[0]]; strings.TrimSpace(part) != "" {
			lead := len(part) - len(strings.TrimLeft(part, " \t"))
			out = append(out, cell{col: offset + utf8.RuneCountInString(s[:at+lead]), s: strings.TrimSpace(part)})
		}

		at = sep[1]
	}

	return out
}

// indent is the column a line's text starts at.
func indent(s string) int {
	return utf8.RuneCountInString(s) - utf8.RuneCountInString(strings.TrimLeft(s, " \t"))
}

// --- page furniture -----------------------------------------------------------

// always is furniture wherever it is: a page count, an address, and a
// table's "continued".
var always = regexp.MustCompile(`(?i)(\bpage \d+ of \d+\b|^page \d+$|https?://|\bwww\.|^\(?continued\b)`)

// furniture is the lines a document repeats on its pages' edges, by their
// shape: the first and last few lines of each page that are not rows, with
// every number taken out, so that "Page 1" and "Page 2", or a header that
// carries the time it was printed, count as one line. A shape on the edge
// of two pages or more is furniture.
//
// Only the edges, and only lines that are not rows, because a row's shape
// repeats too: "Sep 30, 2026  Corner Hardware  $3.78" and the same shop on
// another day are one shape.
func furniture(pages [][]line) map[string]bool {
	seen := map[string]int{}

	for _, page := range pages {
		var text []string

		for _, l := range page {
			if strings.TrimSpace(l.text) != "" {
				text = append(text, l.text)
			}
		}

		edges := map[string]bool{}

		for i, s := range text {
			if i >= 3 && i < len(text)-3 {
				continue
			}

			if _, _, ok := leadingDate(strings.TrimSpace(s), false); ok {
				if _, figs := figures(s); len(figs) > 0 {
					continue
				}
			}

			edges[shape(s)] = true
		}

		for k := range edges {
			seen[k]++
		}
	}

	out := map[string]bool{}

	for k, n := range seen {
		if n >= 2 {
			out[k] = true
		}
	}

	return out
}

var digits = regexp.MustCompile(`\d+`)

func shape(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(digits.ReplaceAllString(s, "#")), " "))
}

func (d document) isFurniture(s string) bool {
	return always.MatchString(strings.TrimSpace(s)) || len(d.pages) > 1 && d.furniture[shape(s)]
}

// --- reading ------------------------------------------------------------------

// table is what a line of column headings said: the headings between the
// date and the amounts, and which of them is a person rather than a
// description.
type table struct {
	middle []heading
}

type heading struct {
	col    int
	person bool
}

// reader walks the document's lines once, carrying what it learned of the
// table from page to page.
type reader struct {
	doc document
	res importbus.Result

	// table is the last line of headings, and cols where its middle
	// columns are on the current page.
	table *table
	tcols []int

	// descCol is where descriptions start on the current page.
	descCol int

	// open is the row the next lines may continue, and how many have.
	open      int
	continued int

	// cols is each record's amount column, for fromBalances.
	cols []int
}

func (r *reader) read() {
	r.open = -1

	for _, page := range r.doc.pages {
		r.learn(page)

		for _, l := range page {
			r.line(l)
		}
	}
}

// row is a line that starts with a date and ends with amounts, cut up.
type row struct {
	date    day
	middle  []cell
	figures []figure
}

// parseRow reads a line as a row, if it is one.
func (r *reader) parseRow(s string) (row, bool) {
	trimmed := strings.TrimLeft(s, " \t")
	offset := indent(s)

	d, rest, ok := leadingDate(trimmed, r.doc.dayFirst)
	if !ok {
		return row{}, false
	}

	// A second date is the posting date beside the date of the sale. The
	// posting date is the one a bank's CSV and OFX carry, so it is the one
	// that finds the same charge already imported from either.
	if d2, rest2, ok := leadingDate(strings.TrimLeft(rest, " \t"), r.doc.dayFirst); ok {
		d, rest = d2, rest2
	}

	body, figs := figures(rest)
	if len(figs) == 0 {
		return row{}, false
	}

	at := offset + utf8.RuneCountInString(trimmed) - utf8.RuneCountInString(rest)

	return row{date: d, middle: cells(body, at), figures: figs}, true
}

// learn reads a page before it is walked, for where its columns are: where
// descriptions start, and where each of the table's middle columns is,
// from the rows that fill every one of them. A printout shifts its columns
// from page to page, and a description continued at the top of a page is
// only recognizable by that page's columns.
func (r *reader) learn(page []line) {
	var starts []int

	full := map[int][]int{}

	for _, l := range page {
		if h, ok := headings(l.text); ok {
			r.table = &h
			r.tcols = nil

			for _, m := range h.middle {
				r.tcols = append(r.tcols, m.col)
			}

			continue
		}

		rw, ok := r.parseRow(l.text)
		if !ok || len(rw.middle) == 0 {
			continue
		}

		starts = append(starts, rw.middle[0].col)

		if r.table != nil && len(rw.middle) == len(r.table.middle) {
			for i, c := range rw.middle {
				full[i] = append(full[i], c.col)
			}
		}
	}

	if r.table != nil && len(full) == len(r.table.middle) {
		for i := range r.tcols {
			r.tcols[i] = median(full[i])
		}
	}

	switch {
	case r.table != nil:
		for i, h := range r.table.middle {
			if !h.person {
				r.descCol = r.tcols[i]

				break
			}
		}
	case len(starts) > 0:
		r.descCol = median(starts)
	}
}

func median(xs []int) int {
	s := slices.Clone(xs)
	slices.Sort(s)

	return s[len(s)/2]
}

// line takes one line of the document.
func (r *reader) line(l line) {
	s := l.text

	if strings.TrimSpace(s) == "" {
		return
	}

	if r.doc.isFurniture(s) {
		return
	}

	if _, ok := headings(s); ok {
		r.open = -1

		return
	}

	if rw, ok := r.parseRow(s); ok {
		r.row(l, rw)

		return
	}

	if _, figs := figures(s); len(figs) > 0 {
		r.stated(s)
		r.open = -1

		return
	}

	if _, _, ok := leadingDate(strings.TrimSpace(s), r.doc.dayFirst); ok {
		r.open = -1

		return
	}

	r.continuation(s)
}

// markers are the descriptions of rows that state a balance rather than
// move money.
var (
	openingLabel = regexp.MustCompile(`(?i)\b(previous|beginning|opening|starting) (statement )?balance\b|\bbalance (brought )?forward\b`)
	closingLabel = regexp.MustCompile(`(?i)\b(new|ending|closing) (statement )?balance\b`)
	totalLabel   = regexp.MustCompile(`(?i)\b(total|net) activity\b`)
)

// row takes a line that is a row.
func (r *reader) row(l line, rw row) {
	r.open = -1

	date, ok := r.doc.resolve(rw.date)
	if !ok || len(rw.figures) > 2 {
		r.res.Skipped++
		r.res.Warn(importbus.Warning{Page: l.page, Problem: importbus.Unclear, Value: clip(strings.TrimSpace(l.text))})

		return
	}

	desc, memo := r.describe(rw.middle)

	// A row that states a balance: kept as the balance it states, and not
	// imported, since it moves nothing.
	last := rw.figures[len(rw.figures)-1].amount

	switch {
	case openingLabel.MatchString(desc):
		if !r.res.Opening.Known {
			r.res.Opening = importbus.Balance{Amount: last, AsOf: date, Known: true}
		}

		return
	case closingLabel.MatchString(desc):
		r.res.Closing = importbus.Balance{Amount: last, AsOf: date, Known: true}

		return
	}

	rec := importbus.Record{
		Line:        l.n,
		Page:        l.page,
		Date:        date,
		Description: desc,
		Amount:      rw.figures[0].amount,
		Memo:        memo,
		Holder:      memo,
	}

	if len(rw.figures) == 2 {
		rec.Balance, rec.HasBalance = rw.figures[1].amount, true
	}

	r.res.Records = append(r.res.Records, rec)
	r.cols = append(r.cols, rw.figures[0].col)
	r.open, r.continued = len(r.res.Records)-1, 0
}

// describe sorts a row's middle runs into its description and its memo: a
// run under a heading that is a person goes to the memo. Without headings,
// or with only one, all of it is the description.
func (r *reader) describe(middle []cell) (string, string) {
	var desc, memo []string

	if r.table == nil || len(r.table.middle) < 2 {
		for _, c := range middle {
			desc = append(desc, c.s)
		}

		return strings.Join(desc, " "), ""
	}

	for _, c := range middle {
		best := 0

		for i, col := range r.tcols {
			if abs(col-c.col) < abs(r.tcols[best]-c.col) {
				best = i
			}
		}

		if r.table.middle[best].person {
			memo = append(memo, c.s)
		} else {
			desc = append(desc, c.s)
		}
	}

	return strings.Join(desc, " "), strings.Join(memo, " ")
}

func abs(n int) int { return max(n, -n) }

// continuation adds a line to the open row's description, if it is at the
// description's column.
func (r *reader) continuation(s string) {
	if r.open < 0 || r.continued >= maxContinuations {
		return
	}

	if abs(indent(s)-r.descCol) > 3 {
		r.open = -1

		return
	}

	rec := &r.res.Records[r.open]
	rec.Description = strings.TrimSpace(rec.Description + " " + strings.Join(strings.Fields(s), " "))
	r.continued++
}

// stated reads what a line outside the table says the document comes to:
// a label, and the first amount after it. The first, not the last, because
// a summary box puts several on a line -- "New balance $1,234.56  Minimum
// payment $25.00" -- and the one after the label is the label's.
func (r *reader) stated(s string) {
	at := func(re *regexp.Regexp) (money.Amount, bool) {
		loc := re.FindStringIndex(s)
		if loc == nil {
			return 0, false
		}

		m := anyFigure.FindStringSubmatch(s[loc[1]:])
		if m == nil {
			return 0, false
		}

		a, err := parseFigure(m[1])

		return a, err == nil
	}

	if a, ok := at(openingLabel); ok && !r.res.Opening.Known {
		r.res.Opening = importbus.Balance{Amount: a, AsOf: r.doc.start, Known: true}
	}

	if a, ok := at(closingLabel); ok && !r.res.Closing.Known {
		r.res.Closing = importbus.Balance{Amount: a, AsOf: r.doc.end, Known: true}
	}

	if a, ok := at(totalLabel); ok && !r.res.Total.Known {
		r.res.Total = importbus.Total{Amount: a, Known: true}
	}
}

// clip shortens a line for a warning.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= 80 {
		return s
	}

	return string([]rune(s)[:79]) + "…"
}

// --- headings -----------------------------------------------------------------

var (
	dateHeading   = regexp.MustCompile(`(?i)\bdate\b`)
	descHeading   = regexp.MustCompile(`(?i)^(transaction )?(description|details|payee|merchant|narrative|transaction)\b`)
	amountHeading = regexp.MustCompile(`(?i)\b(amount|debits?|credits?|withdrawals?|deposits?|charges?|payments?|balance|money)\b`)
	personHeading = regexp.MustCompile(`(?i)^(name|card ?member|card ?holder|member|employee|user|spender)\b`)
)

// headings reads a line of column headings: a date, a description, and
// the amounts, each apart.
func headings(s string) (table, bool) {
	cs := cells(s, 0)
	if len(cs) < 3 || !dateHeading.MatchString(cs[0].s) {
		return table{}, false
	}

	var (
		t        table
		hasDesc  bool
		amounts  bool
		pastDate bool
	)

	for _, c := range cs {
		switch {
		case !pastDate && dateHeading.MatchString(c.s):
			continue
		case amountHeading.MatchString(c.s) && !descHeading.MatchString(c.s):
			amounts = true
		case amounts:
		default:
			pastDate = true
			hasDesc = hasDesc || descHeading.MatchString(c.s)
			t.middle = append(t.middle, heading{col: c.col, person: personHeading.MatchString(c.s)})
		}
	}

	return t, hasDesc && amounts
}
