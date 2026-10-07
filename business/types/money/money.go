// Package money is the strong type for an amount of money, held as a signed
// count of minor units (cents).
//
// Balances are never a float. 0.1 + 0.2 != 0.3, and across a few thousand
// ledger rows that error stops being academic — a monthly total drifts by a
// cent or two and no two runs agree. An int64 of cents covers roughly ±92
// quadrillion of them, which is comfortably more than anyone needs and exactly
// representable at every step.
//
// Sign carries direction and is part of the meaning: negative is money leaving
// an account, positive is money arriving. Aggregates that want a magnitude
// ("spending this month") negate at the point of aggregation rather than
// storing an unsigned value, so the raw row always says which way the money
// went.
//
// Only two-decimal currencies are modelled. JPY (zero minor units) and BHD
// (three) would need the scale to become a property of the currency rather
// than a constant, which is a change worth making when a real need for it
// arrives and not before.
package money

import (
	"cmp"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// centsPerUnit is the minor-unit scale.
const centsPerUnit = 100

// ErrInvalidAmount is returned by Parse for anything that is not an amount.
var ErrInvalidAmount = errors.New("money: not a valid amount")

// Amount is a signed quantity of currency minor units. The zero value is a
// legitimate amount — zero — rather than "unset", because a ledger row of zero
// is meaningful and a missing amount is caught by the parser instead.
type Amount int64

// FromCents builds an Amount from minor units.
func FromCents(cents int64) Amount { return Amount(cents) }

// Cents returns the amount as minor units. This is the form the storage layer
// persists, and the only lossless one.
func (a Amount) Cents() int64 { return int64(a) }

// Float64 converts to a float for charting and ratios only.
//
// Never round-trip a stored balance through this. It exists because an SVG
// coordinate is a float and a savings-rate percentage is a float; neither is a
// balance.
func (a Amount) Float64() float64 { return float64(a) / centsPerUnit }

// Abs returns the magnitude.
func (a Amount) Abs() Amount {
	if a < 0 {
		return -a
	}

	return a
}

// IsZero reports whether the amount is exactly zero.
func (a Amount) IsZero() bool { return a == 0 }

// Parse reads the amount shapes banks actually emit: "1,234.56", "$1234.56",
// "-12.30", "(45.00)" for a negative, and bare integers. Fractions longer than
// two digits round half away from zero.
//
// Currency symbols, thousands separators and padding are stripped. Any other
// unexpected character is an error rather than something to discard silently —
// quietly dropping letters would turn "12x3" into 123, and a parser for money
// is the wrong place to be forgiving.
//
// The '.' decimal separator is assumed. A European export ("1.234,56") parses
// to the wrong number rather than failing, so normalize one upstream.
func Parse(s string) (Amount, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return 0, fmt.Errorf("%w: empty string", ErrInvalidAmount)
	}

	// Accounting parentheses: "(45.00)" is -45.00.
	negative := false
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
		negative = true
		text = text[1 : len(text)-1]
	}

	cleaned, err := strip(text, s)
	if err != nil {
		return 0, err
	}

	switch {
	case strings.HasPrefix(cleaned, "-"):
		negative = !negative
		cleaned = cleaned[1:]
	case strings.HasPrefix(cleaned, "+"):
		cleaned = cleaned[1:]
	}

	if cleaned == "" {
		return 0, fmt.Errorf("%w: %q has no digits", ErrInvalidAmount, s)
	}

	whole, frac, _ := strings.Cut(cleaned, ".")
	if strings.Contains(frac, ".") {
		return 0, fmt.Errorf("%w: %q has more than one decimal point", ErrInvalidAmount, s)
	}

	whole = cmp.Or(whole, "0")
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}

	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is out of range", ErrInvalidAmount, s)
	}

	total := units*centsPerUnit + fractionToCents(frac)
	if negative {
		total = -total
	}

	return Amount(total), nil
}

// MustParse parses s and panics on failure; for tests and known-good constants.
func MustParse(s string) Amount {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}

	return a
}

// String renders a plain signed decimal with two places ("-1234.56"). This is
// the machine-readable form: the App layer puts it on the wire and Parse reads
// it back unchanged.
func (a Amount) String() string {
	sign, v := split(a)

	return fmt.Sprintf("%s%d.%02d", sign, v/centsPerUnit, v%centsPerUnit)
}

// Display renders a grouped, symbol-prefixed form ("-$1,234.56") for a human.
func (a Amount) Display() string {
	sign, v := split(a)

	return fmt.Sprintf("%s$%s.%02d", sign, group(v/centsPerUnit), v%centsPerUnit)
}

// MarshalJSON emits the decimal string, so a consumer parsing with a float
// cannot silently lose precision on the way back in.
func (a Amount) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, `"%s"`, a.String()), nil
}

// UnmarshalJSON accepts either a decimal string or a bare JSON number.
func (a *Amount) UnmarshalJSON(b []byte) error {
	v, err := Parse(strings.Trim(string(b), `"`))
	if err != nil {
		return err
	}

	*a = v

	return nil
}

// Value stores the amount as INTEGER cents.
//
// The storage layer flattens to int64 explicitly in its toDB converters, so
// this is a safety net for any query that binds an Amount directly rather than
// the primary mechanism.
func (a Amount) Value() (driver.Value, error) { return int64(a), nil }

// Scan reads INTEGER cents back.
func (a *Amount) Scan(src any) error {
	switch v := src.(type) {
	case int64:
		*a = Amount(v)
	case nil:
		*a = 0
	default:
		return fmt.Errorf("money: cannot scan %T into an Amount", src)
	}

	return nil
}

// strip removes currency symbols, separators and padding, rejecting anything
// else. original is carried only so the error can quote what the caller passed.
func strip(text, original string) (string, error) {
	var b strings.Builder

	for _, r := range text {
		switch {
		case r >= '0' && r <= '9', r == '.', r == '-', r == '+':
			b.WriteRune(r)
		case r == ',' || r == ' ' || r == ' ' || r == '\t':
			// thousands separator or padding
		case r == '$' || r == '€' || r == '£' || r == '¥':
			// currency symbol
		default:
			return "", fmt.Errorf("%w: unexpected %q in %q", ErrInvalidAmount, r, original)
		}
	}

	return b.String(), nil
}

// fractionToCents reads at most two fractional digits and rounds on the third.
//
// Parsing the whole fraction with ParseInt would overflow on a long one
// ("1.0000000000000000000000"), so only the digits that matter are read.
func fractionToCents(frac string) int64 {
	switch {
	case frac == "":
		return 0
	case len(frac) == 1:
		return int64(frac[0]-'0') * 10
	default:
		cents := int64(frac[0]-'0')*10 + int64(frac[1]-'0')
		if len(frac) > 2 && frac[2] >= '5' {
			cents++
		}

		return cents
	}
}

// split separates an amount into its sign and magnitude for formatting.
func split(a Amount) (sign string, magnitude int64) {
	if a < 0 {
		return "-", int64(-a)
	}

	return "", int64(a)
}

// group inserts thousands separators into a non-negative integer.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}

	var b strings.Builder

	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}

	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}

		b.WriteString(s[i : i+3])
	}

	return b.String()
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}
