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
// The translating itself is done mostly by Claude, through an API that
// arrives later (docs/plan.md, build order step 9), and checked by a person.
// That is why the status has three values rather than two: pending is nothing
// yet, draft is a translation nobody has checked, approved is one somebody
// has.
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

// Status is how far a translation has got.
type Status string

const (
	Pending  Status = "pending"
	Draft    Status = "draft"
	Approved Status = "approved"
)

// Translation is one string in one language.
type Translation struct {
	Source
	Lang      types.Lang
	Text      string
	Status    Status
	UpdatedAt time.Time
}

// Storer is what this package needs from storage.
type Storer interface {
	// Register records every source and opens a pending translation for
	// each of them in each of langs, keeping whatever is already there.
	Register(ctx context.Context, sources []Source, langs []types.Lang, now time.Time) error

	// Translated is every translation that has text: drafts and approved.
	Translated(ctx context.Context) ([]Translation, error)
}

// Business is the translations, and the catalogue every page reads from.
type Business struct {
	store Storer
	now   func() time.Time

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
// folded, and a source with no English is refused: it is a template that
// translates nothing, and a bug.
func (b *Business) Register(ctx context.Context, sources []Source) error {
	seen := map[Source]bool{}
	unique := make([]Source, 0, len(sources))

	for _, s := range sources {
		if strings.TrimSpace(s.EN) == "" {
			return errors.New("a string to translate has no English text")
		}

		if !seen[s] {
			seen[s] = true
			unique = append(unique, s)
		}
	}

	slices.SortFunc(unique, func(a, b Source) int {
		return strings.Compare(a.Context+"\x00"+a.EN, b.Context+"\x00"+b.EN)
	})

	if err := b.store.Register(ctx, unique, types.Translated, b.now()); err != nil {
		return fmt.Errorf("registering the strings to translate: %w", err)
	}

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
