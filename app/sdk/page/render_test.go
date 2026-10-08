package page_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/app/sdk/page"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
)

// spanish translates the strings it has, and leaves the rest in English, as
// translationbus does for a pending string.
type spanish map[translationbus.Source]string

func (s spanish) Translate(lang types.Lang, context, en string) string {
	if lang != types.Spanish {
		return en
	}

	if text, ok := s[translationbus.Source{Context: context, EN: en}]; ok {
		return text
	}

	return en
}

var tr = spanish{
	{EN: "Receipts"}: "Recibos",
	{EN: "{count} receipts are waiting for a match"}: "Hay {count} recibos esperando",
	{Context: "month-end", EN: "Close"}:              "Cierre",
	{EN: "Language"}:                                 "Idioma",
	{EN: "Reconcile is free software."}:              "Reconcile es software libre.",
	{EN: "<b>not bold</b>"}:                          "<b>tampoco</b>",
}

func renderer(t *testing.T, files fstest.MapFS) *page.Renderer {
	t.Helper()

	rn, err := page.NewRenderer(slog.New(slog.NewTextHandler(io.Discard, nil)), tr, files)
	if err != nil {
		t.Fatal(err)
	}

	return rn
}

func file(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

var pages = fstest.MapFS{
	"templates/receipts.html": file(`{{define "content"}}<h1>{{t "Receipts"}}</h1>
<p>{{t "{count} receipts are waiting for a match" "count" .Data}}</p>
{{if .Data}}<p>{{tc "month-end" "Close"}}</p>{{end}}
{{with .Data}}{{template "row" .}}{{end}}
<p>{{t "<b>not bold</b>"}}</p>{{end}}`),
	"templates/partials/row.html": file(`{{define "row"}}<li>{{t "One row"}}</li>{{end}}`),
}

func render(t *testing.T, rn *page.Renderer, lang string, name string, data any) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/receipts", nil)
	if lang != "" {
		r.AddCookie(&http.Cookie{Name: "lang", Value: lang})
	}

	rec := httptest.NewRecorder()

	mid.Lang()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rn.Render(w, r, http.StatusOK, name, data)
	})).ServeHTTP(rec, r)

	return rec
}

func TestRenderInTheReadersLanguage(t *testing.T) {
	rn := renderer(t, pages)

	en := render(t, rn, "en", "receipts", 3)
	es := render(t, rn, "es", "receipts", 3)
	pt := render(t, rn, "pt", "receipts", 3)

	for _, want := range []string{"<h1>Receipts</h1>", "3 receipts are waiting for a match", `<html lang="en">`} {
		if !strings.Contains(en.Body.String(), want) {
			t.Errorf("English page lacks %q", want)
		}
	}

	for _, want := range []string{"<h1>Recibos</h1>", "Hay 3 recibos esperando", "<p>Cierre</p>", `<html lang="es">`, `aria-label="Idioma"`} {
		if !strings.Contains(es.Body.String(), want) {
			t.Errorf("Spanish page lacks %q", want)
		}
	}

	// Nothing is translated into Portuguese here, so it is English -- in a
	// page that says it is Portuguese, which is what the reader asked for.
	if !strings.Contains(pt.Body.String(), "<h1>Receipts</h1>") || !strings.Contains(pt.Body.String(), `<html lang="pt">`) {
		t.Error("an untranslated string did not fall back to English")
	}

	if got := es.Header().Get("Content-Language"); got != "es" {
		t.Errorf("Content-Language = %q", got)
	}

	if got := es.Header().Get("Vary"); !strings.Contains(got, "Accept-Language") || !strings.Contains(got, "Cookie") {
		t.Errorf("Vary = %q; a cache could hand one reader's language to another", got)
	}
}

