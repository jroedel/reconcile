package authapp

import (
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/types"
)

// The cookies that hold the browser's half of a code between asking for it
// and typing it, with the address it went to so the page can say where to
// look. From mass-intentions.
//
// __Host- for the reason the session cookie is: the browser then refuses it
// unless it is Secure, on Path=/, and bound to this one hostname. HttpOnly,
// because nothing on the page needs to read it.
//
// A cookie rather than a hidden field. A phone that puts the browser away
// while somebody reads their mail can bring it back as a fresh load of the
// page, and a hidden field does not survive that; a cookie does.
const (
	signInCookie      = "__Host-signin"
	emailChangeCookie = "__Host-email-change"
)

// pendingLife matches the codes' own fifteen minutes. A cookie outliving them
// would only carry somebody to a refusal.
const pendingLife = 15 * time.Minute

// setPending writes one. The address is base64 so that nothing in it needs
// escaping rules of its own; the credential is hex, a dot and base32, and
// cannot contain the separator.
func setPending(w http.ResponseWriter, name, pending string, email types.Email) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    pending + "~" + base64.RawURLEncoding.EncodeToString([]byte(email.String())),
		Path:     "/",
		MaxAge:   int(pendingLife / time.Second),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// readPending returns the credential and the address, or two empty strings.
//
// The address is for display only: it says which inbox to look in, and is
// escaped by the template like anything else. Nothing is decided by it; the
// credential alone names the request.
func readPending(r *http.Request, name string) (pending, email string) {
	c, err := r.Cookie(name)
	if err != nil {
		return "", ""
	}

	pending, enc, ok := strings.Cut(c.Value, "~")
	if !ok || pending == "" {
		return "", ""
	}

	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return pending, ""
	}

	return pending, string(raw)
}

// clearPending removes one once it has been spent. The attributes match
// setPending's, or the browser treats it as a different cookie.
func clearPending(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
