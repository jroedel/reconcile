package userbus

import (
	"context"
	"errors"
	"time"

	"github.com/jroedel/reconcile/business/types"
)

// The weekly email's settings (docs/accountant.md, 5): whether a person
// gets it, and on what day and at what hour of their own time zone.
//
// Here, and not with the email, because they are the person's: they are
// read and changed on their profile, and through the API as their own,
// whatever any organization says. What goes in the email is the digest's
// to say, from the books.

// Weekly is a person's settings for the weekly email.
type Weekly struct {
	Enabled bool
	Weekday time.Weekday
	Hour    int
}

// DefaultWeekly is the settings of somebody who never chose: on, Monday
// morning. On for everybody, rather than only for an accountant, a
// bookkeeper or an owner as first planned, because an email with nothing
// in it is never sent and only those roles' lines ever have anything in
// them; so the default reaches exactly those people, without this domain
// asking another which roles somebody holds.
var DefaultWeekly = Weekly{Enabled: true, Weekday: time.Monday, Hour: 8}

// ErrZone is a time zone this app does not know.
var ErrZone = errors.New("choose a time zone from the list, such as America/New_York")

// ErrWeekly is a day or an hour that is not one.
var ErrWeekly = errors.New("choose a day of the week and an hour of the day")

// Location is a time zone by its name, or UTC for none or one unknown: a
// person with no time zone yet gets the email at their hour of UTC, which
// their profile says plainly.
func Location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}

	return loc
}

// knownZone reports whether name is a time zone by its IANA name. "UTC" is
// one; "Local" and "" are not names anybody chose.
func knownZone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}

	_, err := time.LoadLocation(name)

	return err == nil
}

// Next is when the next email is due after now, at the day and hour in
// loc: the first such hour that has not begun yet.
func (w Weekly) Next(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	days := (int(w.Weekday) - int(local.Weekday()) + 7) % 7

	next := time.Date(local.Year(), local.Month(), local.Day()+days, w.Hour, 0, 0, 0, loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 7)
	}

	return next
}

// Weekly is a person's settings, the defaults when they never chose.
func (b *Business) Weekly(ctx context.Context, userID types.ID) (Weekly, error) {
	w, ok, err := b.store.Weekly(ctx, userID)
	if err != nil || !ok {
		return DefaultWeekly, err
	}

	return w, nil
}

// SetWeekly saves a person's settings and their time zone, which only they
// change.
func (b *Business) SetWeekly(ctx context.Context, now time.Time, userID types.ID, w Weekly, zone string) error {
	if w.Weekday < time.Sunday || w.Weekday > time.Saturday || w.Hour < 0 || w.Hour > 23 {
		return ErrWeekly
	}

	if !knownZone(zone) {
		return ErrZone
	}

	if err := b.store.SetWeekly(ctx, userID, w); err != nil {
		return err
	}

	return b.store.SetZone(ctx, now, userID, zone, false)
}

// SetZoneOnce gives a person the time zone their browser says, when they
// have none yet: what the sign-in page sends, so that the first email
// comes at their own hour without their visiting the profile. One they
// chose is never overwritten, and a name this app does not know is left
// out, since nobody typed it.
func (b *Business) SetZoneOnce(ctx context.Context, now time.Time, userID types.ID, zone string) error {
	if !knownZone(zone) {
		return nil
	}

	return b.store.SetZone(ctx, now, userID, zone, true)
}
