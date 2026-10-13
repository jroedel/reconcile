package reportbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// ErrForbidden is saving, changing or noting a report without keeping the
// books it is of.
var ErrForbidden = tenancybus.ErrForbidden

// The lines saved reports write into their scope's history.
const (
	ReportSaved   eventbus.Action = "report.saved"
	ReportChanged eventbus.Action = "report.changed"
	ReportRemoved eventbus.Action = "report.removed"
	ReportNoted   eventbus.Action = "report.noted"
)

// The most a person's words on a report may be: a title is a heading, a
// description a few paragraphs, a note what a treasurer says at a meeting.
const (
	MaxTitle       = 100
	MaxDescription = 2000
	MaxNote        = 4000
)

// Invalid is what a person typed that cannot be saved, by field: "title",
// "description", "note", "default" (the period a run shows when its
// address names none), "period" (a note's) or "style".
type Invalid struct {
	Field string
	Err   error
}

func (e Invalid) Error() string { return e.Field + ": " + e.Err.Error() }
func (e Invalid) Unwrap() error { return e.Err }

// Saved is a saved report: a style with its choices made, kept by a person
// under a title of their own so that it comes out the same way every month
// (docs/reports.md, "Saved reports"). It keeps no figures and no period:
// the period is chosen each time it is run, from Period's preset when the
// address names none.
type Saved struct {
	ID    types.ID
	Scope types.Scope
	Style string

	Title       string // the person's words, never translated
	Description string

	// Choices is the style's choices as stored: JSON that only the style
	// reads, so that a choice added to a style later needs no migration.
	Choices string

	// Period is the key of the preset a run shows when the address names
	// no period; "" is the style's own default.
	Period string

	CreatedBy, UpdatedBy types.ID
	CreatedAt, UpdatedAt time.Time
	RemovedAt            time.Time
}

// Removed reports whether it was removed.
func (s Saved) Removed() bool { return !s.RemovedAt.IsZero() }

// Note is what a person wrote under a report for one period, with the
// figures' fingerprint as they stood when it was written.
type Note struct {
	ReportID types.ID
	Period   Period
	Text     string
	Figures  string
	By       types.ID
	At       time.Time
}

// Storer keeps saved reports and their notes.
type Storer interface {
	Create(ctx context.Context, s Saved, ev eventbus.Event) error
	Update(ctx context.Context, s Saved, ev eventbus.Event) error
	ByID(ctx context.Context, id types.ID) (Saved, error)
	Of(ctx context.Context, scope types.Scope) ([]Saved, error)

	Note(ctx context.Context, reportID types.ID, p Period) (Note, bool, error)
	SetNote(ctx context.Context, n Note, ev eventbus.Event) error
	RemoveNote(ctx context.Context, reportID types.ID, p Period, ev eventbus.Event) error
}

// --- choices as stored --------------------------------------------------------------

// storedChoices is every style's choices as JSON, each style reading its
// own fields. Unknown fields are ignored and missing ones are the
// defaults, so a report saved before a choice existed reads as that
// choice's default, and one saved with a choice since removed reads
// without it.
type storedChoices struct {
	Accounts    []string `json:"accounts,omitempty"`
	PassThrough string   `json:"passthrough,omitempty"`
	Detail      string   `json:"detail,omitempty"`

	Gap          int  `json:"gap,omitempty"`
	HideAccounts bool `json:"hide_accounts,omitempty"`
}

func encodeChoices(c IncomeExpensesChoices) string {
	c = c.normal()
	sc := storedChoices{PassThrough: string(c.PassThrough), Detail: string(c.Detail)}
	for _, id := range c.Accounts {
		sc.Accounts = append(sc.Accounts, id.String())
	}

	b, _ := json.Marshal(sc)

	return string(b)
}

func encodeProject(c ProjectChoices) string {
	c = c.normal()

	b, _ := json.Marshal(storedChoices{Gap: c.Gap, HideAccounts: c.HideAccounts})

	return string(b)
}

// ProjectChoices is the report's choices as a project's style reads them,
// the defaults for JSON that cannot be read.
func (s Saved) ProjectChoices() ProjectChoices {
	var sc storedChoices

	_ = json.Unmarshal([]byte(s.Choices), &sc)

	return ProjectChoices{Gap: sc.Gap, HideAccounts: sc.HideAccounts}.normal()
}

