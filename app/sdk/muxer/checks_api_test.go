package muxer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A key for printing checks is made on the keys screen and listed as one;
// it reads nothing of the books, and Claude's tools are none of its.
func TestAChecksKeyReadsNoBooks(t *testing.T) {
	t.Parallel()

	s := newTranslatingSite(t)
	e := newEstate(t, s.h, s.sent)
	key := keyFor(t, e.owner, "Office printer", "checks")

	wantBody(t, e.owner.get("/account/keys"), "Office printer", "prints checks")

	for _, path := range []string{"/api/v1/me", "/api/v1/overview", "/api/v1/receipts/waiting"} {
		if rec := s.api(http.MethodGet, path, key, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a checks key: %d", path, rec.Code)
		}
	}

	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(tools.Tools) != 0 {
		t.Errorf("a checks key's tools: %d", len(tools.Tools))
	}
}
