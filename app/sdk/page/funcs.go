package page

import (
	"errors"
	"fmt"
	"html/template"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/types/money"
)

// Funcs are the template helpers every page has, besides t and tc
// (translate.go).
var Funcs = template.FuncMap{
	"sentence": Sentence,
	"short":    Short,
	"money":    Money,

	// amount reads an amount kept as text, as the history keeps one.
	// Unreadable is zero rather than a failed page: the history is read
	// long after it was written.
	"amount": func(s string) money.Amount {
		a, _ := money.Parse(s)

		return a
	},

	// dict gathers named values for a partial that needs more than one:
	// {{template "x" (dict "Receipt" . "Back" "/receipts")}}.
	"dict": Dict,

	// trail is a page's breadcrumbs, as pairs of a name and a page:
	// {{template "crumbs" (trail $ .Account.Name $account (t "Month by month") $months)}}.
	"trail": Trail,

	// inc counts a range's index from one, as a person counts.
	"inc": func(i int) int { return i + 1 },

	// join lists names, such as files', for a placeholder of a sentence
	// that is translated whole: "{files}", not a sentence built of parts.
	"join": strings.Join,
}

// Crumb is one step of a page's breadcrumbs: a name, and the page it opens.
type Crumb struct {
	Name string
	Path string
}

// Trail is the way up from a page, from the widest place down: pairs of a
// name and the page it opens. A step that is the top bar's place is left
// out, because the bar already has it one tap away; so is an empty name,
// for a step a reader may not see. Each word opens one page wherever it
// appears -- an account's name its page, a month that month's
// transactions -- which the crumbs written page by page did not: an
// account's name opened three different pages, by which page it was on.
func Trail(s Shell, pairs ...string) ([]Crumb, error) {
	if len(pairs)%2 != 0 {
		return nil, errors.New("trail takes pairs of a name and a page")
	}

	var crumbs []Crumb

	for i := 0; i < len(pairs); i += 2 {
		c := Crumb{Name: pairs[i], Path: pairs[i+1]}
		if c.Name == "" || (s.Place != nil && c.Path == s.Place.Path) {
			continue
		}

		crumbs = append(crumbs, c)
	}

	return crumbs, nil
}

// symbols are the currencies whose sign a reader expects in front. Any other
// is written as its code, which is never wrong, only less familiar.
var symbols = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "BRL": "R$", "MXN": "MX$", "CAD": "CA$", "AUD": "A$",
	"ARS": "AR$", "CLP": "CLP$", "COP": "COL$", "PYG": "₲", "UYU": "$U", "CHF": "CHF ", "JPY": "¥",
}

// Money is an amount as a person reads it: the minus sign first, the
// currency's symbol, grouped digits and two decimals (docs/design.md, "Money
// looks like money"). Grouped the English way in every language for now;
// "1.234,56" for Spanish and Portuguese readers is a later version's.
func Money(a money.Amount, currency string) string {
	symbol, ok := symbols[currency]
	if !ok {
		symbol = currency + " "
	}

	return strings.Replace(a.Display(), "$", symbol, 1)
}

// ShortLen is how much of an ID a person is shown: as much as anybody needs
// to say which one, in a conversation or a message. Eight hex characters, as
// git shortens a commit.
const ShortLen = 8

// Short is an ID as a person is shown it: its first ShortLen characters.
func Short(id string) string {
	if len(id) <= ShortLen {
		return id
	}

	return id[:ShortLen]
}

// Sentence makes a rule's problem into something a page can show on its own:
// a capital at the start and a full stop at the end.
//
// The rules write their problems as the continuation of a sentence -- "give
// the account a name" -- because that is also how they read inside an error
// chain in a log.
func Sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]

	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		s += "."
	}

	return s
}

// Dict is pairs of a name and a value as a map. An odd count or a name that
// is not a string is a mistake in a template, and fails the render that has
// it rather than passing a partial half its data.
func Dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, errors.New("dict takes pairs of a name and a value")
	}

	out := make(map[string]any, len(pairs)/2)

	for i := 0; i < len(pairs); i += 2 {
		name, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: %v is not a name", pairs[i])
		}

		out[name] = pairs[i+1]
	}

	return out, nil
}
