package authapp

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
)

// The weekly email's settings on the profile (docs/accountant.md, 5).

// static is the script, and only it: its tests sit beside it, and are
// nobody's to download.
//
//go:embed static/tz.mjs
var static embed.FS

// ZoneScript is the script the sign-in forms and the profile load for the
// device's time zone.
const ZoneScript = "/account/static/tz.mjs"

// Weekdays is the days the profile offers, Monday first.
var Weekdays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

// Zones is the time zones the profile's field suggests as it is typed in:
// those of the Americas, Europe and Africa the people this is for live in,
// and a few more. Any other IANA name is taken as well; Go knows them all
// and cannot list them.
var Zones = []string{
	"America/New_York", "America/Chicago", "America/Denver", "America/Phoenix", "America/Los_Angeles",
	"America/Anchorage", "Pacific/Honolulu", "America/Puerto_Rico", "America/Toronto", "America/Vancouver",
	"America/Mexico_City", "America/Guatemala", "America/Costa_Rica", "America/Panama", "America/Bogota",
	"America/Caracas", "America/Lima", "America/Guayaquil", "America/La_Paz", "America/Santiago",
	"America/Asuncion", "America/Argentina/Buenos_Aires", "America/Montevideo", "America/Sao_Paulo",
	"America/Manaus", "America/Recife", "America/Fortaleza", "America/Belem", "America/Cuiaba", "America/Santo_Domingo",
	"Europe/Lisbon", "Europe/London", "Europe/Dublin", "Europe/Madrid", "Europe/Paris", "Europe/Brussels",
	"Europe/Berlin", "Europe/Rome", "Europe/Vienna", "Europe/Warsaw", "Europe/Prague", "Europe/Zurich",
	"Africa/Lagos", "Africa/Nairobi", "Africa/Johannesburg", "Africa/Luanda", "Africa/Maputo", "Africa/Kinshasa",
	"Asia/Manila", "Asia/Kolkata", "Asia/Tokyo", "Australia/Sydney", "UTC",
}

// Hours is the hours of the day the profile offers.
var Hours = func() []int {
	h := make([]int, 24)
	for i := range h {
		h[i] = i
	}

	return h
}()

func (a app) setWeekly(w http.ResponseWriter, r *http.Request) {
	u, ok := signedIn(w, r)
	if !ok {
		return
	}

	refuse := func(status int, problem string, typed userbus.Weekly, zone string) {
		view := a.accountFor(r.Context(), u)
		view.Problem, view.Weekly, view.Zone, view.Next = problem, typed, zone, time.Time{}
		a.cfg.Render.Render(w, r, status, "account", view)
	}

	if err := r.ParseForm(); err != nil {
		refuse(http.StatusBadRequest, "unreadable", userbus.DefaultWeekly, u.Zone)

		return
	}

	day, errDay := strconv.Atoi(r.PostFormValue("weekday"))
	hour, errHour := strconv.Atoi(r.PostFormValue("hour"))
	typed := userbus.Weekly{Enabled: r.PostFormValue("enabled") == "1", Weekday: time.Weekday(day), Hour: hour}
	zone := r.PostFormValue("tz")

	if errDay != nil || errHour != nil {
		refuse(http.StatusUnprocessableEntity, "weekly-invalid", typed, zone)

		return
	}

	err := a.cfg.Users.SetWeekly(r.Context(), a.cfg.Now(), u.ID, typed, zone)

	switch {
	case errors.Is(err, userbus.ErrZone):
		refuse(http.StatusUnprocessableEntity, "zone-unknown", typed, zone)

		return
	case errors.Is(err, userbus.ErrWeekly):
		refuse(http.StatusUnprocessableEntity, "weekly-invalid", typed, zone)

		return
	case err != nil:
		a.cfg.Log.Error("the weekly email's settings could not be saved", "user_id", u.ID.String(), "error", err)
		refuse(http.StatusInternalServerError, "server", typed, zone)

		return
	}

	http.Redirect(w, r, "/account?done=weekly#weekly", http.StatusSeeOther)
}

// started is when this binary started, for the script's Last-Modified.
var started = time.Now()

// serveStatic serves the app's script, asked about each time (no-cache,
// with its hash as the ETag), as receiptapp serves its own.
func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")

	body, err := static.ReadFile("static/" + name)
	if err != nil || path.Ext(name) != ".mjs" {
		http.NotFound(w, r)

		return
	}

	sum := sha256.Sum256(body)
	h := w.Header()
	h.Set("ETag", `"`+hex.EncodeToString(sum[:])[:16]+`"`)
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")

	http.ServeContent(w, r, name, started, bytes.NewReader(body))
}
