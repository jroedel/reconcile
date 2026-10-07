package page

import "testing"

func TestFill(t *testing.T) {
	for _, tt := range []struct {
		name, en, text string
		args           []any
		want           string
	}{
		{"no placeholders", "Receipts", "Recibos", nil, "Recibos"},
		{"one", "{n} waiting", "Hay {n} esperando", []any{"n", 3}, "Hay 3 esperando"},
		{"moved by the translation", "{who} uploaded {n}", "{n} subidos por {who}", []any{"who", "Ana", "n", 2}, "2 subidos por Ana"},
		{"twice", "{n} and {n}", "{n} y {n}", []any{"n", 1}, "1 y 1"},
		{"a translation that dropped one still renders", "{n} waiting", "Esperando", []any{"n", 3}, "Esperando"},
		{"braces that are not a placeholder", "{ok} {not one}", "{ok} {not one}", []any{"ok", "x"}, "x {not one}"},
	} {
		got, err := fill(tt.en, tt.text, tt.args)
		if err != nil || got != tt.want {
			t.Errorf("%s: fill = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

// Each of these would show a reader "{count}", or a number with no words
// around it, so the page fails instead.
func TestFillRefuses(t *testing.T) {
	for name, args := range map[string][]any{
		"a placeholder not given":     nil,
		"a name the English lacks":    {"n", 1, "extra", 2},
		"an odd number of arguments":  {"n"},
		"a name that is not a string": {1, 2},
	} {
		if _, err := fill("{n} waiting", "{n} waiting", args); err == nil {
			t.Errorf("%s: fill succeeded", name)
		}
	}
}
