// Package page holds what this app puts in its response headers, and later in
// a <head>.
//
// It sits one layer above foundation/ because a policy is a product decision
// rather than plumbing: what may be cached, what a page is allowed to load.
// foundation/web.SecureHeaders takes the policy as a value for exactly that
// reason, and nothing in foundation/ may know a domain word.
package page

import (
	"net/http"
	"strings"

	"github.com/jroedel/reconcile/foundation/web"
)

// hsts is sent on every response, so that it does not depend on a .htaccess
// file staying correct.
const hsts = "max-age=31536000; includeSubDomains"

// Policy is the whole header policy for this application.
//
// It starts as tight as it can be, with nothing allowed but what the app
// serves itself, and is widened a directive at a time when a page needs it.
//
// deploy/deploy.sh relies on the first directive: a response carrying
// "Content-Security-Policy: default-src 'none'" is how it tells the app's
// answer from Apache's, so keep default-src first and keep it 'none'.
func Policy() web.PolicyFor {
	return func(*http.Request) web.Policy {
		return web.Policy{
			ContentSecurityPolicy: strings.Join([]string{
				"default-src 'none'",
				"script-src 'self'",
				"connect-src 'self'",
				"style-src 'self'",
				"font-src 'self'",
				"img-src 'self' data:",

				// receiptapp's manifest, which is what lets Chrome install
				// the app and offer it in the phone's share sheet. Without
				// this it falls to default-src and is refused. The share
				// target's worker needs no line of its own: worker-src
				// falls back to script-src, and that is 'self'.
				"manifest-src 'self'",
				"form-action 'self'",
				"base-uri 'none'",
				"frame-ancestors 'none'",
			}, "; "),

			ReferrerPolicy:  "same-origin",
			CacheControl:    "no-store",
			FrameOptions:    "DENY",
			StrictTransport: hsts,
		}
	}
}

// AllowFormTo lets the page about to be written send its form on to origin as
// well as to this site. Lifted from stewards.
//
// For the one page that needs it: where a translator agrees to let a program
// sign in (oauthapp). Its form posts here, and the answer is a redirect back
// to the program -- to claude.ai -- and Chrome applies form-action to where
// a form's redirects lead as well as to where it posts. With 'self' alone the
// translator presses "Allow" and nothing happens. Widened for that page and
// that one origin, which the handler has checked against the program's own
// metadata document, rather than for every page.
func AllowFormTo(h http.Header, origin string) {
	directives := strings.Split(h.Get("Content-Security-Policy"), ";")

	for i, d := range directives {
		if name, _, _ := strings.Cut(strings.TrimSpace(d), " "); name == "form-action" {
			directives[i] = " " + strings.TrimSpace(d) + " " + origin
		}
	}

	h.Set("Content-Security-Policy", strings.TrimSpace(strings.Join(directives, ";")))
}
