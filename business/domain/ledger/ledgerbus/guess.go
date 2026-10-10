package ledgerbus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Guessing a transaction's sorting for a person to save, and sorting a
// month on one page (docs/sorting.md, "A month at a time").
//
// A guess is a rule's choice where a rule meets the transaction and may be
// applied, and otherwise a suggestion learned from what a person sorted
// (rulebus.Learn). It only preselects: nothing is stored until a person
// saves, and then it is theirs, with no rule's mark.

// GuessBy is where a guess came from.
type GuessBy string

const (
	ByRule       GuessBy = "rule"
	BySuggestion GuessBy = "suggestion"
)

// Guess is a preselected choice for a transaction's one part, and why.
type Guess struct {
	By         GuessBy // "" for no guess
	CategoryID types.ID
	ProjectID  types.ID

	Rule       rulebus.Rule       // when By is ByRule
	Suggestion rulebus.Suggestion // when By is BySuggestion
}

// Made reports whether there is one.
func (g Guess) Made() bool { return g.By != "" }

// guesser is the rules and the learning of one account, read once for a
// page.
type guesser struct {
	sorter
	suggest rulebus.Suggester
}

func (b *Business) guesser(ctx context.Context, now time.Time, actor types.ID, account tenancybus.Account) (guesser, error) {
	s, err := b.sorter(ctx, now, actor, account)
	if err != nil {
		return guesser{}, err
	}

	// The sorter reads the list and the projects only when there are
	// rules; a suggestion and the month's choices need them either way.
	if len(s.rules) == 0 {
		if s, err = b.choices(ctx, s); err != nil {
			return guesser{}, err
		}
	}

	examples, err := b.store.Examples(ctx, account.ID)
	if err != nil {
		return guesser{}, err
	}

	return guesser{sorter: s, suggest: rulebus.Learn(examples)}, nil
}

// guess is the choice for a transaction that is one part with no category
// yet. A rule's needs the part to have nothing chosen at all, as at import;
// a suggestion is a category only, and keeps a project already chosen.
func (g guesser) guess(t Transaction) Guess {
	if len(t.Splits) != 1 || !t.Splits[0].CategoryID.Zero() {
		return Guess{}
	}

	sp := t.Splits[0]

	if sp.ProjectID.Zero() {
		if r, ok, _ := rulebus.Pick(g.rules, t.Words(), t.Amount); ok && g.problem(r, t.PostedOn) == "" {
			return Guess{By: ByRule, CategoryID: r.CategoryID, ProjectID: r.ProjectID, Rule: r}
		}
	}

	s, ok := g.suggest.Suggest(t.Words())
	if !ok {
		return Guess{}
	}

	if c, on := g.categories[s.CategoryID]; !on || c.Archived() {
		return Guess{}
	}

	return Guess{By: BySuggestion, CategoryID: s.CategoryID, ProjectID: sp.ProjectID, Suggestion: s}
}

// Guess is the preselected choice for a transaction's page, for a reader
// who may sort it now.
func (b *Business) Guess(ctx context.Context, now time.Time, actor types.ID, e Editor) (Guess, error) {
	if !e.CanSort() {
		return Guess{}, nil
	}

	g, err := b.guesser(ctx, now, actor, e.Account)
	if err != nil {
		return Guess{}, err
	}

	return g.guess(e.Transaction), nil
}

// MaxSortRows is the most transactions "Sort this month" shows at once.
// The rest are there after those are saved: a page that long is already
// an evening's work.
const MaxSortRows = 100

// SortRow is one transaction on "Sort this month".
type SortRow struct {
	Transaction Transaction
	Guess       Guess

	// Elsewhere is a project the part is in already that the reader does
	// not keep the books of, so that the choice can keep it.
	Elsewhere tenancybus.Project
}

// MonthToSort is an account's month as "Sort this month" shows it.
type MonthToSort struct {
	Account tenancybus.Account
	Month   string

	// Rows is the month's transactions in one part with no category yet,
	// outside reconciled periods, in date order, at most MaxSortRows; More
	// is how many follow those.
	Rows []SortRow
	More int

	// The choices: the account's list without the archived, and the
	// projects the reader keeps the books of that are not archived.
	Categories []categorybus.Category
	Projects   []tenancybus.Project
}

// Grouped is the category choices a kind at a time.
func (m MonthToSort) Grouped() []categorybus.Group { return categorybus.Grouped(m.Categories) }

