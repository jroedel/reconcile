package translationbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/types"
)

// A person looks the translations over (docs/translations.md).
//
// Afterwards, not first: Claude's translations are on the pages as soon as
// they are written, and the review screen is where a translator of that
// language says one looks right, changes it, or sends it back to Claude with
// a note. None of these is a gate. A sent-back translation stays on the pages
// until Claude sends a better one, because the reader is better served by it
// than by English in the meantime.
//
// Every action carries the text the reviewer was shown, and is refused when
// the translation has changed since (ProblemChanged). Claude may write a
// batch while somebody reads the page; a "Looks right" pressed over words
// that are no longer there would approve words nobody read.

// The reasons a review is refused, beside the ones Check gives.
const (
	// ProblemChanged is a translation that is not what the reviewer saw.
	ProblemChanged = "changed"

	// ProblemNote is a send-back with no note, or one too long.
	ProblemNote = "note"

	// ProblemNothing is a send-back of a string with nothing written yet.
	ProblemNothing = "nothing"
)

// MaxNote is the longest note a reviewer may send back with.
const MaxNote = 500

// Refusal is a review the rules refuse, with the problem a page words.
type Refusal struct {
	Problem string
}

func (r Refusal) Error() string { return "the review is refused: " + r.Problem }

// Queue is one of the lists a reviewer works through.
type Queue string

const (
	// ToCheck is Claude's drafts nobody has looked at: the reviewer's work.
	ToCheck Queue = "check"

	// SentBack is drafts a reviewer returned to Claude with a note.
	SentBack Queue = "sent-back"

	// Done is the approved.
	Done Queue = "approved"

	// Untouched is strings nobody has translated yet.
	Untouched Queue = "pending"
)

// Queues is every queue, in the order the screen offers them.
var Queues = []Queue{ToCheck, SentBack, Done, Untouched}

// Counts is how many of a language's strings are in each queue.
type Counts struct {
	ToCheck, SentBack, Done, Untouched int
}

// ReviewPage is the most a page of a queue shows.
const ReviewPage = 50

// Queue is a page of one review queue: a language's translations of the
// strings the running binary still says.
func (b *Business) Queue(ctx context.Context, who Translator, lang types.Lang, q Queue, offset int) ([]Translation, int, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return nil, 0, err
	}

	return b.store.Queue(ctx, lang, b.since(), q, ReviewPage, max(offset, 0))
}

// Counts is how many are in each queue of a language.
func (b *Business) Counts(ctx context.Context, who Translator, lang types.Lang) (Counts, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return Counts{}, err
	}

	return b.store.Counts(ctx, lang, b.since())
}

func (b *Business) since() time.Time { return time.UnixMilli(b.registered.Load()) }

// reviewing reads a translation and checks it is still what the reviewer was
// shown.
func (b *Business) reviewing(ctx context.Context, who Translator, lang types.Lang, src Source, shown string) (Translation, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return Translation{}, err
	}

	t, err := b.store.Get(ctx, src, lang)
	if err != nil {
		return Translation{}, err
	}

	if t.Text != shown {
		return Translation{}, Refusal{Problem: ProblemChanged}
	}

	return t, nil
}

