package ledgerbus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Finding transactions across accounts (docs/books-api.md, "Reading"), and
// trying a sorting rule before it is saved. Both are for hunting down an
// amount -- "the card payments out of this account this year", "anything
// for 1,240.00 in March" -- and both answer from the app's own reading of
// the books rather than from whoever asks remembering them.
//
// Neither adds a query of its own. Find is the web's search over every
// account the actor may read (explore.go), and TryRule asks the rules' own
// Meets and Pick, so that a rule tried here sorts exactly what the same
// rule saved would.

// Query is what to look for: the web's search's choices (Criteria), over
// every account the actor may read, those named, or one organization's.
// Every part is optional; the zero Query finds the newest transactions of
// every account the actor may read but those for reference.
type Query struct {
	// Criteria is what a person chooses on the web, read the same way: a
	// category, a kind and a project are judged on one part together, and
	// a row's amount is then those parts'. Criteria.Account is one more of
	// Accounts; Limit is DefaultFind when zero, at most MaxFind.
	Criteria

	// Accounts narrows the search to these. One the actor may not read is
	// left out, as if it were not there.
	Accounts []types.ID

	// Org narrows it to an organization's accounts, and with Accounts to
	// those of them that are its.
	Org types.ID
}

const (
	// DefaultFind and MaxFind are how many Find returns.
	DefaultFind = 50
	MaxFind     = 200
)

// Found is a transaction Find found, with its account, and the amount it
// fits with: the parts that were chosen, which Part says when they are not
// all of it, and are Parts.
type Found struct {
	Transaction Transaction
	Account     tenancybus.Account
	Amount      money.Amount
	Part        bool
	Parts       []Split

	// Why and Given are the row's, when one of the accountant's lists was
	// chosen (accountant.go).
	Why   []Reason
	Given []Given
}

// Finding is Find's answer: a page of the transactions, newest first, and
// over all of them how many fitted and their money in and out.
type Finding struct {
	Found []Found

	// More is how many fitted after this page, Next the Skip that reads
	// them, and Total how many fitted in all. Sums is their money, a
	// pending charge since posted left out as the web's search leaves it.
	More, Next, Total int
	Sums              []Sum

	// LeftOut is the accounts for reference that were not searched,
	// because none was named: their money is counted in another account,
	// and a total with both would count it twice.
	LeftOut []tenancybus.Account
}

// Find is the transactions that fit q in the accounts the actor may read:
// a search (explore.go) over all of them, newest first.
func (b *Business) Find(ctx context.Context, actor types.ID, q Query) (Finding, error) {
	asked := slices.Clone(q.Accounts)
	if !q.Account.Zero() {
		asked = append(asked, q.Account)
	}

	accounts, err := b.findable(ctx, actor, q.Org, asked)
	if err != nil {
		return Finding{}, err
	}

	var (
		f      Finding
		within Within
	)

	// Named, an account for reference is searched like any other: it is
	// how a charge in one is found.
	if len(asked) == 0 {
		accounts = slices.DeleteFunc(accounts, func(a tenancybus.Account) bool {
			if a.Reference {
				f.LeftOut = append(f.LeftOut, a)
			}

			return a.Reference
		})
	}

	for _, a := range accounts {
		within.Accounts = append(within.Accounts, a.ID)
	}

	c := q.Criteria
	c.Account = types.ID{}

	if len(within.Accounts) == 0 {
		return f, nil
	}

	rows, err := b.look(ctx, within, accounts, c)
	if err != nil {
		return Finding{}, err
	}

	newestFirst(rows)

	f.Total, f.Sums = len(rows), sums(rows)

	limit := c.Limit
	if limit <= 0 {
		limit = DefaultFind
	}

	skip := min(max(c.Skip, 0), len(rows))
	end := min(skip+min(limit, MaxFind), len(rows))

	for _, r := range rows[skip:end] {
		f.Found = append(f.Found, Found{Transaction: r.Transaction, Account: r.Account, Amount: r.Amount, Part: r.Part, Parts: r.Parts, Why: r.Why, Given: r.Given})
	}

	if end < len(rows) {
		f.More, f.Next = len(rows)-end, end
	}

	return f, nil
}

// findable is the accounts the actor may read among those asked for, or
// all of them, within the organization when one is given: one the actor
// holds no role on is ErrNotFound, as everywhere.
func (b *Business) findable(ctx context.Context, actor, org types.ID, asked []types.ID) ([]tenancybus.Account, error) {
	if org.Zero() {
		return b.readable(ctx, actor, asked)
	}

	in, _, err := b.accounts.Contents(ctx, actor, org)
	if err != nil {
		return nil, err
	}

	if len(asked) == 0 {
		return in, nil
	}

	return slices.DeleteFunc(in, func(a tenancybus.Account) bool { return !slices.Contains(asked, a.ID) }), nil
}

