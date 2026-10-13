package page

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestReturnFrom(t *testing.T) {
	t.Parallel()

	const org = "/orgs/0123456789abcdef0123456789abcdef"

	for _, tc := range []struct {
		back, kind string
	}{
		{org + "/reports/income-expenses-by-month/parts?kind=income&month=2026-07", "report"},
		{"/reports/0123456789abcdef0123456789abcdef", "report"},
		{"/projects/0123456789abcdef0123456789abcdef/book", "book"},
		{"/accounts/0123456789abcdef0123456789abcdef/sort?month=2026-07", "sort"},
		{org + "/payees/2026", "payees-year"},
		{"/transactions/0123456789abcdef0123456789abcdef/explain", "explain"},
		{"/receipts", "receipts"},

		// Not a page a way back is offered to, or not this site's.
		{"/accounts/0123456789abcdef0123456789abcdef/transactions", ""},
		{"//elsewhere.invalid" + org + "/payees/2026", ""},
		{"https://elsewhere.invalid/receipts", ""},
		{"/receipts\r\nLocation: /", ""},
		{"", ""},
	} {
		r := httptest.NewRequest("GET", "/transactions/0123456789abcdef0123456789abcdef?"+url.Values{"back": {tc.back}}.Encode(), nil)

		got := ReturnFrom(r)
		switch {
		case tc.kind == "" && got != nil:
			t.Errorf("%q: offered a way back to %+v", tc.back, got)
		case tc.kind != "" && (got == nil || got.Kind != tc.kind || got.Path != tc.back):
			t.Errorf("%q: %+v, want %s", tc.back, got, tc.kind)
		}
	}
}

func TestHereLeavesOutWhatJustHappened(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest("GET", "/projects/1/book?done=sorted&kind=income&problem=x", nil)
	if got := here(r); got != "/projects/1/book?kind=income" {
		t.Errorf("here = %q", got)
	}

	if got := here(httptest.NewRequest("POST", "/transactions/1", nil)); got != "" {
		t.Errorf("a form's answer has an address to come back to: %q", got)
	}
}
