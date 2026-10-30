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
