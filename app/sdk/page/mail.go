package page

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	texttemplate "text/template"

	"github.com/jroedel/reconcile/business/types"
)

// A message the app sends is a file in an app's mail directory, defining two
// templates:
//
//	{{define "subject"}}{{t "Your sign-in code"}}{{end}}
//	{{define "body"}}{{t "Your code to sign in to Reconcile is:"}} ...{{end}}
//
// t and tc work as they do on a page, and their strings are registered for
// translation with the pages' (extract.go), so a message is sent in the
// reader's language as soon as somebody has translated it, and in English
// until then.

// parseMails reads every app's mail/*.txt, each into a set of its own for the
// reason pages are: every one defines "subject" and "body".
func parseMails(own []fs.FS) (map[string]*texttemplate.Template, error) {
	mails := map[string]*texttemplate.Template{}

	for _, fsys := range own {
		names, err := fs.Glob(fsys, "mail/*.txt")
		if err != nil {
			return nil, fmt.Errorf("the mail templates could not be listed: %w", err)
		}

		for _, name := range names {
			set, err := texttemplate.New(path.Base(name)).Funcs(texttemplate.FuncMap(marking)).ParseFS(fsys, name)
			if err != nil {
				return nil, fmt.Errorf("%s could not be read: %w", name, err)
			}

			for _, part := range []string{"subject", "body"} {
				if set.Lookup(part) == nil {
					return nil, fmt.Errorf("%s defines no %q", name, part)
				}
			}

			mail := strings.TrimSuffix(path.Base(name), ".txt")

			if _, taken := mails[mail]; taken {
				return nil, fmt.Errorf("two apps both define a %s message", mail)
			}

			mails[mail] = set
		}
	}

	return mails, nil
}

// Mail writes one message in a language: its subject, on one line, and its
// plain-text body.
//
// The language is the caller's to choose rather than the request's, because
// a message is read by whoever it is sent to -- usually the person asking,
// but a notice to an address somebody has just left is read by its owner.
func (rn *Renderer) Mail(lang types.Lang, name string, data any) (subject, body string, err error) {
	original, ok := rn.mails[name]
	if !ok {
		return "", "", fmt.Errorf("there is no %s message", name)
	}

	set, err := original.Clone()
	if err != nil {
		return "", "", err
	}

	set.Funcs(texttemplate.FuncMap(translating(rn.tr, lang)))

	var subj, text strings.Builder

	if err := set.ExecuteTemplate(&subj, "subject", data); err != nil {
		return "", "", fmt.Errorf("the %s message's subject: %w", name, err)
	}

	if err := set.ExecuteTemplate(&text, "body", data); err != nil {
		return "", "", fmt.Errorf("the %s message: %w", name, err)
	}

	// A subject is one header line. A translation that wrapped it would
	// otherwise be refused by the sender at the moment somebody is waiting
	// for a code.
	subject = strings.Join(strings.Fields(subj.String()), " ")
	if subject == "" {
		return "", "", errors.New("the " + name + " message has an empty subject")
	}

	return subject, strings.TrimSpace(text.String()) + "\n", nil
}
