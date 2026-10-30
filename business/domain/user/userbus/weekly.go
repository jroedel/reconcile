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

// Last is when the last email was due at or before now: the week before
// Next, at the same hour of the same local day, whatever the clocks did.
func (w Weekly) Last(now time.Time, loc *time.Location) time.Time {
	return w.Next(now, loc).AddDate(0, 0, -7)
}

// Late is how long after its hour an email still goes. The sender looks
// every few minutes, so this is for a server that was down at the hour;
// past it, that week's email waits for the next, which says everything
// since the last that went, so nothing is lost but its timing. It also
// keeps an email from going at a strange hour to somebody who turns it
// back on mid-week, or who has just been given their first role.
const Late = 6 * time.Hour

// WeeklyState is a person's settings, and when their last email was sent:
// zero for never.
type WeeklyState struct {
	Weekly
	SentUpTo time.Time
}

// Due is a person whose weekly email is due: their settings, and when the
// last one was sent, which is since when the next one tells.
type Due struct {
	User   User
	Weekly Weekly
	Since  time.Time
}

// DueWeekly is everybody whose weekly email is due at now: signed up and
// not disabled, with it on, and whose hour this week came within Late
// before now and after the last one they were sent.
func (b *Business) DueWeekly(ctx context.Context, now time.Time) ([]Due, error) {
	users, err := b.store.Users(ctx)
	if err != nil {
		return nil, err
	}

	states, err := b.store.Weeklies(ctx)
	if err != nil {
		return nil, err
	}

	var due []Due

	for _, u := range users {
		ws, ok := states[u.ID]
		if !ok {
			ws.Weekly = DefaultWeekly
		}

		if !u.Enabled || !ws.Enabled {
			continue
		}

		last := ws.Last(now, Location(u.Zone))
		if now.Sub(last) >= Late || (!ws.SentUpTo.IsZero() && !ws.SentUpTo.Before(last)) {
			continue
		}

		due = append(due, Due{User: u, Weekly: ws.Weekly, Since: ws.SentUpTo})
	}

	return due, nil
}

// ClaimWeek takes a person's email for this week, sent at now, if nobody
// took it since was: false means another sender did, and this one says
// nothing.
func (b *Business) ClaimWeek(ctx context.Context, now time.Time, userID types.ID, was time.Time) (bool, error) {
	return b.store.ClaimWeek(ctx, userID, was, now)
}

// StopToken is a fresh token for the link in a person's email that stops
// it, which replaces the last email's: only its hash is kept.
func (b *Business) StopToken(ctx context.Context, userID types.ID) (string, error) {
	token := mintSecret()

	if err := b.store.SetStopHash(ctx, userID, hashSecret(token)); err != nil {
		return "", err
	}

	return token, nil
}

// StopWeekly turns off the weekly email of whoever's last one carried the
// token, which no longer works after: false for a token that is not, or no
// longer, anybody's.
func (b *Business) StopWeekly(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}

	return b.store.StopByHash(ctx, hashSecret(token))
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
