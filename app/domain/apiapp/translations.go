package apiapp

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/web"
)

// How many a pending list gives by default: a batch Claude can translate in
// one answer. translationbus.MaxBatch is the most, either way.
const pendingDefault = 50

func (a app) translationEndpoints() []Endpoint {
	lang := Field{Name: "lang", Type: "string", Required: true, Values: []string{"es", "pt"}, Description: "The language: es for Spanish, pt for Portuguese."}

	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/translations/pending", Tool: "list_pending_translations", NeedsKey: true,
			Query:   []Field{lang, {Name: "limit", Type: "integer", Description: fmt.Sprintf("How many to give, %d by default and %d at most.", pendingDefault, translationbus.MaxBatch)}},
			Summary: "The interface's strings waiting for a translation into one language: those a reviewer sent back first, with their note and the translation there now, then those with none. Each is its English, a context that tells apart two strings with the same English (usually empty), and the template files it is written in, which say where it appears. The glossary is how the app's own terms are translated already; use the same words.",
			Returns: `{"lang", "pending": [{context, en, pages: [string], current, note}], "remaining": n, "glossary": [{en, text}]}. current and note only for one sent back. remaining counts the ones given too; ask again until it is 0.`,
			handler: a.pendingTranslations,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/translations", Tool: "put_translations", NeedsKey: true,
			Summary: "Translate: send each string's translation into one language. Each is on every page at once. One a person approved or corrected is refused unless sent unchanged; say what you would change instead. One refused does not stop the rest. Sending the same translation again changes nothing.",
			Body: &Body{Encoding: "json", Fields: []Field{
				lang,
				{Name: "translations", Type: "array of object {context, en, text}", Required: true, Description: fmt.Sprintf("At most %d. context and en exactly as the pending list gave them; text the translation, with every {placeholder} kept as it is.", translationbus.MaxBatch)},
			}},
			Returns: `200 {"results": [{context, en, outcome: "created", "updated", "unchanged" or "refused", problem}]}, in the order sent.`,
			handler: a.putTranslations,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/translations", Tool: "list_translations", NeedsKey: true,
			Query: []Field{
				lang,
				{Name: "status", Type: "string", Values: []string{"pending", "draft", "approved"}, Description: "pending for none yet, draft for written and not yet looked over, approved for looked over by a person; leave it out for all."},
				{Name: "limit", Type: "integer", Description: "How many to give, 100 by default and 500 at most."},
				{Name: "offset", Type: "integer", Description: "How many to skip, for the next page."},
			},
			Summary: "The translations of one language, in the order of their English, with who wrote each. For revising some when a person asks, not for translating what is waiting: list_pending_translations is that.",
			Returns: `{"translations": [{context, en, text, status, origin: "claude", "person" or "", note, pages: [string], updated_at}], "total": n}`,
			handler: a.listTranslations,
		},
	}
}

// translator is who the key belongs to, as the translation rules ask it.
func translator(r *http.Request) translationbus.Translator {
	u, _ := mid.UserFrom(r.Context())

	return translationbus.Translator{ID: u.ID, SiteAdmin: u.SiteAdmin}
}

// language reads the lang a request names, answering 400 itself when it is
// not one that is translated.
func language(w http.ResponseWriter, s string) (types.Lang, bool) {
	l, err := types.ParseLang(s)
	if err != nil || l == types.English {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("lang", "Give lang as es for Spanish or pt for Portuguese."))

		return "", false
	}

	return l, true
}

// refused answers the errors that refuse a whole request.
func (a app) refused(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, translationbus.ErrForbidden):
		web.WriteJSON(w, http.StatusForbidden, web.Problem("lang",
			"The person this key belongs to does not translate that language. The site administrator decides who does."))
	case errors.Is(err, translationbus.ErrInvalid):
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("", err.Error()))
	default:
		a.fail(w, r, what, err)
	}
}

