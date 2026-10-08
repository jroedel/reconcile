// Package translationbus holds the interface's words in every language the
// app speaks, and answers what a string is in one of them.
//
// # How a string gets here
//
// Copy is written in English, in the templates, as {{t "Upload receipts"}}.
// At startup the renderer reads every such call out of the parsed templates
// (app/sdk/page, extract.go) and hands them to Register, which records each
// string once and opens a pending translation for it in every other language.
// Nothing has to be listed by hand, so nothing can be forgotten: a string is
// waiting to be translated from the first time a binary carrying it starts.
//
// The translating itself is done mostly by Claude, through the API
// (claude.go), and looked over afterwards by a person (review.go). That is
// why the status has three values rather than two: pending is nothing yet,
// draft is a translation nobody has checked, approved is one somebody has.
//
// # What a reader sees
//
// A draft is shown, not only an approved translation. The readers who need
// Spanish or Portuguese are better served by a translation that has not been
// checked than by English, and a mistake in one is a sentence somebody
// reports rather than a number that is wrong. A pending string -- no text
// yet -- is shown in English.
//
// # Keys
//
// A string is keyed by its English text and an optional context, as gettext
// does. The English is the key because it is what the template says; a
// separate identifier would be one more thing to keep in step. The context
// tells apart two strings that are the same in English and not in Spanish --
// "Close" the verb and "Close" the month-end -- and is empty for nearly all.
// Changing the English is a new string, which is right: the old translation
// translated something else.
package translationbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jroedel/reconcile/business/types"
)

// Source is one string as the code writes it.
type Source struct {
	Context string
	EN      string
}

// Use is a string the running binary says, and the template files it is
// written in.
type Use struct {
	Source
	Pages []string
}

// Uses is sources with no files, for tests and callers that know none.
func Uses(sources ...Source) []Use {
	out := make([]Use, len(sources))
	for i, s := range sources {
		out[i] = Use{Source: s}
	}

	return out
}

// Status is how far a translation has got.
type Status string

const (
	Pending  Status = "pending"
	Draft    Status = "draft"
	Approved Status = "approved"
)

// Origin is who wrote a translation's text.
type Origin string

const (
	// ByClaude is a translation written through the API.
	ByClaude Origin = "claude"

	// ByPerson is one a person wrote or corrected on the review screen.
	ByPerson Origin = "person"
)

// Translation is one string in one language.
type Translation struct {
	Source
	Lang      types.Lang
	Text      string
	Status    Status
	Origin    Origin
	UpdatedBy types.ID

	// Note is a reviewer's reason for sending it back, or "".
	Note string

	// Pages is the template files the string is written in.
	Pages []string

	UpdatedAt time.Time
}

// Storer is what this package needs from storage.
type Storer interface {
	// Register records every string and the files it is in, and opens a
	// pending translation for each of them in each of langs, keeping
	// whatever is already there.
	Register(ctx context.Context, uses []Use, langs []types.Lang, now time.Time) error

	// Translated is every translation that has text: drafts and approved.
	Translated(ctx context.Context) ([]Translation, error)

	// Pending is up to limit translations of a language with no text, or
	// sent back with a note, of strings seen since since: the sent-back
	// first. And how many there are in all.
	Pending(ctx context.Context, lang types.Lang, since time.Time, limit int) ([]Translation, int, error)

	// Get is one translation, ErrNotFound if the string or the language
	// has none.
	Get(ctx context.Context, src Source, lang types.Lang) (Translation, error)

	// Put writes a translation's text, status, origin and who, and clears
	// its note.
	Put(ctx context.Context, t Translation) error

	// List is a page of a language's translations with a status, or every
	// status for "", in the order of their English, and how many in all.
	List(ctx context.Context, lang types.Lang, status Status, limit, offset int) ([]Translation, int, error)

	// Queue is a page of one review queue of a language's strings seen
	// since since, in the order of their English, and how many in all.
	Queue(ctx context.Context, lang types.Lang, since time.Time, q Queue, limit, offset int) ([]Translation, int, error)

	// Counts is how many of a language's strings seen since since are in
	// each review queue.
	Counts(ctx context.Context, lang types.Lang, since time.Time) (Counts, error)

	// SendBack makes a translation with text a draft again with a note,
	// ErrNotFound if it has no text.
	SendBack(ctx context.Context, src Source, lang types.Lang, note string, at time.Time) error

	// TranslatorLangs is the languages somebody was made a translator of.
	TranslatorLangs(ctx context.Context, userID types.ID) ([]types.Lang, error)

	// AddTranslator records a grant, keeping one already there.
	AddTranslator(ctx context.Context, g Grant) error

	// RemoveTranslator takes a language from somebody.
	RemoveTranslator(ctx context.Context, userID types.ID, lang types.Lang) error

	// Translators is every grant.
	Translators(ctx context.Context) ([]Grant, error)
}

