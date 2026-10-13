package page_test

import (
	"slices"
	"testing"

	"github.com/jroedel/reconcile/app/sdk/page"
)

// A trail leaves out what the top bar already names, and a step a reader
// may not see, which arrives with no name.
func TestTrailLeavesOutTheTopBarsPlace(t *testing.T) {
	t.Parallel()

	s := page.Shell{Place: &page.Place{Name: "St. Joseph Parish", Path: "/orgs/1"}}

	got, err := page.Trail(s, "St. Joseph Parish", "/orgs/1", "Parish checking", "/accounts/2", "", "/projects/3", "2026-07", "/accounts/2/transactions?month=2026-07")
	if err != nil {
		t.Fatal(err)
	}

	want := []page.Crumb{{Name: "Parish checking", Path: "/accounts/2"}, {Name: "2026-07", Path: "/accounts/2/transactions?month=2026-07"}}
	if !slices.Equal(got, want) {
		t.Errorf("trail = %v, want %v", got, want)
	}

	if got, _ := page.Trail(page.Shell{}, "Parish checking", "/accounts/2"); len(got) != 1 {
		t.Errorf("with no place in the bar, trail = %v", got)
	}

	if _, err := page.Trail(s, "Parish checking"); err == nil {
		t.Error("a name with no page was taken")
	}
}
