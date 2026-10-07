package page

import (
	"html/template"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Funcs are the template helpers every page has, besides t and tc
// (translate.go).
var Funcs = template.FuncMap{
	"sentence": Sentence,
	"short":    Short,
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
