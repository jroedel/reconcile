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
