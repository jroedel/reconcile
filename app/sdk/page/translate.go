package page

import (
	"fmt"
	"html/template"
	"regexp"
	"strings"

	"github.com/jroedel/reconcile/business/types"
)

// How a template asks for a string in the reader's language:
//
//	{{t "Upload receipts"}}
//	{{t "{count} receipts are waiting for a match" "count" .Waiting}}
//	{{tc "month-end" "Close"}}
//
// The first argument is the English, and it is the key the translation is
// stored under (translationbus). After it come pairs: a placeholder's name,
// then its value. A placeholder is written {name} in the English, and a
// translation may put it wherever its own grammar wants it -- which is why
// the names are words rather than positions. tc is t with a context first,
// for the rare English string that two places need translated differently.
//
// Both return a plain string, so html/template escapes it like anything else:
// a translation can never inject markup, and a sentence that needs a link in
// it is two strings and the link between them.
//
// Every argument but the values must be a literal string. That is checked at
// startup (extract.go), because a key built at run time is a key nobody can
// find to translate.

// marking is what the templates are parsed with. It is never executed: every
// Render binds translating in its place. It exists because a template that
// names a function must be parsed with one of that name.
var marking = template.FuncMap{
	"t":  func(en string, args ...any) (string, error) { return fill(en, en, args) },
	"tc": func(_, en string, args ...any) (string, error) { return fill(en, en, args) },
}

// translating is t and tc for one language.
func translating(tr Translator, lang types.Lang) template.FuncMap {
	return template.FuncMap{
		"t": func(en string, args ...any) (string, error) {
			return fill(en, tr.Translate(lang, "", en), args)
		},
		"tc": func(context, en string, args ...any) (string, error) {
			return fill(en, tr.Translate(lang, context, en), args)
		},
	}
}

// placeholder is {name}: a letter, then letters, digits and underscores.
var placeholder = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)\}`)

// fill puts the values into text, the translation of en.
//
// A name the English does not have, or one it has and is not given, is an
// error: the page fails rather than showing "{count}" to somebody. The names
// are checked against the English and not the translation, because the
// English is what the template was written against; a translation that
// leaves a placeholder out still renders, and is a translation to fix.
func fill(en, text string, args []any) (string, error) {
	if len(args)%2 != 0 {
		return "", fmt.Errorf("t %q: the values after the text come in pairs, a name and then its value", en)
	}

	values := make(map[string]string, len(args)/2)

	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return "", fmt.Errorf("t %q: argument %d should be a placeholder's name", en, i+2)
		}

		values[name] = fmt.Sprint(args[i+1])
	}

	want := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(en, -1) {
		want[m[1]] = true
	}

	for name := range values {
		if !want[name] {
			return "", fmt.Errorf("t %q: there is no {%s} in it", en, name)
		}
	}

	for name := range want {
		if _, ok := values[name]; !ok {
			return "", fmt.Errorf("t %q: {%s} is not given a value", en, name)
		}
	}

	if len(values) == 0 {
		return text, nil
	}

	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		if v, ok := values[strings.Trim(m, "{}")]; ok {
			return v
		}

		return m
	}), nil
}
