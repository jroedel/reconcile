package pdfsource

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// day is a date as a document printed it: the year is zero when it printed
// none, as a statement's rows often do ("09/30", "Sep 30").
type day struct {
	y, d int
	m    time.Month
}

var months = map[string]time.Month{
	"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3,
	"apr": 4, "april": 4, "may": 5, "jun": 6, "june": 6, "jul": 7, "july": 7,
	"aug": 8, "august": 8, "sep": 9, "sept": 9, "september": 9, "oct": 10,
	"october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12,
}

// The shapes of a date at the start of a line. A slash date is read
// month-first unless the document shows otherwise (dayFirst). A dot is not
// a separator here, because a line that starts "12.50" is an amount.
var (
	isoDate   = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:\s|$)`)
	slashDate = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})(?:/(\d{4}|\d{2}))?(?:\s|$)`)
	dashDate  = regexp.MustCompile(`^(\d{1,2})-(\d{1,2})-(\d{4})(?:\s|$)`)
	namedDate = regexp.MustCompile(`^([A-Za-z]{3,9})\.?\s+(\d{1,2})(?:,?\s+(\d{4}))?(?:\s|,|$)`)
	dayName   = regexp.MustCompile(`^(\d{1,2})[\s-]([A-Za-z]{3,9})\.?(?:[\s-](\d{4}|\d{2}))?(?:\s|$)`)
)

// leadingDate reads a date at the start of s, and returns what follows it.
func leadingDate(s string, dayFirst bool) (day, string, bool) {
	num := func(t string) int {
		n, _ := strconv.Atoi(t)

		return n
	}

	year := func(t string) int {
		switch y := num(t); {
		case t == "":
			return 0
		case y < 100:
			return 2000 + y
		default:
			return y
		}
	}

	var (
		d   day
		end int
	)

	switch {
	case isoDate.MatchString(s):
		m := isoDate.FindStringSubmatch(s)
		d, end = day{y: num(m[1]), m: time.Month(num(m[2])), d: num(m[3])}, len(m[0])
	case slashDate.MatchString(s):
		m := slashDate.FindStringSubmatch(s)
		a, b := num(m[1]), num(m[2])

		if dayFirst {
			a, b = b, a
		}

		d, end = day{y: year(m[3]), m: time.Month(a), d: b}, len(m[0])
	case dashDate.MatchString(s):
		m := dashDate.FindStringSubmatch(s)
		a, b := num(m[1]), num(m[2])

		if dayFirst {
			a, b = b, a
		}

		d, end = day{y: year(m[3]), m: time.Month(a), d: b}, len(m[0])
	case namedDate.MatchString(s):
		m := namedDate.FindStringSubmatch(s)

		mon, ok := months[strings.ToLower(m[1])]
		if !ok {
			return day{}, "", false
		}

		d, end = day{y: year(m[3]), m: mon, d: num(m[2])}, len(m[0])
	case dayName.MatchString(s):
		m := dayName.FindStringSubmatch(s)

		mon, ok := months[strings.ToLower(m[2])]
		if !ok {
			return day{}, "", false
		}

		d, end = day{y: year(m[3]), m: mon, d: num(m[1])}, len(m[0])
	default:
		return day{}, "", false
	}

	if d.m < 1 || d.m > 12 || d.d < 1 || d.d > 31 {
		return day{}, "", false
	}

	// The match took the space after the date with it; give it back, so the
	// rest keeps its columns.
	if end > 0 && end <= len(s) && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == ',') {
		end--
	}

	return d, s[end:], true
}

// dayFirst reports whether the document writes its slash dates day first:
// whether any row's first number could only be a day. Decided once for the
// whole document, like a CSV's date format, so that the 3rd of April is
// not read as the 4th of March for twelve days of the month.
func dayFirst(lines []line) bool {
	for _, l := range lines {
		m := slashDate.FindStringSubmatch(strings.TrimSpace(l.text))
		if m == nil {
			continue
		}

		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])

		if a > 12 && b <= 12 {
			return true
		}
	}

	return false
}

