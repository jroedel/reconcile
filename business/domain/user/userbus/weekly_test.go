package userbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
)

// The next email is the first chosen hour that has not begun, in the
// person's own time zone, across a change of the clocks.
func TestTheNextWeeklyEmail(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	ny, _ := time.LoadLocation("America/New_York")
	monday8 := userbus.Weekly{Enabled: true, Weekday: time.Monday, Hour: 8}

	for _, c := range []struct {
		name string
		w    userbus.Weekly
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		{"a Thursday, to the Monday after", monday8, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)},
		{"a Monday before eight, the same day", monday8, time.Date(2026, 11, 2, 7, 59, 0, 0, time.UTC), time.UTC, time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)},
		{"a Monday at eight, a week on", monday8, time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)},
		{"in São Paulo, its own Monday", monday8, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), sp, time.Date(2026, 11, 2, 8, 0, 0, 0, sp)},
		{"in New York, across the clocks going back", monday8, time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), ny, time.Date(2026, 11, 2, 8, 0, 0, 0, ny)},
		{"a Sunday at midnight", userbus.Weekly{Weekday: time.Sunday}, time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if got := c.w.Next(c.now, c.loc); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Nobody who never chose is without the defaults; a day, an hour and a
// zone are checked; and the zone a browser sends is kept only when there
// is none.
func TestTheWeeklySettings(t *testing.T) {
	users := newBus(t)
	ctx := t.Context()
	u := signUp(t, users, "treasurer@example.org")
	now := start

	if got, err := users.Weekly(ctx, u.ID); err != nil || got != userbus.DefaultWeekly {
		t.Errorf("never chosen: %+v, %v", got, err)
	}

	for _, c := range []struct {
		w    userbus.Weekly
		zone string
		want error
	}{
		{userbus.Weekly{Weekday: 7, Hour: 8}, "UTC", userbus.ErrWeekly},
		{userbus.Weekly{Weekday: time.Monday, Hour: 24}, "UTC", userbus.ErrWeekly},
		{userbus.Weekly{Weekday: time.Monday, Hour: 8}, "Mars/Olympus", userbus.ErrZone},
		{userbus.Weekly{Weekday: time.Monday, Hour: 8}, "", userbus.ErrZone},
		{userbus.Weekly{Weekday: time.Monday, Hour: 8}, "Local", userbus.ErrZone},
	} {
		if err := users.SetWeekly(ctx, now, u.ID, c.w, c.zone); !errors.Is(err, c.want) {
			t.Errorf("%+v in %q: %v, want %v", c.w, c.zone, err, c.want)
		}
	}

	if err := users.SetZoneOnce(ctx, now, u.ID, "Not/AZone"); err != nil {
		t.Fatal(err)
	}

	if err := users.SetZoneOnce(ctx, now, u.ID, "America/Sao_Paulo"); err != nil {
		t.Fatal(err)
	}

	friday := userbus.Weekly{Enabled: true, Weekday: time.Friday, Hour: 17}
	if err := users.SetZoneOnce(ctx, now, u.ID, "Europe/Lisbon"); err != nil {
		t.Fatal(err)
	}

	if got, _ := users.ByID(ctx, u.ID); got.Zone != "America/Sao_Paulo" {
		t.Errorf("the browser's zone, twice: %q", got.Zone)
	}

	if err := users.SetWeekly(ctx, now, u.ID, friday, "Europe/Lisbon"); err != nil {
		t.Fatal(err)
	}

	got, err := users.ByID(ctx, u.ID)
	if err != nil || got.Zone != "Europe/Lisbon" {
		t.Errorf("the zone chosen: %q, %v", got.Zone, err)
	}

	if got, err := users.Weekly(ctx, u.ID); err != nil || got != friday {
		t.Errorf("the settings chosen: %+v, %v", got, err)
	}

	if userbus.Location("") != time.UTC || userbus.Location("Mars/Olympus") != time.UTC || userbus.Location("Europe/Lisbon").String() != "Europe/Lisbon" {
		t.Error("Location")
	}
}

// Whose email is due: at the person's own hour, within Late of it, once a
// week, never when it is off; and the claim is taken once.
func TestWhoseWeeklyEmailIsDue(t *testing.T) {
	users := newBus(t)
	ctx := t.Context()
	treasurer := signUp(t, users, "treasurer@example.org")
	confrere := signUp(t, users, "confrere@example.org")
	quiet := signUp(t, users, "quiet@example.org")

	if err := users.SetWeekly(ctx, start, confrere.ID, userbus.Weekly{Enabled: true, Weekday: time.Monday, Hour: 8}, "America/Sao_Paulo"); err != nil {
		t.Fatal(err)
	}

	if err := users.SetWeekly(ctx, start, quiet.ID, userbus.Weekly{Weekday: time.Monday, Hour: 8}, "UTC"); err != nil {
		t.Fatal(err)
	}

	due := func(now time.Time) map[string]time.Time {
		t.Helper()

		ds, err := users.DueWeekly(ctx, now)
		if err != nil {
			t.Fatal(err)
		}

		out := map[string]time.Time{}
		for _, d := range ds {
			out[d.User.Email.String()] = d.Since
		}

		return out
	}

	monday := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

	if got := due(monday.Add(7*time.Hour + 59*time.Minute)); len(got) != 0 {
		t.Errorf("before eight: %v", got)
	}

	at := monday.Add(8*time.Hour + 5*time.Minute)
	if got := due(at); len(got) != 1 || !got["treasurer@example.org"].IsZero() {
		t.Fatalf("at eight UTC, the treasurer for the first time, and nobody else: %v", got)
	}

	if ok, err := users.ClaimWeek(ctx, at, treasurer.ID, time.Time{}); err != nil || !ok {
		t.Fatalf("claiming: %v %v", ok, err)
	}

	if ok, err := users.ClaimWeek(ctx, at, treasurer.ID, time.Time{}); err != nil || ok {
		t.Fatalf("claimed twice: %v %v", ok, err)
	}

	// Eight in São Paulo is eleven UTC; the treasurer has had theirs.
	if got := due(monday.Add(11*time.Hour + 5*time.Minute)); len(got) != 1 || !got["confrere@example.org"].IsZero() {
		t.Errorf("at eight in São Paulo: %v", got)
	}

	// Past Late, a week that was never sent waits for the next.
	if got := due(monday.Add(17*time.Hour + 5*time.Minute)); len(got) != 0 {
		t.Errorf("too late: %v", got)
	}

	next := monday.AddDate(0, 0, 7).Add(8*time.Hour + 10*time.Minute)
	if got := due(next); len(got) != 1 || !got["treasurer@example.org"].Equal(at) {
		t.Errorf("a week on, since the last: %v", got)
	}
}

// The link in an email stops it, once, and only the last email's link;
// the day and the hour are kept for when it is turned back on.
func TestStoppingTheWeeklyEmail(t *testing.T) {
	users := newBus(t)
	ctx := t.Context()
	u := signUp(t, users, "treasurer@example.org")

	friday := userbus.Weekly{Enabled: true, Weekday: time.Friday, Hour: 17}
	if err := users.SetWeekly(ctx, start, u.ID, friday, "Europe/Lisbon"); err != nil {
		t.Fatal(err)
	}

	older, err := users.StopToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	token, err := users.StopToken(ctx, u.ID)
	if err != nil || token == older {
		t.Fatalf("a second token: %q %v", token, err)
	}

	for _, wrong := range []string{"", older, "NOTATOKEN"} {
		if ok, err := users.StopWeekly(ctx, wrong); err != nil || ok {
			t.Errorf("%q stopped it: %v %v", wrong, ok, err)
		}
	}

	if ok, err := users.StopWeekly(ctx, token); err != nil || !ok {
		t.Fatalf("stopping: %v %v", ok, err)
	}

	if ok, _ := users.StopWeekly(ctx, token); ok {
		t.Error("a token used twice")
	}

	stopped := friday
	stopped.Enabled = false

	if got, err := users.Weekly(ctx, u.ID); err != nil || got != stopped {
		t.Errorf("after: %+v %v", got, err)
	}
}
