package page

import (
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
