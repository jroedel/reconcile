package types

import (
	"fmt"
	"strings"
	"time"
)

// Date is a day on the calendar, with no time and no zone: the day an
// account was opened, the day a project starts, the day a bank says a
// transaction posted.
//
// Its own type rather than a time.Time, because a time.Time is an instant,
// and an instant turned into a day depends on where you stand. A charge
// posted on the 1st is on the 1st for everybody, and a midnight UTC that
// renders as the 31st in Chicago is how a month's reconciliation ends up a
// transaction short.
//
// Stored as its ISO text, YYYY-MM-DD, which sorts as dates do; CLAUDE.md's
// "timestamps are integers" is about instants, and this is not one.
type Date struct {
	s string
}

const dateLayout = "2006-01-02"

// ParseDate reads YYYY-MM-DD, which is what an <input type="date"> sends.
// Empty is the zero Date and not an error: every date here is optional
// until something says otherwise.
func ParseDate(s string) (Date, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Date{}, nil
	}

	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("%q is not a date written YYYY-MM-DD", s)
	}

	return Date{s: t.Format(dateLayout)}, nil
}

// DateOf is the calendar day of t where t is.
func DateOf(t time.Time) Date { return Date{s: t.Format(dateLayout)} }

func (d Date) String() string { return d.s }

// Zero reports whether there is no date.
func (d Date) Zero() bool { return d.s == "" }

// Before reports whether d is an earlier day than e. Both must be set.
func (d Date) Before(e Date) bool { return d.s < e.s }

// AddDays is the day n days after d, or before it for a negative n. The
// zero Date stays zero.
func (d Date) AddDays(n int) Date {
	t, err := time.Parse(dateLayout, d.s)
	if err != nil {
		return Date{}
	}

	return DateOf(t.AddDate(0, 0, n))
}
