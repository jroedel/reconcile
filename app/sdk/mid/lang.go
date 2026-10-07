// Package mid holds the middleware that knows about this application but not
// about any one domain: which language a request is in, and who is signed in.
// Request plumbing with no domain words stays in foundation/web.
package mid

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jroedel/reconcile/business/types"
)

// langCookie remembers the choice made with the language links.
const langCookie = "lang"

type ctxKey int

const (
	langKey ctxKey = iota + 1
	userKey
)

// LangFrom is the language of the request, English outside one.
func LangFrom(ctx context.Context) types.Lang {
	if l, ok := ctx.Value(langKey).(types.Lang); ok {
		return l
	}

	return types.English
}

// Lang decides the language of every request, in this order:
//
//  1. ?lang=es, ?lang=pt or ?lang=en, which is what the header's language
//     links point to. It is remembered in a cookie and the browser is sent
//     back to the same address without it, so the choice sticks for every
//     page after, and an address shared or bookmarked is the clean one.
//  2. The cookie, from an earlier choice on this browser.
//  3. The language the signed-in user chose on their account page, so that
//     it follows them to a new phone. Authenticate must come before Lang in
//     the chain for this one to be seen.
//  4. The phone's own language, from Accept-Language. Somebody whose phone
//     is set to Portuguese gets Portuguese without finding the links first.
//  5. English.
//
// A cookie rather than a /es/ prefix on every path, because an address is
// something people send each other -- "the receipt is at this link" -- and
// it should open in the reader's language, not the sender's.
func Lang() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if asked := r.URL.Query().Get("lang"); asked != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				if l, err := types.ParseLang(asked); err == nil {
					SetLang(w, l)

					q := r.URL.Query()
					q.Del("lang")
					clean := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}

					http.Redirect(w, r, clean.String(), http.StatusSeeOther)

					return
				}
			}

			ctx := context.WithValue(r.Context(), langKey, chooseLang(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SetLang remembers a language on this browser, or forgets it when l is "",
// so that the next request follows the user's own setting or the phone.
//
// Not Secure, not HttpOnly: it is a display preference that reveals nothing,
// and it must work on plain http while developing. SameSite=Lax is the
// browser default anyway.
func SetLang(w http.ResponseWriter, l types.Lang) {
	c := &http.Cookie{
		Name:     langCookie,
		Value:    string(l),
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		SameSite: http.SameSiteLaxMode,
	}

	if l == "" {
		c.MaxAge = -1
	}

	http.SetCookie(w, c)
}

func chooseLang(r *http.Request) types.Lang {
	if c, err := r.Cookie(langCookie); err == nil {
		if l, err := types.ParseLang(c.Value); err == nil {
			return l
		}
	}

	if u, ok := UserFrom(r.Context()); ok && u.Lang != "" {
		return u.Lang
	}

	return fromAcceptLanguage(r.Header.Get("Accept-Language"))
}

// fromAcceptLanguage picks whichever language the app speaks the browser
// ranks highest. "es-MX", "es-419" and plain "es" are all Spanish, "pt-BR"
// and "pt-PT" both Portuguese; a phone that asks for none of them gets
// English.
func fromAcceptLanguage(header string) types.Lang {
	best, bestQ := types.English, -1.0

	for part := range strings.SplitSeq(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")

		l, err := types.ParseLang(primary)
		if err != nil {
			continue
		}

		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if parsed, err := strconv.ParseFloat(v, 64); err == nil {
				q = parsed
			}
		}

		if q > bestQ {
			best, bestQ = l, q
		}
	}

	return best
}

// SwitchURL is the current address with ?lang= set to l, for the language
// links.
func SwitchURL(r *http.Request, l types.Lang) string {
	q := r.URL.Query()
	q.Set("lang", string(l))

	return (&url.URL{Path: r.URL.Path, RawQuery: q.Encode()}).String()
}
