package translationbus

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/types"
)

// Claude fills the translations (docs/translations.md).
//
// Through the API a translation is written as a draft, by Claude, and is
// shown on every page at once: Claude's translations are trusted, and a
// person looks them over afterwards. "Draft" means only that no person has
// looked at it yet. What the API never does is overwrite a translation a
// person approved or corrected, because that is the person's word on it.
//
// Each translation in a batch stands alone. A refusal says which rule it
// broke, and the rest of the batch is kept: a batch of fifty with one
// placeholder dropped keeps forty-nine.

// ErrNotFound is a string that is not there, in a language it should be.
var ErrNotFound = errors.New("no such string")

// ErrForbidden is somebody who may not translate that language.
var ErrForbidden = errors.New("not a translator of that language")

// Translator is who is asking, as much as this domain needs to know: the
// site administrator may translate every language.
type Translator struct {
	ID        types.ID
	SiteAdmin bool
}

// MayTranslate reports whether somebody may write and look over a
// language's translations, and hold a key to do it through the API: the
// site administrator, every language, and the translators the
// administrator names, theirs (review.go). Nobody else -- anyone may sign
// up here, and "every signed-in person" would let a stranger rewrite the
// interface.
func (b *Business) MayTranslate(ctx context.Context, who Translator, lang types.Lang) (bool, error) {
	langs, err := b.Languages(ctx, who)
	if err != nil {
		return false, err
	}

	return slices.Contains(langs, lang), nil
}

// MayTranslateAny reports whether somebody may translate some language:
// whether they may hold a key at all.
func (b *Business) MayTranslateAny(ctx context.Context, who Translator) (bool, error) {
	langs, err := b.Languages(ctx, who)

	return len(langs) > 0, err
}

// Languages is the languages somebody may translate, in the order the app
// lists them.
func (b *Business) Languages(ctx context.Context, who Translator) ([]types.Lang, error) {
	if who.SiteAdmin {
		return slices.Clone(types.Translated), nil
	}

	if who.ID.Zero() {
		return nil, nil
	}

	granted, err := b.store.TranslatorLangs(ctx, who.ID)
	if err != nil {
		return nil, fmt.Errorf("reading somebody's languages: %w", err)
	}

	var out []types.Lang

	for _, l := range types.Translated {
		if slices.Contains(granted, l) {
			out = append(out, l)
		}
	}

	return out, nil
}

func (b *Business) mayTranslate(ctx context.Context, who Translator, lang types.Lang) error {
	ok, err := b.MayTranslate(ctx, who, lang)

	switch {
	case err != nil:
		return err
	case !ok:
		return ErrForbidden
	}

	return nil
}

// --- what is waiting ------------------------------------------------------------

// Waiting is the translations of a language Claude is asked for, and how
// many there are in all.
type Waiting struct {
	Lang      types.Lang
	Items     []Translation
	Remaining int

	// Glossary is how the app's own terms are translated already, so that
	// "statement" is the same word on every page.
	Glossary []Term
}

// Term is one glossary entry.
type Term struct {
	EN   string
	Text string
}

// MaxBatch is the most translations asked for or written at once.
const MaxBatch = 200

// Pending is up to limit translations of a language waiting for Claude:
// the ones a reviewer sent back first, with their notes, then those with no
// text yet. Only strings the running binary still says are asked for.
func (b *Business) Pending(ctx context.Context, who Translator, lang types.Lang, limit int) (Waiting, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return Waiting{}, err
	}

	limit = min(max(limit, 1), MaxBatch)

	items, remaining, err := b.store.Pending(ctx, lang, time.UnixMilli(b.registered.Load()), limit)
	if err != nil {
		return Waiting{}, fmt.Errorf("reading the translations waiting: %w", err)
	}

	return Waiting{Lang: lang, Items: items, Remaining: remaining, Glossary: b.glossary(lang)}, nil
}

// glossaryTerms are the app's own words: those a treasurer must read the
// same way on every page. Translated once, they are handed to Claude with
// every batch.
var glossaryTerms = []string{
	"Statement", "Statements", "Reconcile", "Reconciled", "Receipt", "Receipts",
	"Category", "Project", "Money in", "Money out", "Transactions",
	"Owner", "Bookkeeper", "Contributor", "Accountant", "Viewer",
	"not sorted yet", "Month by month", "For the accountant",
}

func (b *Business) glossary(lang types.Lang) []Term {
	var out []Term

	for _, en := range glossaryTerms {
		if text := b.Translate(lang, "", en); text != en {
			out = append(out, Term{EN: en, Text: text})
		}
	}

	return out
}

