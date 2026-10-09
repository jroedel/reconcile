package budgetbus

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// An organization's budget year (docs/budgets.md): twelve months from the
// first of the month its owners chose (tenancybus.Org.FiscalStart), named
// by the calendar year it starts in.

// The years a budget may be for: wide enough for any organization's
// history, narrow enough that a typed year is a year.
const (
	FirstYear = 2000
	LastYear  = 2100
)

// YearOf is the budget year a day is in, for a year that starts in month.
func YearOf(day time.Time, month int) int {
	if int(day.Month()) >= month {
		return day.Year()
	}

	return day.Year() - 1
}

// Span is a budget year's days: its first, and the first after it.
func Span(year, month int) (types.Date, types.Date) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)

	return types.DateOf(start), types.DateOf(start.AddDate(1, 0, 0))
}

// Label is how a page names a budget year: "2026" for one that starts in
// January, "2026–27" for one that runs into the next.
func Label(year, month int) string {
	if month <= 1 {
		return strconv.Itoa(year)
	}

	return fmt.Sprintf("%d–%02d", year, (year+1)%100)
}

// pace is how much of a year has gone by on a day, 0 to 100: none before it
// starts, all of it once it has ended.
func pace(day time.Time, year, month int) int {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)

	switch {
	case day.Before(start):
		return 0
	case !day.Before(end):
		return 100
	}

	return int(day.Sub(start) * 100 / end.Sub(start))
}

// org is the organization and the reader's access to it, and the year to
// show: the one asked for, or the one today is in.
func (b *Business) org(ctx context.Context, now time.Time, actor, orgID types.ID, year int) (tenancybus.Org, tenancybus.Access, int, error) {
	o, access, err := b.access.Org(ctx, actor, orgID)
	if err != nil {
		return tenancybus.Org{}, tenancybus.Access{}, 0, err
	}

	if year == 0 {
		year = YearOf(now, o.FiscalStart)
	}

	if year < FirstYear || year > LastYear {
		return tenancybus.Org{}, tenancybus.Access{}, 0, ErrNotFound
	}

	return o, access, year, nil
}

// OrgYear is an organization's budget year beside what its accounts did in
// it, for anyone who may read the organization. Year zero is the year today
// is in.
func (b *Business) OrgYear(ctx context.Context, now time.Time, actor, orgID types.ID, year int) (Form, error) {
	o, access, year, err := b.org(ctx, now, actor, orgID, year)
	if err != nil {
		return Form{}, err
	}

	start, end := Span(year, o.FiscalStart)

	parts, totals, err := b.ledger.OrgPeriod(ctx, actor, orgID, start, end)
	if err != nil {
		return Form{}, err
	}

	lines, err := b.store.Lines(ctx, o.Scope(), year)
	if err != nil {
		return Form{}, err
	}

	cats, err := b.choices(ctx, orgID, parts)
	if err != nil {
		return Form{}, err
	}

	c := Comparison{
		Org: o, Access: access, Year: year, Label: Label(year, o.FiscalStart), Start: start, End: end,
		Pace: pace(now, year, o.FiscalStart), Set: len(lines) > 0,
	}

	if c.Currency = currencyOf(lines); c.Currency == "" {
		if c.Currency, err = b.defaultCurrency(ctx, actor, parts); err != nil {
			return Form{}, err
		}
	}

	compare(&c, lines, parts, cats)

	for _, t := range totals {
		if t.Currency != c.Currency {
			c.Elsewhere = append(c.Elsewhere, t)
		}
	}

	if !c.Set && c.CanSet() {
		last, err := b.store.Lines(ctx, o.Scope(), year-1)
		if err != nil {
			return Form{}, err
		}

		c.CanCopy = len(last) > 0
	}

	// The form's choices go with the comparison: an organization's page
	// has read everything they need already.
	return form(c, cats, parts), nil
}

// SetOrgYear makes an organization's budget for a year these entries, in
// this currency: an owner's. Each change is a line in its history, saying
// which year.
func (b *Business) SetOrgYear(ctx context.Context, now time.Time, actor, orgID types.ID, year int, currency string, entries []Entry) error {
	o, access, year, err := b.org(ctx, now, actor, orgID, year)
	if err != nil {
		return err
	}

	if !access.Can(tenancybus.Manage) {
		return ErrForbidden
	}

	start, end := Span(year, o.FiscalStart)

	parts, _, err := b.ledger.OrgPeriod(ctx, actor, orgID, start, end)
	if err != nil {
		return err
	}

	cats, err := b.choices(ctx, orgID, parts)
	if err != nil {
		return err
	}

	before, err := b.store.Lines(ctx, o.Scope(), year)
	if err != nil {
		return err
	}

	lines, err := linesOf(o.Scope(), year, currency, entries, cats, before, actor, now)
	if err != nil {
		return err
	}

	return b.store.Replace(ctx, o.Scope(), year, lines, events(now, actor, o.Scope(), Label(year, o.FiscalStart), before, lines, cats))
}

// ErrNothingToCopy is a year that has a budget already, or whose year
// before has none.
var ErrNothingToCopy = errors.New("there is no budget to copy, or this year has one")

// CopyYear makes a year's budget the year before's, for a year that has
// none: an owner's. A line for a category since archived, or now of
// another kind, is left behind, since it could not be set today. One line
// of history says it was copied.
func (b *Business) CopyYear(ctx context.Context, now time.Time, actor, orgID types.ID, year int) (int, error) {
	o, access, year, err := b.org(ctx, now, actor, orgID, year)
	if err != nil {
		return 0, err
	}

	if !access.Can(tenancybus.Manage) {
		return 0, ErrForbidden
	}

	have, err := b.store.Lines(ctx, o.Scope(), year)
	if err != nil {
		return 0, err
	}

	last, err := b.store.Lines(ctx, o.Scope(), year-1)
	if err != nil {
		return 0, err
	}

	if len(have) > 0 || len(last) == 0 {
		return 0, ErrNothingToCopy
	}

	cats, err := b.choices(ctx, orgID, nil)
	if err != nil {
		return 0, err
	}

	var lines []Line

	for _, l := range last {
		if !l.CategoryID.Zero() {
			if c, ok := cats[l.CategoryID]; !ok || c.Archived() || c.Kind != l.Kind {
				continue
			}
		}

		l.ID, l.Year, l.UpdatedBy, l.UpdatedAt = types.NewID(), year, actor, now
		lines = append(lines, l)
	}

	if len(lines) == 0 {
		return 0, ErrNothingToCopy
	}

	ev := eventbus.New(now, actor, o.Scope(), Copied, map[string]string{
		"from": Label(year-1, o.FiscalStart), "year": Label(year, o.FiscalStart), "count": strconv.Itoa(len(lines)),
	})

	return len(lines), b.store.Replace(ctx, o.Scope(), year, lines, []eventbus.Event{ev})
}
