package types

import (
	"fmt"
	"strings"
)

// Lang is a language the app speaks.
//
// English is the language the copy is written in; Spanish and Portuguese are
// translated from it (docs/plan.md, "Translations"). Adding one is adding it
// here, to Langs, and to the language links in the layout: the translation
// tables take any of them, and every string already registered gets a
// pending row for it at the next startup.
type Lang string

const (
	English    Lang = "en"
	Spanish    Lang = "es"
	Portuguese Lang = "pt"
)

// Langs is every language, English first.
var Langs = []Lang{English, Spanish, Portuguese}

// Translated is every language but English: the ones a string waits to be
// translated into.
var Translated = []Lang{Spanish, Portuguese}

// ParseLang reads a language from outside -- a query parameter, a cookie, a
// form field -- and refuses anything it does not speak.
func ParseLang(s string) (Lang, error) {
	switch l := Lang(strings.ToLower(strings.TrimSpace(s))); l {
	case English, Spanish, Portuguese:
		return l, nil
	}

	return "", fmt.Errorf("%q is not a language this app speaks; use en, es or pt", s)
}

// Name is the language's name in itself, for a link that switches to it: a
// reader who cannot read the current page can still find their own.
func (l Lang) Name() string {
	switch l {
	case Spanish:
		return "Español"
	case Portuguese:
		return "Português"
	}

	return "English"
}