// IncomeExpensesChoices is the report's choices, as its style reads them.
// Stored JSON that cannot be read is the defaults: a report that shows
// everything is better than one that will not open.
func (s Saved) IncomeExpensesChoices() IncomeExpensesChoices {
	var sc storedChoices

	_ = json.Unmarshal([]byte(s.Choices), &sc)

	c := IncomeExpensesChoices{PassThrough: PassThroughShown(sc.PassThrough), Detail: Detail(sc.Detail)}

	for _, a := range sc.Accounts {
		if id, err := types.ParseID(a); err == nil {
			c.Accounts = append(c.Accounts, id)
		}
	}

	return c.normal()
}

// --- saving, changing, removing ------------------------------------------------------

// Fields is what a person says of a saved report: its words, its default
// period, and the choices of its style -- Choices for income and expenses
// by month, Project for a project's.
type Fields struct {
	Title, Description string
	Period             string // a preset's key, or ""
	Choices            IncomeExpensesChoices
	Project            ProjectChoices
}

// encoded is the choices of a style, as stored. A gap that is not one is
// refused rather than defaulted: a person typed it, and should see that it
// did not take.
func (f Fields) encoded(style string) (string, error) {
	if style != ProjectStyle {
		return encodeChoices(f.Choices), nil
	}

	if f.Project.Gap != 0 && (f.Project.Gap < 1 || f.Project.Gap > MaxGap) {
		return "", Invalid{Field: "gap", Err: fmt.Errorf("a quiet gap is a number of days from 1 to %d", MaxGap)}
	}

	return encodeProject(f.Project), nil
}

func (f Fields) clean() (Fields, error) {
	f.Title = strings.TrimSpace(f.Title)
	f.Description = strings.TrimSpace(strings.ReplaceAll(f.Description, "\r\n", "\n"))

	switch {
	case f.Title == "" || utf8.RuneCountInString(f.Title) > MaxTitle:
		return f, Invalid{Field: "title", Err: fmt.Errorf("give it a title of at most %d characters", MaxTitle)}
	case utf8.RuneCountInString(f.Description) > MaxDescription:
		return f, Invalid{Field: "description", Err: fmt.Errorf("the description can be at most %d characters", MaxDescription)}
	case f.Period != "" && !slices.Contains([]string{LastMonth, YearToDate, LastTwelve, EveryMonth}, f.Period):
		return f, Invalid{Field: "default", Err: errors.New("choose one of the periods offered")}
	}

	return f, nil
}

// keeping is the books, when the actor keeps them: Bookkeep on the scope.
func (b *Business) keeping(ctx context.Context, actor types.ID, scope types.Scope) (Books, error) {
	books, err := b.Books(ctx, actor, scope)
	if err != nil {
		return Books{}, err
	}

	if !books.Access.Can(tenancybus.Bookkeep) {
		return Books{}, ErrForbidden
	}

	return books, nil
}

// Save saves a style's run as a report of the scope, under a title: a
// bookkeeper's.
func (b *Business) Save(ctx context.Context, now time.Time, actor types.ID, scope types.Scope, style string, f Fields) (Saved, error) {
	if _, err := b.keeping(ctx, actor, scope); err != nil {
		return Saved{}, err
	}

	if s, ok := StyleOf(style); !ok || !slices.Contains(s.Scopes, scope.Kind) {
		return Saved{}, Invalid{Field: "style", Err: errors.New("that layout is not one these books can be reported in")}
	}

	f, err := f.clean()
	if err != nil {
		return Saved{}, err
	}

	choices, err := f.encoded(style)
	if err != nil {
		return Saved{}, err
	}

	s := Saved{
		ID: types.NewID(), Scope: scope, Style: style,
		Title: f.Title, Description: f.Description, Period: f.Period, Choices: choices,
		CreatedBy: actor, CreatedAt: now, UpdatedBy: actor, UpdatedAt: now,
	}

	return s, b.store.Create(ctx, s, eventbus.New(now, actor, scope, ReportSaved, map[string]string{"title": s.Title}))
}

// Saved is a saved report and its books, for anyone who may read them.
func (b *Business) Saved(ctx context.Context, actor, id types.ID) (Saved, Books, error) {
	s, err := b.store.ByID(ctx, id)
	if err != nil {
		return Saved{}, Books{}, err
	}

	if s.Removed() {
		return Saved{}, Books{}, ErrNotFound
	}

	books, err := b.Books(ctx, actor, s.Scope)
	if err != nil {
		return Saved{}, Books{}, err
	}

	return s, books, nil
}

