package reportbus

import (
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/types"
)

// Period is the months a report is run for: whole calendar months, From to
// To, both included, written as a person and an address write them
// ("2026-07"). A report keeps no period of its own (docs/reports.md); the
// page chooses one each time it is run, and this is that choice.
//
// The zero Period is no choice at all, which a style reads as its default.
// All is every month there is money in, which is how a house sees the
// history of a card it stopped using two years ago.
type Period struct {
	From, To string
	All      bool
}

// ErrPeriod is a period an address names that is not one: a month that is
// not a month, or one that ends before it starts.
var ErrPeriod = errors.New("choose a first and last month, the first not after the last")

// MaxMonths is the most months a period may span: forty years, past which
// a month typed wrong is likelier than a history that long, and the table
// of a month to a column would be unreadable anyway.
const MaxMonths = 480

// NewPeriod reads a period from two months as an address writes them.
func NewPeriod(from, to string) (Period, error) {
	f, err := monthStart(from)
	if err != nil {
		return Period{}, ErrPeriod
	}

	t, err := monthStart(to)
	if err != nil || t.Before(f) {
		return Period{}, ErrPeriod
	}

	p := Period{From: from, To: to}
	if len(p.Months()) > MaxMonths {
		return Period{}, ErrPeriod
	}

	return p, nil
}

// Zero reports whether no period was chosen.
func (p Period) Zero() bool { return !p.All && p.From == "" }

// Months is each month of it, oldest first; none for All, whose months are
// the ones that turn out to have money in them.
func (p Period) Months() []string {
	if p.All || p.From == "" {
		return nil
	}

	f, _ := monthStart(p.From)
	t, _ := monthStart(p.To)

	var out []string
	for m := f; !t.Before(m); m = m.AddDate(0, 1, 0) {
		out = append(out, m.Format("2006-01"))
	}

	return out
}

// span is the days of the period, from the first to before the last: what
// the ledger is asked for. All is every day there is.
func (p Period) span() (from, to types.Date) {
	if p.All || p.From == "" {
		end, _ := types.ParseDate("9999-12-31")

		return types.Date{}, end
	}

	f, _ := monthStart(p.From)
	t, _ := monthStart(p.To)

	return types.DateOf(f), types.DateOf(t.AddDate(0, 1, 0))
}

// Shifted is the period of the same length just before (n < 0) or after
// (n > 0) this one, n lengths away: the "previous" and "next" links. All
// has none, and is returned as it is.
func (p Period) Shifted(n int) Period {
	months := len(p.Months())
	if months == 0 {
		return p
	}

	f, _ := monthStart(p.From)
	t, _ := monthStart(p.To)

	return Period{
		From: f.AddDate(0, n*months, 0).Format("2006-01"),
		To:   t.AddDate(0, n*months, 0).Format("2006-01"),
	}
}

// Preset is a period a page offers by name.
type Preset struct {
	Key    string // "last-month", "year-to-date", "last-12-months", "all"
	Period Period
}

// The presets' keys, which a page words.
const (
	LastMonth    = "last-month"
	YearToDate   = "year-to-date"
	LastTwelve   = "last-12-months"
	EveryMonth   = "all"
	defaultShown = YearToDate
)

// Presets is the periods a page offers, as of today, for books whose year
// starts in fiscalStart (1 for January). Each ends with the last month that
// is over: a house meets about the month just gone, and a month still
// running would read as a bad one.
//
// The year to date is the budget year's months up to that one: in
// January, with a year that starts in January, it is the whole of the year
// just ended, which is the report a house wants then.
func Presets(today types.Date, fiscalStart int) []Preset {
	t, err := time.Parse("2006-01-02", today.String())
	if err != nil {
		return nil
	}

	if fiscalStart < 1 || fiscalStart > 12 {
		fiscalStart = 1
	}

	last := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)

	year := last.Year()
	if int(last.Month()) < fiscalStart {
		year--
	}

	start := time.Date(year, time.Month(fiscalStart), 1, 0, 0, 0, 0, time.UTC)

	month := func(m time.Time) string { return m.Format("2006-01") }

	return []Preset{
		{Key: LastMonth, Period: Period{From: month(last), To: month(last)}},
		{Key: YearToDate, Period: Period{From: month(start), To: month(last)}},
		{Key: LastTwelve, Period: Period{From: month(last.AddDate(0, -11, 0)), To: month(last)}},
		{Key: EveryMonth, Period: Period{All: true}},
	}
}

// defaulted is the period itself, or the default preset when none was
// chosen.
func defaulted(p Period, today types.Date, fiscalStart int) Period {
	if !p.Zero() {
		return p
	}

	for _, pr := range Presets(today, fiscalStart) {
		if pr.Key == defaultShown {
			return pr.Period
		}
	}

	return Period{All: true}
}

// monthStart reads "2026-07" as the first moment of that month.
func monthStart(s string) (time.Time, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a month written YYYY-MM", s)
	}

	return t, nil
}