// Keep is a reviewer's yes to the words in the box. When they are the words
// that were shown, the translation is approved as it is, and keeps who
// wrote it; when the reviewer changed them, theirs are checked by the rules
// any translation keeps and approved as a person's. Either way the note, if
// any, is answered and cleared. A string nobody has translated yet is
// translated by hand this way, with shown "".
func (b *Business) Keep(ctx context.Context, now time.Time, who Translator, lang types.Lang, src Source, shown, text string) error {
	t, err := b.reviewing(ctx, who, lang, src, shown)
	if err != nil {
		return err
	}

	text = strings.TrimSpace(text)

	if problem := Check(src.EN, text); problem != "" {
		return Refusal{Problem: problem}
	}

	if text == t.Text && t.Status == Approved {
		return nil
	}

	origin := t.Origin
	if text != t.Text || origin == "" {
		origin = ByPerson
	}

	err = b.store.Put(ctx, Translation{
		Source: src, Lang: lang, Text: text, Status: Approved, Origin: origin,
		UpdatedBy: who.ID, UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("keeping a translation: %w", err)
	}

	if text != t.Text {
		return b.Reload(ctx)
	}

	return nil
}

// SendBack returns a translation to Claude with the reviewer's note: it
// becomes a draft again, goes first in Claude's next list, and stays on the
// pages meanwhile. One that is approved may be sent back too, which is how a
// reviewer asks Claude to look again at words a person already kept.
func (b *Business) SendBack(ctx context.Context, now time.Time, who Translator, lang types.Lang, src Source, shown, note string) error {
	note = strings.TrimSpace(note)

	if note == "" || utf8.RuneCountInString(note) > MaxNote {
		return Refusal{Problem: ProblemNote}
	}

	t, err := b.reviewing(ctx, who, lang, src, shown)
	if err != nil {
		return err
	}

	if t.Text == "" {
		return Refusal{Problem: ProblemNothing}
	}

	if err := b.store.SendBack(ctx, src, lang, note, now); err != nil {
		return fmt.Errorf("sending a translation back: %w", err)
	}

	return nil
}

// Shown is a translation as a page showed it.
type Shown struct {
	Source
	Text string
}

// ApproveAll is "all of these look right": every one of them that is still
// a draft nobody sent back, with the words shown, is approved as it is. One
// that changed meanwhile, or was sent back, is left for the reviewer to see
// again. How many were approved is the answer.
func (b *Business) ApproveAll(ctx context.Context, now time.Time, who Translator, lang types.Lang, shown []Shown) (int, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return 0, err
	}

	if len(shown) > ReviewPage {
		return 0, fmt.Errorf("%w: at most %d at once", ErrInvalid, ReviewPage)
	}

	n := 0

	for _, s := range shown {
		t, err := b.store.Get(ctx, s.Source, lang)

		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return n, fmt.Errorf("reading a translation: %w", err)
		case t.Status != Draft || t.Note != "" || t.Text != s.Text || t.Text == "":
			continue
		}

		t.Status, t.UpdatedBy, t.UpdatedAt = Approved, who.ID, now

		if err := b.store.Put(ctx, t); err != nil {
			return n, fmt.Errorf("approving a translation: %w", err)
		}

		n++
	}

	return n, nil
}

// --- translators ------------------------------------------------------------------

// Grant is somebody made a translator of a language.
type Grant struct {
	UserID    types.ID
	Lang      types.Lang
	GrantedBy types.ID
	GrantedAt time.Time
}

// Translators is every grant. The site administrator's to see.
func (b *Business) Translators(ctx context.Context, who Translator) ([]Grant, error) {
	if !who.SiteAdmin {
		return nil, ErrForbidden
	}

	return b.store.Translators(ctx)
}

// AddTranslator makes somebody a translator of a language, which lets them
// look over its translations and hold a key for the API. The site
// administrator's to do; a language that is not translated is ErrInvalid.
func (b *Business) AddTranslator(ctx context.Context, now time.Time, who Translator, userID types.ID, lang types.Lang) error {
	if !who.SiteAdmin {
		return ErrForbidden
	}

	if !slices.Contains(types.Translated, lang) {
		return fmt.Errorf("%w: a language that is translated", ErrInvalid)
	}

	return b.store.AddTranslator(ctx, Grant{UserID: userID, Lang: lang, GrantedBy: who.ID, GrantedAt: now})
}

// RemoveTranslator takes a language from somebody. Their keys stay, and
// open nothing of that language from the next request on; with no language
// left, nothing at all.
func (b *Business) RemoveTranslator(ctx context.Context, who Translator, userID types.ID, lang types.Lang) error {
	if !who.SiteAdmin {
		return ErrForbidden
	}

	return b.store.RemoveTranslator(ctx, userID, lang)
}
