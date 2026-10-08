package page_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/app/sdk/page"
)

func TestAllowFormToWidensOnlyFormAction(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Security-Policy", page.Policy()(httptest.NewRequest(http.MethodGet, "/", nil)).ContentSecurityPolicy)

	page.AllowFormTo(h, "https://claude.ai")

	got := h.Get("Content-Security-Policy")
	if !strings.Contains(got, "; form-action 'self' https://claude.ai; ") || !strings.HasPrefix(got, "default-src 'none'; ") || strings.Count(got, "https://claude.ai") != 1 {
		t.Errorf("%s", got)
	}
}
