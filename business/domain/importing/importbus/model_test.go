package importbus_test

import (
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
)

// A check's number is its digits, without padding; anything else is no
// check number.
func TestCheckNumber(t *testing.T) {
	for in, want := range map[string]string{
		"1176":      "1176",
		" 0001176 ": "1176",
		"#1176":     "1176",
		"":          "",
		"0000":      "",
		"DS-41":     "",
		"1176A":     "",
		// Eleven digits: longer than any check number, and the shape of
		// an account's.
		strings.Repeat("1", 11): "",
	} {
		if got := importbus.CheckNumber(in); got != want {
			t.Errorf("CheckNumber(%q) = %q, want %q", in, got, want)
		}
	}

	if got := importbus.CheckUnsaid("1176", "Check 1176"); got != "" {
		t.Errorf("a description that says it: %q", got)
	}

	if got := importbus.CheckUnsaid("1176", "CHECK"); got != "1176" {
		t.Errorf("a description that does not: %q", got)
	}
}

func TestLast4(t *testing.T) {
	for number, want := range map[string]string{
		"000123456789":     "6789",
		"XXXXXXXXXXXX4242": "4242",
		"1234 5678-9012":   "9012",
		"****12":           "",
		"":                 "",
	} {
		if got := importbus.Last4(number); got != want {
			t.Errorf("%q: %q", number, got)
		}
	}
}