// SavedOf is the books' saved reports, by title, for anyone who may read
// them.
func (b *Business) SavedOf(ctx context.Context, actor types.ID, scope types.Scope) ([]Saved, error) {
	if _, err := b.Books(ctx, actor, scope); err != nil {
		return nil, err
	}

	return b.store.Of(ctx, scope)
}

// Change changes what a saved report says and shows: a bookkeeper's. The
// history says what its title was, when that changed.
func (b *Business) Change(ctx context.Context, now time.Time, actor, id types.ID, f Fields) (Saved, error) {
	s, err := b.ownSaved(ctx, actor, id)
	if err != nil {
		return Saved{}, err
	}

	if f, err = f.clean(); err != nil {
		return Saved{}, err
	}

	choices, err := f.encoded(s.Style)
	if err != nil {
		return Saved{}, err
	}

	detail := map[string]string{"title": f.Title}
	if f.Title != s.Title {
		detail["from"] = s.Title
	}

	s.Title, s.Description, s.Period, s.Choices = f.Title, f.Description, f.Period, choices
	s.UpdatedBy, s.UpdatedAt = actor, now

	return s, b.store.Update(ctx, s, eventbus.New(now, actor, s.Scope, ReportChanged, detail))
}

// Remove takes a saved report off its books' list: a bookkeeper's. Its
// notes stay, with the report, for the history.
func (b *Business) Remove(ctx context.Context, now time.Time, actor, id types.ID) error {
	s, err := b.ownSaved(ctx, actor, id)
	if err != nil {
		return err
	}

	s.RemovedAt, s.UpdatedBy, s.UpdatedAt = now, actor, now

	return b.store.Update(ctx, s, eventbus.New(now, actor, s.Scope, ReportRemoved, map[string]string{"title": s.Title}))
}

// ownSaved is a saved report the actor keeps the books of.
func (b *Business) ownSaved(ctx context.Context, actor, id types.ID) (Saved, error) {
	s, _, err := b.Saved(ctx, actor, id)
	if err != nil {
		return Saved{}, err
	}

	if _, err := b.keeping(ctx, actor, s.Scope); err != nil {
		return Saved{}, err
	}

	return s, nil
}

// --- running one ----------------------------------------------------------------------

// SavedRun is a saved report run for a period.
type SavedRun struct {
	Saved Saved

	// Known reports whether its style is still one this code has. A report
	// whose style is gone shows nothing but that, and the way to remove it.
	Known bool

	// Frame is the run's books and period, whichever its style; Report
	// is the run of income and expenses by month, and Project of a
	// project's, the other one zero.
	Frame   Frame
	Report  IncomeExpenses
	Project ProjectReport

	// Note is the period's note, if there is one; Changed reports whether
	// the figures are not what they were when it was written.
	Note    Note
	HasNote bool
	Changed bool

	// LeftOut is accounts the report was saved with that are no longer
	// among the books' -- put for reference since -- and Missing how many
	// it was saved with that no longer exist at all. Each is named on the
	// page rather than dropped in silence.
	LeftOut []tenancybus.Account
	Missing int
}

// Run runs a saved report for a period, for anyone who may read its
// books. A zero period is the report's own default.
func (b *Business) Run(ctx context.Context, now time.Time, actor, id types.ID, p Period) (SavedRun, error) {
	s, books, err := b.Saved(ctx, actor, id)
	if err != nil {
		return SavedRun{}, err
	}

	run := SavedRun{Saved: s}

	if _, run.Known = StyleOf(s.Style); !run.Known {
		run.Frame.Books = books

		return run, nil
	}

	if p.Zero() && s.Period != "" {
		for _, pr := range Presets(types.DateOf(now), books.FiscalStart) {
			if pr.Key == s.Period {
				p = pr.Period
			}
		}
	}

	var figures string

	switch s.Style {
	case ProjectStyle:
		if run.Project, err = b.ProjectReport(ctx, now, actor, s.Scope, p, s.ProjectChoices()); err != nil {
			return SavedRun{}, err
		}

		run.Frame, figures = run.Project.Frame, run.Project.figures()
	default:
		c := s.IncomeExpensesChoices()

		if run.Report, err = b.IncomeExpenses(ctx, now, actor, s.Scope, p, c); err != nil {
			return SavedRun{}, err
		}

		if run.LeftOut, run.Missing, err = b.unknown(ctx, actor, books, c.Accounts); err != nil {
			return SavedRun{}, err
		}

		run.Frame, figures = run.Report.Frame, fingerprint(run.Report.Answer)
	}

	if run.Note, run.HasNote, err = b.store.Note(ctx, id, run.Frame.Period); err != nil {
		return SavedRun{}, err
	}

	run.Changed = run.HasNote && run.Note.Figures != figures

	return run, nil
}

