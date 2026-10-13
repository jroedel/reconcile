package muxer

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The top bar names where a page belongs, one tap from every page: the
// organization for whoever holds a role on it, the account itself for
// whoever was given only the account, and nothing for a stranger, whose
// page must not confirm there is anything there at all.
func TestTheTopBarSaysWhereAPageBelongs(t *testing.T) {
	t.Parallel()

	e, txs, _, signUpAs := sorted(t)
	grocery := txs["CORNER GROCERY"]
	orgID := strings.TrimPrefix(e.org, "/orgs/")

	statement := regexp.MustCompile(`/statements/[0-9a-f]{32}`).FindString(e.owner.get(e.account + "/transactions").Body.String())
	if statement == "" {
		t.Fatal("no statement on the account's page")
	}

	parish := `<a class="place" href="` + e.org + `">St. Joseph Parish</a>`
	for _, path := range []string{
		e.org, e.account, e.account + "/transactions?month=2026-07", e.account + "/sort?month=2026-07",
		grocery, grocery + "/explain", statement, e.project, e.project + "/book",
		e.org + "/payees", e.org + "/reports/income-expenses-by-month", "/accounts/new?org=" + orgID,
	} {
		wantBody(t, e.owner.get(path), parish)
	}

	wantBody(t, e.owner.get(e.ownAccount), `<a class="place" href="`+e.ownAccount+`">My card</a>`)

	for _, path := range []string{"/", "/account", "/imports", "/accounts/new"} {
		if body := e.owner.get(path).Body.String(); strings.Contains(body, `class="place"`) {
			t.Errorf("%s belongs to nothing, and its top bar names a place", path)
		}
	}

	// Given the account alone, the treasurer is shown the account, never
	// the organization it lives in.
	wantRedirect(t, e.owner.post(e.account+"/people", url.Values{"email": {"treasurer@example.org"}, "role": {"bookkeeper"}}), e.account+"?done=granted")
	treasurer := signUpAs("treasurer@example.org")

	for _, path := range []string{e.account, grocery, statement} {
		body := treasurer.get(path).Body.String()
		if !strings.Contains(body, `<a class="place" href="`+e.account+`">Parish checking</a>`) || strings.Contains(body, "St. Joseph Parish") {
			t.Errorf("the treasurer's top bar on %s:\n%s", path, body)
		}
	}

	stranger := signUpAs("stranger@example.org")
	for _, path := range []string{e.org, e.account, grocery, statement, e.project} {
		if body := stranger.get(path).Body.String(); strings.Contains(body, `class="place"`) || strings.Contains(body, "St. Joseph Parish") {
			t.Errorf("a stranger's top bar on %s names a place:\n%s", path, body)
		}
	}
}
