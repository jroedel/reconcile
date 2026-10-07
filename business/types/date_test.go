package types_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/types"
)

func TestParseDate(t *testing.T) {
	for in, want := range map[string]string{
		"2026-03-01":   "2026-03-01",
		" 2026-12-31 ": "2026-12-31",
		"":             "",
	} {
		d, err := types.ParseDate(in)
		if err != nil || d.String() != want {
			t.Errorf("ParseDate(%q) = %q, %v", in, d, err)
		}
	}

	for _, bad := range []string{"2026-02-30", "03/01/2026", "2026-3-1", "yesterday"} {
		if _, err := types.ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) succeeded", bad)
		}
	}

	a, _ := types.ParseDate("2026-01-31")
	b, _ := types.ParseDate("2026-02-01")

	if !a.Before(b) || b.Before(a) {
		t.Error("Before has the order wrong")
	}
}

func TestParseScopeKind(t *testing.T) {
	for _, ok := range []string{"org", "account", "project"} {
		if _, err := types.ParseScopeKind(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}

	if _, err := types.ParseScopeKind("category"); err == nil {
		t.Error("an unknown kind was accepted")
	}
}