// resolve gives a day its year and makes it a time: a year it printed, or
// the one that puts it in the document's period, or failing a period the
// year the document's dates mostly have.
// before reports whether one date is before another, once both are
// resolved; a date that cannot be is before nothing.
func (d document) before(a, b day) bool {
	ta, okA := d.resolve(a)
	tb, okB := d.resolve(b)

	return okA && okB && ta.Before(tb)
}

func (d document) resolve(x day) (time.Time, bool) {
	in := func(y int) (time.Time, bool) {
		t := time.Date(y, x.m, x.d, 0, 0, 0, 0, time.UTC)

		return t, t.Month() == x.m
	}

	if x.y != 0 {
		return in(x.y)
	}

	if d.end.IsZero() {
		return time.Time{}, false
	}

	// A statement for December into January prints both years' days without
	// either year; the one inside the period (with a margin for a row
	// posted a little outside it) is the one meant.
	for _, y := range []int{d.end.Year(), d.end.Year() - 1} {
		t, ok := in(y)
		if ok && !t.Before(d.start.AddDate(0, 0, -40)) && !t.After(d.end.AddDate(0, 0, 10)) {
			return t, true
		}
	}

	return in(d.end.Year())
}

// fullDate finds dates with a year anywhere in a line.
var fullDate = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}/(?:\d{4}|\d{2})|[A-Za-z]{3,9}\.? \d{1,2},? \d{4}|\d{1,2} [A-Za-z]{3,9}\.? \d{4})\b`)

// connectors are what stands between the two dates of a period.
var connectors = map[string]bool{"to": true, "through": true, "thru": true, "until": true, "-": true, "–": true, "—": true, "a": true, "al": true, "até": true}

// period is the span the document says it covers, from the first line that
// names one: two dates with a year and "to", "through" or a dash between
// them. Its end is also what gives a year to rows that print none. With no
// period stated, the year before the latest date with a year stands in
// for that, and stated is false: it is no period to put on a statement.
//
// form is how the period was written, for the layout's structure: each
// date's letters an "M" and its digits a "#", and what stood between them
// -- "M #, # through M #, #" -- so that every month of one layout writes
// it the same.
func period(lines []line) (start, end time.Time, stated bool, form string) {
	df := dayFirst(lines)

	for _, l := range lines {
		locs := fullDate.FindAllStringIndex(l.text, -1)

		for i := 0; i+1 < len(locs); i++ {
			between := strings.ToLower(strings.TrimSpace(l.text[locs[i][1]:locs[i+1][0]]))
			if !connectors[between] {
				continue
			}

			a, _, ok1 := leadingDate(l.text[locs[i][0]:locs[i][1]], df)
			b, _, ok2 := leadingDate(l.text[locs[i+1][0]:locs[i+1][1]], df)

			if !ok1 || !ok2 {
				continue
			}

			start := time.Date(a.y, a.m, a.d, 0, 0, 0, 0, time.UTC)
			end := time.Date(b.y, b.m, b.d, 0, 0, 0, 0, time.UTC)

			if !end.Before(start) && end.Sub(start) < 400*24*time.Hour {
				form := dateForm(l.text[locs[i][0]:locs[i][1]]) + " " + between + " " + dateForm(l.text[locs[i+1][0]:locs[i+1][1]])

				return start, end, true, form
			}
		}
	}

	// No period stated: the latest date with a year gives the year.
	var latest time.Time

	for _, l := range lines {
		for _, m := range fullDate.FindAllString(l.text, -1) {
			if x, _, ok := leadingDate(m, df); ok {
				if t := time.Date(x.y, x.m, x.d, 0, 0, 0, 0, time.UTC); t.After(latest) {
					latest = t
				}
			}
		}
	}

	if latest.IsZero() {
		return time.Time{}, time.Time{}, false, ""
	}

	return latest.AddDate(-1, 0, 0), latest, false, ""
}

var (
	letterRuns = regexp.MustCompile(`[A-Za-z]+`)
	numberRuns = regexp.MustCompile(`\d+`)
)

// dateForm is a date as its layout writes it, with no date in it.
func dateForm(s string) string {
	return numberRuns.ReplaceAllString(letterRuns.ReplaceAllString(s, "M"), "#")
}