// Every language is offered in its own name, and the current one is not a
// link.
func TestRenderOffersEveryLanguage(t *testing.T) {
	body := render(t, renderer(t, pages), "es", "receipts", 1).Body.String()

	for _, want := range []string{
		`<span class="lang is-on" aria-current="true" lang="es">Español</span>`,
		`href="/receipts?lang=en"`, `>English</a>`,
		`href="/receipts?lang=pt"`, `>Português</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the language links lack %s", want)
		}
	}
}

// A translation is text, never markup: the template escapes it.
func TestRenderEscapesTranslations(t *testing.T) {
	body := render(t, renderer(t, pages), "es", "receipts", 1).Body.String()

	if strings.Contains(body, "<b>tampoco</b>") || !strings.Contains(body, "&lt;b&gt;tampoco&lt;/b&gt;") {
		t.Error("a translation reached the page as markup")
	}
}

// One request's language must never leak into another's: the parsed sets are
// cloned per request, and these run at once.
func TestRenderLanguagesDoNotLeakBetweenRequests(t *testing.T) {
	rn := renderer(t, pages)

	for range 20 {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			if !strings.Contains(render(t, rn, "es", "receipts", 1).Body.String(), "Recibos") {
				t.Error("a Spanish request was answered in English")
			}

			if !strings.Contains(render(t, rn, "en", "receipts", 1).Body.String(), "<h1>Receipts</h1>") {
				t.Error("an English request was answered in Spanish")
			}
		})
	}
}

// A template that fails halfway is a clean 500, not half a page under a 200.
func TestRenderFailsWhole(t *testing.T) {
	rn := renderer(t, fstest.MapFS{
		"templates/broken.html": file(`{{define "content"}}<p>{{t "Waiting for {count}"}}</p>{{end}}`),
	})

	rec := render(t, rn, "en", "broken", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("part of the page was sent")
	}
}

func TestRenderUnknownPage(t *testing.T) {
	if rec := render(t, renderer(t, pages), "en", "nowhere", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestStringsAreReadFromEveryTemplate(t *testing.T) {
	got := map[translationbus.Source]bool{}
	for _, s := range renderer(t, pages).Strings() {
		got[s] = true
	}

	for _, want := range []translationbus.Source{
		{EN: "Receipts"},
		{EN: "{count} receipts are waiting for a match"},
		{Context: "month-end", EN: "Close"}, // inside an {{if}}, with a context
		{EN: "One row"},                     // in a partial, reached through {{template}}
		{EN: "Language"},                    // the layout's own
	} {
		if !got[want] {
			t.Errorf("%+v was not found", want)
		}
	}
}

// A string that cannot be found cannot be translated, so it does not start.
func TestNewRendererRefusesAStringItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"a field":            `{{define "content"}}{{t .Data}}{{end}}`,
		"a context field":    `{{define "content"}}{{tc .Data "Close"}}{{end}}`,
		"a placeholder name": `{{define "content"}}{{t "Hello {name}" .Data "x"}}{{end}}`,
		"inside a pipe":      `{{define "content"}}{{if eq (t .Data) "x"}}y{{end}}{{end}}`,
		"no text at all":     `{{define "content"}}{{t}}{{end}}`,
	} {
		_, err := page.NewRenderer(slog.New(slog.NewTextHandler(io.Discard, nil)), tr, fstest.MapFS{
			"templates/p.html": file(body),
		})
		if err == nil {
			t.Errorf("%s: NewRenderer accepted it", name)
		}
	}
}

func TestStylesheetIsCachedForEverUnderItsHash(t *testing.T) {
	rn := renderer(t, pages)

	if !strings.HasPrefix(rn.StylesheetPath(), "/static/app.") {
		t.Fatalf("StylesheetPath = %q", rn.StylesheetPath())
	}

	rec := httptest.NewRecorder()
	rn.Stylesheet()(rec, httptest.NewRequest(http.MethodGet, rn.StylesheetPath(), nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	if !strings.Contains(render(t, rn, "en", "receipts", 1).Body.String(), rn.StylesheetPath()) {
		t.Error("the page does not link the stylesheet it serves")
	}
}

var withMail = fstest.MapFS{
	"templates/receipts.html":     pages["templates/receipts.html"],
	"templates/partials/row.html": pages["templates/partials/row.html"],
	"mail/code.txt": file(`{{define "subject"}}{{t "Your code"}}
{{end}}{{define "body"}}{{t "Your code is {code}." "code" .}}
<b>{{t "Plain text"}}</b>{{end}}`),
}

// A message is in the language asked for, its subject is one line, and it is
// not escaped for HTML: it is plain text.
func TestMailInALanguage(t *testing.T) {
	rn := renderer(t, withMail)

	es := spanish{{EN: "Your code"}: "Tu código", {EN: "Your code is {code}."}: "Tu código es {code}."}
	rn2, err := page.NewRenderer(slog.New(slog.NewTextHandler(io.Discard, nil)), es, withMail)
	if err != nil {
		t.Fatal(err)
	}

	subject, body, err := rn2.Mail(types.Spanish, "code", "123456")
	if err != nil {
		t.Fatal(err)
	}

	if subject != "Tu código" || !strings.Contains(body, "Tu código es 123456.") || !strings.Contains(body, "<b>Plain text</b>") {
		t.Errorf("subject %q, body %q", subject, body)
	}

	if _, _, err := rn.Mail(types.English, "nowhere", nil); err == nil {
		t.Error("an unknown message was sent")
	}

	got := map[translationbus.Source]bool{}
	for _, s := range rn.Strings() {
		got[s] = true
	}

	if !got[translationbus.Source{EN: "Your code is {code}."}] {
		t.Error("a message's strings were not registered for translation")
	}
}

func TestAMessageNeedsASubjectAndABody(t *testing.T) {
	_, err := page.NewRenderer(slog.New(slog.NewTextHandler(io.Discard, nil)), tr, fstest.MapFS{
		"templates/p.html": file(`{{define "content"}}x{{end}}`),
		"mail/m.txt":       file(`{{define "body"}}x{{end}}`),
	})
	if err == nil {
		t.Fatal("a message with no subject was accepted")
	}
}

// A text is written in the reader's language, its strings are registered,
// and nothing in it is escaped for HTML: it goes into a spreadsheet.
func TestAText(t *testing.T) {
	rn := renderer(t, fstest.MapFS{
		"templates/p.html": file(`{{define "content"}}x{{end}}`),
		"text/columns.txt": file("{{t \"Receipts\"}}\n{{t \"A & B\"}}\n"),
	})

	got, err := rn.Text(types.Spanish, "columns", nil)
	if err != nil || got != "Recibos\nA & B\n" {
		t.Errorf("the text: %q, %v", got, err)
	}

	if _, err := rn.Text(types.English, "nowhere", nil); err == nil {
		t.Error("an unknown text was written")
	}

	if !slices.Contains(rn.Strings(), translationbus.Source{EN: "A & B"}) {
		t.Error("a text's strings were not registered for translation")
	}
}
