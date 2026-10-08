package page

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	texttemplate "text/template"

	"github.com/jroedel/reconcile/business/types"
)

// A text is words the app writes somewhere other than a page or a message:
// the header row of a spreadsheet in a download, say. It is a file in an
// app's text directory, and the file is the template:
//
//	{{t "Date"}}
//	{{t "Account"}}
//
// Without this, words like those would be English in a handler, missing from
// ui_strings, and never translated. t and tc work as they do on a page, and
// the strings are registered with the pages' (extract.go).

// parseTexts reads every app's text/*.txt.
func parseTexts(own []fs.FS) (map[string]*texttemplate.Template, error) {
	texts := map[string]*texttemplate.Template{}

	for _, fsys := range own {
		names, err := fs.Glob(fsys, "text/*.txt")
		if err != nil {
			return nil, fmt.Errorf("the text templates could not be listed: %w", err)
		}

		for _, name := range names {
			set, err := texttemplate.New(path.Base(name)).Funcs(texttemplate.FuncMap(marking)).ParseFS(fsys, name)
			if err != nil {
				return nil, fmt.Errorf("%s could not be read: %w", name, err)
			}

			text := strings.TrimSuffix(path.Base(name), ".txt")

			if _, taken := texts[text]; taken {
				return nil, fmt.Errorf("two apps both define a %s text", text)
			}

			texts[text] = set
		}
	}

	return texts, nil
}

// Text writes one text in a language.
func (rn *Renderer) Text(lang types.Lang, name string, data any) (string, error) {
	original, ok := rn.texts[name]
	if !ok {
		return "", fmt.Errorf("there is no %s text", name)
	}

	set, err := original.Clone()
	if err != nil {
		return "", err
	}

	set.Funcs(texttemplate.FuncMap(translating(rn.tr, lang)))

	var out strings.Builder

	if err := set.ExecuteTemplate(&out, name+".txt", data); err != nil {
		return "", fmt.Errorf("the %s text: %w", name, err)
	}

	return out.String(), nil
}
