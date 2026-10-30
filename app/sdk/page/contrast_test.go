package page_test

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	token     = regexp.MustCompile(`--([a-z-]+):\s*(#[0-9a-fA-F]{6});`)
	rootBlock = regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)
)

// tokens is the colours one :root block of the stylesheet defines.
func tokens(block string) map[string]string {
	out := map[string]string{}
	for _, m := range token.FindAllStringSubmatch(block, -1) {
		out[m[1]] = m[2]
	}

	return out
}

// luminance is a colour's relative luminance, as WCAG 2 defines it.
func luminance(t *testing.T, hex string) float64 {
	t.Helper()

	var c [3]float64
	for i := range c {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("%s: %v", hex, err)
		}

		s := float64(v) / 255
		if s <= 0.04045 {
			c[i] = s / 12.92
		} else {
			c[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}

	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()

	la, lb := luminance(t, a), luminance(t, b)

	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// The words the accountant's pages put on their colours (#151) can be read
// in both schemes: the text on the notices' and badges' yellow and green,
// the hint lines that say why a row is listed and what it was given, and
// the links and buttons everything is reached by, all at WCAG's 4.5:1 for
// small text. The rule under the lists' tabs is not held to the 3:1 a
// mark needs when it alone shows a state, because it does not alone: the
// shown tab is set apart by weight and by its words, so the stylesheet is
// checked to say that instead. And the dark scheme redefines every colour
// the light one has, so that none is left light on dark.
func TestTheColoursCanBeRead(t *testing.T) {
	css, err := os.ReadFile("assets/app/app.css")
	if err != nil {
		t.Fatal(err)
	}

	sheet := string(css)

	light := rootBlock.FindStringSubmatch(sheet)
	_, after, ok := strings.Cut(sheet, "@media (prefers-color-scheme: dark)")
	if light == nil || !ok {
		t.Fatal("no :root block, or no dark scheme")
	}

	dark := rootBlock.FindStringSubmatch(after)
	if dark == nil {
		t.Fatal("the dark scheme has no :root block")
	}

	schemes := map[string]map[string]string{"light": tokens(light[1]), "dark": tokens(dark[1])}

	for name := range schemes["light"] {
		if _, ok := schemes["dark"][name]; !ok {
			t.Errorf("--%s has no dark value", name)
		}
	}

	pairs := []struct{ fg, bg string }{
		{"ink", "warn-bg"}, {"ink", "ok-bg"}, {"ink", "surface"}, {"ink", "bg"},
		{"muted", "warn-bg"}, {"muted", "ok-bg"}, {"muted", "surface"}, {"muted", "bg"},
		{"accent", "surface"}, {"accent", "bg"}, {"accent-ink", "accent"},
	}

	for scheme, colours := range schemes {
		for _, p := range pairs {
			fg, bg := colours[p.fg], colours[p.bg]
			if fg == "" || bg == "" {
				t.Errorf("%s: --%s or --%s is not defined", scheme, p.fg, p.bg)
				continue
			}

			if r := contrast(t, fg, bg); r < 4.5 {
				t.Errorf("%s: --%s on --%s is %.2f:1, want at least 4.5:1", scheme, p.fg, p.bg, r)
			}
		}
	}

	if !regexp.MustCompile(`\.tabs a\[aria-current\]\s*\{[^}]*font-weight:\s*(600|700|bold)`).MatchString(sheet) {
		t.Error("the shown tab is not set apart by weight, so its colour would be all that shows it")
	}
}