// PendingJSON is a string waiting for its translation.
type PendingJSON struct {
	Context string   `json:"context"`
	EN      string   `json:"en"`
	Pages   []string `json:"pages"`
	Current string   `json:"current,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// TermJSON is a glossary entry.
type TermJSON struct {
	EN   string `json:"en"`
	Text string `json:"text"`
}

func (a app) pendingTranslations(w http.ResponseWriter, r *http.Request) {
	lang, ok := language(w, r.URL.Query().Get("lang"))
	if !ok {
		return
	}

	limit, ok := a.number(w, r, "limit", pendingDefault, 1, translationbus.MaxBatch)
	if !ok {
		return
	}

	got, err := a.translations.Pending(r.Context(), translator(r), lang, limit)
	if err != nil {
		a.refused(w, r, "listing the translations waiting", err)

		return
	}

	pending := make([]PendingJSON, 0, len(got.Items))
	for _, t := range got.Items {
		p := PendingJSON{Context: t.Context, EN: t.EN, Pages: nonNil(t.Pages)}
		if t.Note != "" {
			p.Current, p.Note = t.Text, t.Note
		}

		pending = append(pending, p)
	}

	glossary := make([]TermJSON, 0, len(got.Glossary))
	for _, g := range got.Glossary {
		glossary = append(glossary, TermJSON(g))
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"lang": lang, "pending": pending, "remaining": got.Remaining, "glossary": glossary})
}

// TranslationIn is one translation in a PUT.
type TranslationIn struct {
	Context string `json:"context"`
	EN      string `json:"en"`
	Text    string `json:"text"`
}

// ResultJSON is what became of one.
type ResultJSON struct {
	Context string `json:"context"`
	EN      string `json:"en"`
	Outcome string `json:"outcome"`
	Problem string `json:"problem,omitempty"`
}

// problems says what each refusal means, for the program that sent it.
var problems = map[string]string{
	translationbus.ProblemUnknown:      "There is no such string. Ask for the pending list again: the English may have changed since.",
	translationbus.ProblemEmpty:        "The translation is empty.",
	translationbus.ProblemLength:       "The translation is far longer than the English. Say it more briefly.",
	translationbus.ProblemPlaceholders: "Keep every {placeholder} of the English exactly as it is, untranslated, and add none.",
	translationbus.ProblemMarkup:       "A translation is text: take out the < and >.",
	translationbus.ProblemApproved:     "A person approved or corrected this translation, so it is not changed through the API. Tell them what you would change.",
}

func (a app) putTranslations(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lang         string          `json:"lang"`
		Translations []TranslationIn `json:"translations"`
	}

	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", "The body is not the JSON this takes: "+err.Error()))

		return
	}

	lang, ok := language(w, in.Lang)
	if !ok {
		return
	}

	if len(in.Translations) == 0 {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("translations", "Send at least one translation, as {context, en, text}."))

		return
	}

	batch := make([]translationbus.Written, 0, len(in.Translations))
	for _, t := range in.Translations {
		batch = append(batch, translationbus.Written{Source: translationbus.Source{Context: t.Context, EN: t.EN}, Text: t.Text})
	}

	who := translator(r)

	results, err := a.translations.Write(r.Context(), time.Now(), who, lang, batch)
	if err != nil {
		a.refused(w, r, "keeping translations", err)

		return
	}

	out := make([]ResultJSON, 0, len(results))
	counts := map[translationbus.Outcome]int{}

	for _, res := range results {
		counts[res.Outcome]++
		out = append(out, ResultJSON{Context: res.Context, EN: res.EN, Outcome: string(res.Outcome), Problem: problems[res.Problem]})
	}

	a.log.InfoContext(r.Context(), "translations kept", "lang", string(lang),
		"created", counts[translationbus.Created], "updated", counts[translationbus.Updated],
		"unchanged", counts[translationbus.Unchanged], "refused", counts[translationbus.Refused],
		"user_id", who.ID.String())

	web.WriteJSON(w, http.StatusOK, map[string]any{"results": out})
}

// TranslationJSON is a translation as the API shows it.
type TranslationJSON struct {
	Context   string    `json:"context"`
	EN        string    `json:"en"`
	Text      string    `json:"text"`
	Status    string    `json:"status"`
	Origin    string    `json:"origin"`
	Note      string    `json:"note,omitempty"`
	Pages     []string  `json:"pages"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a app) listTranslations(w http.ResponseWriter, r *http.Request) {
	lang, ok := language(w, r.URL.Query().Get("lang"))
	if !ok {
		return
	}

	limit, ok := a.number(w, r, "limit", 100, 1, 500)
	if !ok {
		return
	}

	offset, ok := a.number(w, r, "offset", 0, 0, 1<<30)
	if !ok {
		return
	}

	status := translationbus.Status(r.URL.Query().Get("status"))

	all, total, err := a.translations.List(r.Context(), translator(r), lang, status, limit, offset)
	if err != nil {
		a.refused(w, r, "listing translations", err)

		return
	}

	out := make([]TranslationJSON, 0, len(all))
	for _, t := range all {
		out = append(out, TranslationJSON{
			Context: t.Context, EN: t.EN, Text: t.Text, Status: string(t.Status), Origin: string(t.Origin),
			Note: t.Note, Pages: nonNil(t.Pages), UpdatedAt: t.UpdatedAt.UTC(),
		})
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"translations": out, "total": total})
}

// nonNil is a list that is [] in JSON rather than null when empty.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