// Business is the translations, and the catalogue every page reads from.
type Business struct {
	store Storer
	now   func() time.Time

	// registered is when this binary registered its strings: a string not
	// seen since is one it no longer says, and nobody is asked to
	// translate it (Pending).
	registered atomic.Int64

	// The catalogue is read on every string of every page, and replaced
	// whole when translations change -- so an atomic pointer to a map that
	// is never written after it is built, rather than a map behind a lock.
	current atomic.Pointer[catalogue]
}

// catalogue is a translation for every string that has one, by language.
type catalogue map[types.Lang]map[Source]string

// NewBusiness constructs one. A nil now means time.Now. Until Reload has run
// every string is answered in English.
func NewBusiness(store Storer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	b := &Business{store: store, now: now}
	b.current.Store(&catalogue{})

	return b
}

// Register records the strings the running binary uses. Duplicates are
// folded, their files together, and a source with no English is refused: it
// is a template that translates nothing, and a bug.
func (b *Business) Register(ctx context.Context, uses []Use) error {
	at := map[Source]int{}
	unique := make([]Use, 0, len(uses))

	for _, u := range uses {
		if strings.TrimSpace(u.EN) == "" {
			return errors.New("a string to translate has no English text")
		}

		i, seen := at[u.Source]
		if !seen {
			at[u.Source] = len(unique)
			unique = append(unique, Use{Source: u.Source})
			i = len(unique) - 1
		}

		unique[i].Pages = append(unique[i].Pages, u.Pages...)
	}

	for i := range unique {
		slices.Sort(unique[i].Pages)
		unique[i].Pages = slices.Compact(unique[i].Pages)
	}

	slices.SortFunc(unique, func(a, b Use) int {
		return strings.Compare(a.Context+"\x00"+a.EN, b.Context+"\x00"+b.EN)
	})

	now := b.now()

	if err := b.store.Register(ctx, unique, types.Translated, now); err != nil {
		return fmt.Errorf("registering the strings to translate: %w", err)
	}

	b.registered.Store(now.UnixMilli())

	return nil
}

// Reload reads every translation into the catalogue the pages use.
func (b *Business) Reload(ctx context.Context) error {
	all, err := b.store.Translated(ctx)
	if err != nil {
		return fmt.Errorf("reading the translations: %w", err)
	}

	next := catalogue{}

	for _, t := range all {
		if t.Text == "" || t.Status == Pending {
			continue
		}

		if next[t.Lang] == nil {
			next[t.Lang] = map[Source]string{}
		}

		next[t.Lang][t.Source] = t.Text
	}

	b.current.Store(&next)

	return nil
}

// Translate is a string in a language: its translation if it has one, and
// its English otherwise.
func (b *Business) Translate(lang types.Lang, context, en string) string {
	if lang == types.English {
		return en
	}

	if text, ok := (*b.current.Load())[lang][Source{Context: context, EN: en}]; ok {
		return text
	}

	return en
}
