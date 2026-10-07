package web

import (
	"mime"
	"net/http"
	"strings"
)

// --- where a write came from ------------------------------------------------

// SameOriginOnly refuses a write that a page of this site did not make.
//
// Taken from mass-intentions, with one change that matters here. Safe methods
// are not checked: writing is the whole attack, and reading a form tells
// nobody anything.
//
//   - Sec-Fetch-Site is sent by every current browser and page script cannot
//     forge it. same-origin is ours; none means somebody typed the address or
//     used a bookmark. same-site and cross-site are refused.
//   - Where it is absent -- an old browser, or curl -- Origin is compared
//     against origin, the site's own public address.
//   - A request carrying neither is allowed through. That is the hole in this
//     check, and it is deliberate: it is a command-line client rather than a
//     browser being used against somebody, and no browser reaches it.
//
// The change: mass-intentions compares Origin against the request's Host. On
// konsoleH that is wrong, because the .htaccess proxy cannot preserve the
// Host header and every request arrives as Host: 127.0.0.1:<port>. The
// comparison there is only ever reached by a browser too old to send
// Sec-Fetch-Site, so it has never refused a real form -- but here it would, so
// the public origin is configuration instead.
//
// So this is a browser-CSRF control and nothing else. What protects a person's
// session is that the cookie is SameSite=Lax and this refuses cross-site writes.
func SameOriginOnly(origin string) Middleware {
	origin = strings.TrimSuffix(origin, "/")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !safeMethod(r.Method) && !sameOrigin(r, origin) {
				http.Error(w,
					"That did not come from a page on this site. Open the page again and send it from there.",
					http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func sameOrigin(r *http.Request, origin string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "same-site", "cross-site":
		return false
	}

	// A browser sends Origin: null on some native form posts, so a null one
	// is no usable Origin rather than a mismatch -- mass-intentions found
	// that the hard way, rejecting its own forms on exactly the old clients
	// this fallback exists for.
	got := r.Header.Get("Origin")
	if got == "" || got == "null" {
		return true
	}

	return got == origin
}

// --- body size --------------------------------------------------------------

// MaxBody caps what a request may send.
//
// Every route a stranger can post to needs one, because a form endpoint is an
// upload endpoint whether or not it was meant to be.
//
// It can only tighten. http.MaxBytesReader wraps a body, and wrapping a body
// already capped at 64 KB in a reader that allows 10 MB still stops at 64 KB
// -- mass-intentions shipped exactly that, and the symptom was not "too big"
// but a 502 from Apache, which was still writing the upload into a socket the
// server had closed. An upload route gets its own limit, and this one is not
// wrapped around it.
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// --- what a write may be ------------------------------------------------------

// FormEncoded is the one content type a form post arrives as.
const FormEncoded = "application/x-www-form-urlencoded"

// FormEncodedOnly refuses a write that is not an ordinary form post.
//
// r.ParseForm accepts multipart as well, and multipart is the expensive parse:
// a body read into memory in parts by a handler that has not decided it wants
// the request. Outside the few routes given MultipartOnly instead, the only
// bodies this service parses are the ones its own forms produce.
func FormEncodedOnly() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !safeMethod(r.Method) && !formEncoded(r.Header.Get("Content-Type")) {
				http.Error(w,
					"That was not sent from one of this site's forms. Open the page again and send it from there.",
					http.StatusUnsupportedMediaType)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// formEncoded reads a Content-Type the way the specification says to.
// mime.ParseMediaType rather than a prefix test, because a longer type with
// this one as its prefix is not this type.
func formEncoded(header string) bool {
	kind, _, err := mime.ParseMediaType(header)

	return err == nil && kind == FormEncoded
}

// Multipart is the content type a form carrying a file arrives as.
const Multipart = "multipart/form-data"

// MultipartOnly is FormEncodedOnly's counterpart for the routes that take a
// file: a write to one of them must be a multipart form. It is mounted on
// those routes alone, so that the expensive parse is only ever offered where
// a handler asked for it.
func MultipartOnly() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if !safeMethod(r.Method) && (err != nil || kind != Multipart) {
				http.Error(w,
					"That was not sent from one of this site's forms. Open the page again and send it from there.",
					http.StatusUnsupportedMediaType)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