// figures is the fingerprint of a saved report's figures for a period, as
// its style reckons them.
func (b *Business) figures(ctx context.Context, now time.Time, actor types.ID, s Saved, p Period) (string, error) {
	if s.Style == ProjectStyle {
		r, err := b.ProjectReport(ctx, now, actor, s.Scope, p, s.ProjectChoices())

		return r.figures(), err
	}

	r, err := b.IncomeExpenses(ctx, now, actor, s.Scope, p, s.IncomeExpensesChoices())

	return fingerprint(r.Answer), err
}

// unknown is the accounts among ids that are not the books': those put
// for reference since, and how many no longer exist.
func (b *Business) unknown(ctx context.Context, actor types.ID, books Books, ids []types.ID) ([]tenancybus.Account, int, error) {
	var gone []types.ID

	for _, id := range ids {
		if !slices.ContainsFunc(books.Accounts, func(a tenancybus.Account) bool { return a.ID == id }) {
			gone = append(gone, id)
		}
	}

	if len(gone) == 0 || books.Scope.Kind != types.ScopeOrg {
		return nil, len(gone), nil
	}

	all, _, err := b.access.Contents(ctx, actor, books.Scope.ID)
	if err != nil {
		return nil, 0, err
	}

	var left []tenancybus.Account

	for _, a := range all {
		if slices.Contains(gone, a.ID) {
			left = append(left, a)
		}
	}

	return left, len(gone) - len(left), nil
}

// fingerprint is the figures an answer adds up to, in a few characters:
// enough to tell, later, that they are not what they were. Names are not
// in it, so renaming a category changes nothing; every sum is, so sorting
// a charge, importing a statement, a category's kind or an account's mark
// all do.
//
// Planned as a reading of the history (docs/reports.md), and changed: the
// history records a part put in a project, not a part sorted into a
// category, and recording every sort would drown each account's history
// in lines nobody reads. The figures themselves are the thing a note was
// written beside, and they say whether they moved.
//
// A style may add lines of its own (more): a project's balance brought
// forward, which is money the cells do not hold.
func fingerprint(a Answer, more ...string) string {
	h := sha256.New()

	for _, c := range a.Cells {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%d\n", c.Currency, c.Kind, c.CategoryID, c.ProjectID, c.AccountID, c.Month, c.Sum)
	}

	for _, m := range more {
		fmt.Fprintln(h, m)
	}

	return hex.EncodeToString(h.Sum(nil)[:12])
}

// SetNote writes a saved report's note for a period, or removes it when
// the text is empty: a bookkeeper's. The figures as they stand are kept
// with it, so that a later run can say when they have changed.
func (b *Business) SetNote(ctx context.Context, now time.Time, actor, id types.ID, p Period, text string) error {
	s, err := b.ownSaved(ctx, actor, id)
	if err != nil {
		return err
	}

	if p.Zero() {
		return Invalid{Field: "period", Err: errors.New("a note is for the months it was written beside")}
	}

	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if utf8.RuneCountInString(text) > MaxNote {
		return Invalid{Field: "note", Err: fmt.Errorf("a note can be at most %d characters", MaxNote)}
	}

	before, had, err := b.store.Note(ctx, id, p)
	if err != nil {
		return err
	}

	detail := map[string]string{"title": s.Title, "from": p.From, "to": p.To, "note": text}
	if p.All {
		detail["from"], detail["to"] = "", ""
	}

	if had {
		detail["before"] = before.Text
	}

	ev := eventbus.New(now, actor, s.Scope, ReportNoted, detail)

	if text == "" {
		if !had {
			return nil
		}

		return b.store.RemoveNote(ctx, id, p, ev)
	}

	if had && before.Text == text {
		return nil
	}

	figures, err := b.figures(ctx, now, actor, s, p)
	if err != nil {
		return err
	}

	return b.store.SetNote(ctx, Note{ReportID: id, Period: p, Text: text, Figures: figures, By: actor, At: now}, ev)
}
