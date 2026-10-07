package mid

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/foundation/web"
)

// SessionCookie is the signed-in browser's session.
//
// The __Host- prefix makes the browser refuse the cookie unless it is Secure,
// has Path=/ and no Domain -- so no other subdomain of the site's domain can
// set it or read it, including whatever lives on the parent domain.
const SessionCookie = "__Host-session"

// Authenticator is the slice of userbus this needs.
type Authenticator interface {
	Authenticate(ctx context.Context, now time.Time, presented string) (userbus.User, error)
}

// UserFrom is the user signed in on this request. The bool is false on the
// front page and the sign-in pages; a handler that needs it true belongs
// behind Require.
func UserFrom(ctx context.Context) (userbus.User, bool) {
	u, ok := ctx.Value(userKey).(userbus.User)

	return u, ok
}

// Authenticate settles who is asking, and refuses nobody.
//
// Refusing nobody is what lets the front page, the sign-in page and an
// account's ledger share one chain. Require comes after it, so a route
// mounted without Require fails by looking signed out rather than by leaking.
//
// A cookie that no longer resolves is cleared on the way past. Otherwise a
// session that ended while a tab was open is a cookie the browser keeps
// sending, and Require keeps redirecting, with nothing to say why.
func Authenticate(log *slog.Logger, auth Authenticator) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookie)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)

				return
			}

			u, err := auth.Authenticate(r.Context(), time.Now(), c.Value)

			switch {
			case errors.Is(err, userbus.ErrDenied):
				ClearSession(w)
				next.ServeHTTP(w, r)

				return
			case err != nil:
				// Not "signed out". An unreadable database would otherwise
				// look like a sign-in page, and somebody would sign in again
				// and again with nothing saying why.
				log.Error("the session could not be checked", "request_id", web.RequestIDFrom(r.Context()), "error", err)
				http.Error(w, "Something went wrong at our end. Try again in a few minutes.", http.StatusInternalServerError)

				return
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
		})
	}
}

// Require refuses a request with nobody signed in behind it.
//
// A page read is sent to sign in, carrying where it was going so that signing
// in lands them there. Anything else is a bare 403: a redirected POST
// loses its body, and answering a form with a different page is worse than a
// refusal that says why.
func Require(signInPath string) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := UserFrom(r.Context()); ok {
				next.ServeHTTP(w, r)

				return
			}

			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "Sign in to do that, then try again.", http.StatusForbidden)

				return
			}

			to := signInPath
			if want := SafeNext(r.URL.RequestURI()); want != "" {
				to += "?next=" + url.QueryEscape(want)
			}

			http.Redirect(w, r, to, http.StatusSeeOther)
		})
	}
}

// SafeNext is raw if it is a path on this site, and "" otherwise.
//
// The open-redirect guard: a sign-in link that lands somewhere else is a
// phishing page with our address in front of it. It refuses the shapes that
// get another origin past "starts with a slash":
//
//	//evil.example/    protocol-relative: a browser reads it as https://evil.example/
//	/\evil.example/    browsers turn the backslash into a slash
//	https://evil.example   has a scheme
//
// "" rather than an error, because a hand-made next is a bug in our own links
// or an attempt, and the answer to both is the default page.
func SafeNext(raw string) string {
	switch {
	case raw == "", raw == "/",
		!strings.HasPrefix(raw, "/"),
		strings.HasPrefix(raw, "//"),
		strings.HasPrefix(raw, "/\\"),
		strings.ContainsAny(raw, "\r\n"): // a newline ends a Location header and starts another
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}

	return raw
}

// SetSession writes the session cookie.
//
// SameSite=Lax rather than Strict: following a link here from a mail app --
// an invitation, say -- is a cross-site navigation, and Strict would withhold
// the cookie on it, so somebody signed in would arrive looking signed out.
// What Lax still withholds is a cross-site POST, which with
// web.SameOriginOnly is the CSRF defence.
func SetSession(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, sessionCookie(value, expires))
}

// ClearSession removes it. The attributes must match those it was set with or
// the browser treats it as another cookie and keeps the first.
func ClearSession(w http.ResponseWriter) {
	c := sessionCookie("", time.Unix(0, 0))
	c.MaxAge = -1

	http.SetCookie(w, c)
}

func sessionCookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}