// readable is the accounts the actor may read, of those asked for, or all
// of them, by name.
func (b *Business) readable(ctx context.Context, actor types.ID, asked []types.ID) ([]tenancybus.Account, error) {
	if len(asked) == 0 {
		ov, err := b.accounts.Overview(ctx, actor)
		if err != nil {
			return nil, err
		}

		asked = nil
		for _, a := range ov.Accounts {
			asked = append(asked, a.ID)
		}

		for _, o := range ov.Orgs {
			for _, a := range o.Accounts {
				asked = append(asked, a.ID)
			}
		}
	}

	var out []tenancybus.Account

	seen := map[types.ID]bool{}

	for _, id := range asked {
		if seen[id] {
			continue
		}

		seen[id] = true

		a, _, err := b.accounts.Account(ctx, actor, id)

		switch {
		case errors.Is(err, tenancybus.ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}

		out = append(out, a)
	}

	slices.SortFunc(out, func(x, y tenancybus.Account) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})

	return out, nil
}

// --- trying a rule ----------------------------------------------------------------

// MaxTrial is how many transactions of each kind a trial names; the counts
// are whole.
const MaxTrial = 25

// Trial is what a sorting rule would do if it were saved now, and nothing
// is saved: the account's transactions with the rule's text, by what would
// become of each.
type Trial struct {
	Account tenancybus.Account
	Rule    rulebus.Rule

	// Would is those it would sort now: not sorted yet, outside a
	// reconciled period, and no longer rule wins.
	Would      []Transaction
	WouldCount int

	// Elsewhere is those a rule with longer text would sort instead.
	Elsewhere      []Transaction
	ElsewhereCount int

	// Torn is those where a rule with text as long disagrees, so that
	// neither would sort them; With is the rules it would disagree with.
	Torn      []Transaction
	TornCount int
	With      []rulebus.Rule

	// Already is those with the text that are sorted already, by a
	// person or a rule, or locked in a reconciled period: a rule never
	// changes them.
	Already      []Transaction
	AlreadyCount int
}

// TryRule is what a rule with these fields would do on the account, for a
// bookkeeper of it, as rulebus would check the fields: the text's length
// and the direction. The category and project are optional here -- the
// trial is often asked before they are chosen -- and are what tells
// agreement from disagreement with a rule of the same length.
func (b *Business) TryRule(ctx context.Context, now time.Time, actor, accountID types.ID, f rulebus.Fields) (Trial, error) {
	account, err := b.bookkept(ctx, actor, accountID)
	if err != nil {
		return Trial{}, err
	}

	f.Match = strings.Join(strings.Fields(f.Match), " ")
	if f.Direction == "" {
		f.Direction = rulebus.Either
	}

	switch n := utf8.RuneCountInString(f.Match); {
	case n < rulebus.MinMatch || n > rulebus.MaxMatch:
		return Trial{}, rulebus.Invalid{Field: "match", Err: errors.New("give the text to look for, 3 to 100 characters")}
	case !slices.Contains(rulebus.Directions, f.Direction):
		return Trial{}, rulebus.Invalid{Field: "direction", Err: errors.New("choose money out, money in or either")}
	}

	tried := rulebus.Rule{AccountID: accountID, Match: f.Match, Direction: f.Direction, CategoryID: f.CategoryID, ProjectID: f.ProjectID}
	trial := Trial{Account: account, Rule: tried}

	rules, err := b.rules.ForAccount(ctx, accountID)
	if err != nil {
		return Trial{}, err
	}

	// Without the rule itself, when it is a rule already being changed:
	// the same text is unique on an account, so trying it again is trying
	// a correction.
	rules = slices.DeleteFunc(rules, func(r rulebus.Rule) bool { return rulebus.Key(r.Match) == rulebus.Key(f.Match) })
	with := append(slices.Clone(rules), tried)

	blank, err := b.store.Blank(ctx, accountID)
	if err != nil {
		return Trial{}, err
	}

	open := make(map[types.ID]bool, len(blank))
	torn := map[types.ID]rulebus.Rule{}

	add := func(list *[]Transaction, count *int, t Transaction) {
		*count++
		if len(*list) < MaxTrial {
			*list = append(*list, t)
		}
	}

	for _, t := range blank {
		open[t.ID] = true

		if !tried.Meets(t.Words(), t.Amount) {
			continue
		}

		picked, ok, disagree := rulebus.Pick(with, t.Words(), t.Amount)

		// A rule as long that agrees picks the same as the rule tried
		// would: it is the tried rule's to sort, either way.
		agrees := ok && utf8.RuneCountInString(rulebus.Key(picked.Match)) == utf8.RuneCountInString(rulebus.Key(tried.Match)) &&
			picked.CategoryID == tried.CategoryID && picked.ProjectID == tried.ProjectID

		switch {
		case ok && (picked.ID.Zero() || agrees):
			add(&trial.Would, &trial.WouldCount, t)
		case ok:
			add(&trial.Elsewhere, &trial.ElsewhereCount, t)
		default:
			add(&trial.Torn, &trial.TornCount, t)

			for _, r := range disagree {
				if !r.ID.Zero() {
					torn[r.ID] = r
				}
			}
		}
	}

	for _, r := range torn {
		trial.With = append(trial.With, r)
	}

	slices.SortFunc(trial.With, func(x, y rulebus.Rule) int { return strings.Compare(x.Match, y.Match) })

	all, err := b.store.Transactions(ctx, accountID, types.Date{}, types.DateOf(now.AddDate(1, 0, 0)))
	if err != nil {
		return Trial{}, err
	}

	for _, t := range all {
		if !open[t.ID] && tried.Meets(t.Words(), t.Amount) {
			add(&trial.Already, &trial.AlreadyCount, t)
		}
	}

	return trial, nil
}
