package money

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1", 100},
		{"1.5", 150},
		{"1.05", 105},
		{"1234.56", 123456},
		{"1,234.56", 123456},
		{"$1,234.56", 123456},
		{"-12.30", -1230},
		{"(45.00)", -4500},
		{"($45.00)", -4500},
		{"(-45.00)", 4500}, // parens and sign both negate
		{"+7.25", 725},
		{" 8.99 ", 899},
		{".99", 99},
		{"1.", 100},
		{"1.005", 101},                    // rounds half away from zero
		{"1.004", 100},                    // rounds down
		{"-1.005", -101},                  // magnitude rounds, then the sign applies
		{"1.0000000000000000000000", 100}, // a long fraction must not overflow
	}

	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)

			continue
		}

		if got.Cents() != c.want {
			t.Errorf("Parse(%q) = %d cents, want %d", c.in, got.Cents(), c.want)
		}
	}
}

// "12x3" is the case that matters: silently stripping the letter would yield
// 123 and put a wrong number in the ledger with no error to notice.
func TestParseRejectsJunk(t *testing.T) {
	for _, in := range []string{"", "   ", "abc", "$", "1.2.3", "12x3", "1.2 USD", "5%"} {
		got, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, got)

			continue
		}

		if !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("Parse(%q) gave %v, want ErrInvalidAmount", in, err)
		}
	}
}

func TestDisplay(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{123456, "$1,234.56"},
		{-123456, "-$1,234.56"},
		{100000000, "$1,000,000.00"},
		{99999, "$999.99"},
		{-5, "-$0.05"},
	}

	for _, c := range cases {
		if got := FromCents(c.cents).Display(); got != c.want {
			t.Errorf("FromCents(%d).Display() = %q, want %q", c.cents, got, c.want)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 1, -1, 99, -99, 123456, -123456, 100000000} {
		a := FromCents(cents)

		back, err := Parse(a.String())
		if err != nil {
			t.Fatalf("Parse(%q): %v", a.String(), err)
		}

		if back != a {
			t.Errorf("round trip of %d cents gave %d", cents, back.Cents())
		}
	}
}

// Marshalling as a float would let a JSON consumer reintroduce exactly the
// imprecision the type exists to avoid.
func TestJSONIsAStringNotAFloat(t *testing.T) {
	b, err := json.Marshal(FromCents(-123456))
	if err != nil {
		t.Fatal(err)
	}

	if string(b) != `"-1234.56"` {
		t.Errorf("marshalled to %s, want a quoted decimal", b)
	}

	var back Amount
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}

	if back.Cents() != -123456 {
		t.Errorf("round trip gave %d cents", back.Cents())
	}
}

func TestAbsAndIsZero(t *testing.T) {
	if got := FromCents(-500).Abs(); got.Cents() != 500 {
		t.Errorf("Abs = %d", got.Cents())
	}

	if got := FromCents(500).Abs(); got.Cents() != 500 {
		t.Errorf("Abs = %d", got.Cents())
	}

	if !FromCents(0).IsZero() {
		t.Error("zero is not IsZero")
	}

	if FromCents(1).IsZero() {
		t.Error("one cent reported IsZero")
	}
}

// Integer cents are exact where floats are not; this is the whole reason for
// the type.
func TestSummationIsExact(t *testing.T) {
	var total Amount
	for range 10 {
		total += MustParse("0.10")
	}

	if total != MustParse("1.00") {
		t.Errorf("ten dimes summed to %s, want $1.00", total)
	}
}

func TestScanAndValue(t *testing.T) {
	var a Amount
	if err := a.Scan(int64(-1230)); err != nil {
		t.Fatal(err)
	}

	if a.Cents() != -1230 {
		t.Errorf("Scan gave %d cents", a.Cents())
	}

	if err := a.Scan(nil); err != nil {
		t.Fatal(err)
	}

	if !a.IsZero() {
		t.Error("scanning NULL should yield zero")
	}

	if err := a.Scan("nope"); err == nil {
		t.Error("scanning a string should fail")
	}

	v, err := FromCents(42).Value()
	if err != nil {
		t.Fatal(err)
	}

	if v != int64(42) {
		t.Errorf("Value = %v, want int64(42)", v)
	}
}
