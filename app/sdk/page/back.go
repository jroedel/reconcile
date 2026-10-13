package page

import (
	"net/http"
	"net/url"
	"regexp"

	"github.com/jroedel/reconcile/app/sdk/mid"
)

// Return is the page a person came from, when it is not the one above:
// a transaction opened from a report's figure, a project's money, a
// payee's year. The page shows a way back to it, and saving sends them
// there instead of to the account's month. Without one, a person who
// opened a charge from the report to fix it landed on the account's
// month after saving, and went back through the organization and chose
// the months again (docs/design.md).
type Return struct {
	Path string // the page, as this site's own address
	Kind string // what kind of page it is, for the words: see returnKinds
}

// Query is the return as a query, escaped, for a form's hidden value
// that is itself an address: /transactions/{id}?back=…. Where the
// template writes the address into an href or an action, it writes
// ?back={{.Path}} and the template escapes it.
func (r Return) Query() string { return url.Values{"back": {r.Path}}.Encode() }

// idPattern is one of this site's IDs in an address.
const idPattern = `[0-9a-f]{32}`

// returnKinds is each page a way back is offered to, by its address. The
// words for each are in base.html's "return", so that they go through t
// whole; a page not listed here is not offered, and saving goes where it
// always went.
var returnKinds = []struct {
	kind string
	path *regexp.Regexp
}{
	{"report", regexp.MustCompile(`^/(orgs|accounts|projects)/` + idPattern + `/reports/[a-z-]+(/parts)?$`)},
	{"report", regexp.MustCompile(`^/reports/` + idPattern + `(/parts)?$`)},
	{"book", regexp.MustCompile(`^/projects/` + idPattern + `/book$`)},
	{"sort", regexp.MustCompile(`^/accounts/` + idPattern + `/sort$`)},
	{"statement", regexp.MustCompile(`^/statements/` + idPattern + `$`)},
	{"payees-year", regexp.MustCompile(`^/(orgs|accounts)/` + idPattern + `/payees/[0-9]{4}$`)},
	{"payee", regexp.MustCompile(`^/payees/` + idPattern + `$`)},
	{"explain", regexp.MustCompile(`^/transactions/` + idPattern + `/explain$`)},
	{"receipt", regexp.MustCompile(`^/receipts/` + idPattern + `$`)},
	{"receipts", regexp.MustCompile(`^/receipts$`)},
}

// ReturnFrom is the way back a link or a form's address carried in its
// query as back=, or nil for none. Only the query is read, never the
// body: a receipt's upload is a stream read part by part, and reading a
// form from it here would swallow the files. An address that is not this
// site's own page is refused (mid.SafeNext), so a link from elsewhere
// cannot send a person off the site after they save.
func ReturnFrom(r *http.Request) *Return {
	raw := r.URL.Query().Get("back")
	if raw == "" || mid.SafeNext(raw) != raw {
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}

	for _, k := range returnKinds {
		if k.path.MatchString(u.Path) {
			return &Return{Path: raw, Kind: k.kind}
		}
	}

	return nil
}

// here is a page's own address, for a link from it that offers a way
// back (Shell.Here): its path and query without what said only what just
// happened, so that coming back does not say "Saved." again. Empty for a
// page that answers a form, whose address is the form's and cannot be
// opened again.
func here(r *http.Request) string {
	if r.Method != http.MethodGet {
		return ""
	}

	q := r.URL.Query()
	q.Del("done")
	q.Del("problem")

	if len(q) == 0 {
		return r.URL.Path
	}

	return r.URL.Path + "?" + q.Encode()
}