// CategoryName names a category for a guess's sentence.
func (m MonthToSort) CategoryName(id types.ID) string {
	for _, c := range m.Categories {
		if c.ID == id {
			return c.Name
		}
	}

	return ""
}

// ToSort is a month's transactions to sort on one page, a bookkeeper's.
func (b *Business) ToSort(ctx context.Context, now time.Time, actor, accountID types.ID, month string) (MonthToSort, error) {
	account, err := b.bookkept(ctx, actor, accountID)
	if err != nil {
		return MonthToSort{}, err
	}

	from, to, err := MonthRange(month)
	if err != nil {
		return MonthToSort{}, ErrNotFound
	}

	txs, err := b.store.Transactions(ctx, accountID, from, to)
	if err != nil {
		return MonthToSort{}, err
	}

	recs, err := b.store.Reconciliations(ctx, accountID)
	if err != nil {
		return MonthToSort{}, err
	}

	g, err := b.guesser(ctx, now, actor, account)
	if err != nil {
		return MonthToSort{}, err
	}

	m := MonthToSort{Account: account, Month: month}

	for _, c := range g.categories {
		if !c.Archived() {
			m.Categories = append(m.Categories, c)
		}
	}

	slices.SortFunc(m.Categories, func(x, y categorybus.Category) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})

	for _, p := range g.projects {
		if !p.Archived() {
			m.Projects = append(m.Projects, p)
		}
	}

	slices.SortFunc(m.Projects, func(x, y tenancybus.Project) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})

	var elsewhere []types.ID

	for _, t := range txs {
		if len(t.Splits) != 1 || !t.Splits[0].CategoryID.Zero() ||
			slices.ContainsFunc(recs, func(r Reconciliation) bool { return r.Covers(t.PostedOn) }) {
			continue
		}

		if len(m.Rows) == MaxSortRows {
			m.More++

			continue
		}

		row := SortRow{Transaction: t, Guess: g.guess(t)}

		if p := t.Splits[0].ProjectID; !p.Zero() && !slices.ContainsFunc(m.Projects, func(x tenancybus.Project) bool { return x.ID == p }) {
			row.Elsewhere.ID = p
			elsewhere = append(elsewhere, p)
		}

		m.Rows = append(m.Rows, row)
	}

	if len(elsewhere) > 0 {
		names, err := b.accounts.ProjectNames(ctx, elsewhere)
		if err != nil {
			return MonthToSort{}, err
		}

		for i := range m.Rows {
			m.Rows[i].Elsewhere.Name = names[m.Rows[i].Elsewhere.ID]
		}
	}

	return m, nil
}

// Choice is what a person chose for one row of "Sort this month".
type Choice struct {
	TransactionID types.ID
	CategoryID    types.ID
	ProjectID     types.ID
}

// SortMany saves the rows of "Sort this month" that have a choice, each as
// one part for the whole transaction with its memo kept, and says how many
// it saved. A row whose choice is what is stored already is left alone, so
// that a rule's mark is not taken off a part nobody looked at again; a row
// somebody sorted or split since the page was shown is left as they did
// it. What one row cannot be is in Refused, by transaction, and the others
// are saved regardless: one archived category should not cost a person the
// other ninety-nine.
func (b *Business) SortMany(ctx context.Context, now time.Time, actor, accountID types.ID, choices []Choice) (saved int, refused map[types.ID]error, err error) {
	if _, err := b.bookkept(ctx, actor, accountID); err != nil {
		return 0, nil, err
	}

	refused = map[types.ID]error{}

	for _, c := range choices {
		if c.CategoryID.Zero() && c.ProjectID.Zero() {
			continue
		}

		t, err := b.store.TransactionByID(ctx, c.TransactionID)
		if err != nil {
			return saved, refused, err
		}

		if t.AccountID != accountID {
			return saved, refused, ErrNotFound
		}

		if len(t.Splits) != 1 || !t.Splits[0].CategoryID.Zero() {
			continue
		}

		sp := t.Splits[0]
		if sp.CategoryID == c.CategoryID && sp.ProjectID == c.ProjectID {
			continue
		}

		_, err = b.SetSplits(ctx, now, actor, t.ID, []Part{{Amount: t.Amount, CategoryID: c.CategoryID, ProjectID: c.ProjectID, Memo: sp.Memo}})

		_, invalid := errors.AsType[Invalid](err)

		switch {
		case err == nil:
			saved++
		case errors.Is(err, ErrLocked), invalid:
			refused[t.ID] = err
		default:
			return saved, refused, err
		}
	}

	return saved, refused, nil
}