// List is a page of a language's translations with a status, or of every
// status when status is "", and how many there are in all.
func (b *Business) List(ctx context.Context, who Translator, lang types.Lang, status Status, limit, offset int) ([]Translation, int, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return nil, 0, err
	}

	if status != "" && status != Pending && status != Draft && status != Approved {
		return nil, 0, fmt.Errorf("%w: a status of pending, draft or approved", ErrInvalid)
	}

	return b.store.List(ctx, lang, status, min(max(limit, 1), 500), max(offset, 0))
}

// ErrInvalid is a request the rules refuse as a whole.
var ErrInvalid = errors.New("that needs")

// --- writing ----------------------------------------------------------------------

// Written is one translation sent.
type Written struct {
	Source
	Text string
}

// Outcome is what became of it.
type Outcome string

const (
	Created   Outcome = "created"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
	Refused   Outcome = "refused"
)

// The reasons a translation is refused, as Result.Problem.
const (
	ProblemUnknown      = "unknown"
	ProblemEmpty        = "empty"
	ProblemLength       = "length"
	ProblemPlaceholders = "placeholders"
	ProblemMarkup       = "markup"
	ProblemApproved     = "approved"
)

// Result is what became of one translation sent.
type Result struct {
	Source
	Outcome Outcome
	Problem string
}

// Write keeps a batch of Claude's translations of one language as drafts,
// shown at once, and says what became of each. The error is for the batch
// as a whole -- not allowed, too many, the store failing -- never for one
// translation.
func (b *Business) Write(ctx context.Context, now time.Time, who Translator, lang types.Lang, batch []Written) ([]Result, error) {
	if err := b.mayTranslate(ctx, who, lang); err != nil {
		return nil, err
	}

	if len(batch) > MaxBatch {
		return nil, fmt.Errorf("%w: at most %d translations at once", ErrInvalid, MaxBatch)
	}

	results := make([]Result, 0, len(batch))
	changed := false

	for _, w := range batch {
		r, err := b.write(ctx, now, who, lang, w)
		if err != nil {
			return nil, err
		}

		changed = changed || r.Outcome == Created || r.Outcome == Updated
		results = append(results, r)
	}

	if changed {
		if err := b.Reload(ctx); err != nil {
			return nil, err
		}
	}

	return results, nil
}

func (b *Business) write(ctx context.Context, now time.Time, who Translator, lang types.Lang, w Written) (Result, error) {
	text := strings.TrimSpace(w.Text)
	refuse := func(problem string) (Result, error) {
		return Result{Source: w.Source, Outcome: Refused, Problem: problem}, nil
	}

	old, err := b.store.Get(ctx, w.Source, lang)

	switch {
	case errors.Is(err, ErrNotFound):
		return refuse(ProblemUnknown)
	case err != nil:
		return Result{}, fmt.Errorf("reading a translation: %w", err)
	}

	if problem := Check(w.EN, text); problem != "" {
		return refuse(problem)
	}

	switch {
	case old.Status == Approved && old.Text == text,
		old.Status == Draft && old.Text == text && old.Note == "":
		return Result{Source: w.Source, Outcome: Unchanged}, nil
	case old.Status == Approved:
		return refuse(ProblemApproved)
	}

	outcome := Updated
	if old.Status == Pending {
		outcome = Created
	}

	t := Translation{
		Source: w.Source, Lang: lang, Text: text, Status: Draft, Origin: ByClaude,
		UpdatedBy: who.ID, UpdatedAt: now,
	}

	if err := b.store.Put(ctx, t); err != nil {
		return Result{}, fmt.Errorf("keeping a translation: %w", err)
	}

	return Result{Source: w.Source, Outcome: outcome}, nil
}

// placeholder is a {name} the app fills in as it shows a string; the same
// pattern the pages fill (app/sdk/page, translate.go).
var placeholder = regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9_]*\}`)

// Check is the rules any translation of en must keep, whoever wrote it:
// not empty, not far longer than the English, every {placeholder} the
// English has and no other, and no markup the English does not have. The
// answer is a Problem, or "".
//
// Markup because a translation is text: the page escapes it, so a "<b>"
// would be shown as itself, and a translation is no place for a tag the
// English did not need.
func Check(en, text string) string {
	switch {
	case text == "":
		return ProblemEmpty
	case utf8.RuneCountInString(text) > 4*utf8.RuneCountInString(en)+100:
		return ProblemLength
	case !slices.Equal(placeholders(en), placeholders(text)):
		return ProblemPlaceholders
	case strings.ContainsAny(text, "<>") && !strings.ContainsAny(en, "<>"):
		return ProblemMarkup
	}

	return ""
}

// placeholders is every {name} in s, sorted, once each.
func placeholders(s string) []string {
	found := placeholder.FindAllString(s, -1)
	slices.Sort(found)

	return slices.Compact(found)
}
