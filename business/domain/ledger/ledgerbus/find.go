package ledgerbus

import (
	"cmp"
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
// Neither adds a query of its own. Find reads each account's transactions
// as the month list does and keeps those that fit, and TryRule asks the
// rules' own Meets and Pick, so that a rule tried here sorts exactly what
// the same rule saved would.

// Query is what to look for. Every part is optional; the zero Query finds
// the newest transactions of every account the actor may read.
type Query struct {
	// Text is words to look for in the description, the payee or the
	// transaction's own description, as a rule looks for them: without
	// case or runs of spaces.
	Text string

	// Min and Max bound the amount without its sign, when Has says they
	// are set: an exact amount is both. Direction is "in", "out" or "".
	Min, Max       money.Amount
	HasMin, HasMax bool
	Direction      rulebus.Direction

	// From and To bound the date, both days included, when not zero.
	From, To types.Date

	// Accounts narrows the search to these; nil is every account the
	// actor may read. One they may not read is left out, as if it were
	// not there.
	Accounts []types.ID

	// Limit is the most to return, newest first: DefaultFind when zero,
	// at most MaxFind.
	Limit int
}

const (
	// DefaultFind and MaxFind are how many Find returns.
	DefaultFind = 50
	MaxFind     = 200
)

// Found is a transaction Find found, with its account.
type Found struct {
	Transaction Transaction
	Account     tenancybus.Account
}

// Finding is Find's answer: the transactions, newest first, and how many
// more fitted than the limit let through.
type Finding struct {
	Found []Found
	More  int
}

// Find is the transactions that fit q in the accounts the actor may read.
func (b *Business) Find(ctx context.Context, actor types.ID, q Query) (Finding, error) {
	accounts, err := b.readable(ctx, actor, q.Accounts)
	if err != nil {
		return Finding{}, err
	}

	from, to := q.From, q.To
	if to.Zero() {
		to = types.DateOf(time.Now().AddDate(1, 0, 0))
	}

	text := rulebus.Key(q.Text)

	var all []Found

	for _, a := range accounts {
		txs, err := b.store.Transactions(ctx, a.ID, from, to.AddDays(1))
		if err != nil {
			return Finding{}, err
		}

		for _, t := range txs {
			if fits(t, text, q) {
				all = append(all, Found{Transaction: t, Account: a})
			}
		}
	}

	slices.SortStableFunc(all, func(x, y Found) int {
		return cmp.Compare(y.Transaction.PostedOn.String(), x.Transaction.PostedOn.String())
	})

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultFind
	}

	limit = min(limit, MaxFind)

	if len(all) <= limit {
		return Finding{Found: all}, nil
	}

	return Finding{Found: all[:limit], More: len(all) - limit}, nil
}

// fits reports whether a transaction is one q asks for.
func fits(t Transaction, text string, q Query) bool {
	abs := t.Amount.Abs()

	switch {
	case text != "" && !strings.Contains(rulebus.Key(t.Words()), text) && !strings.Contains(rulebus.Key(t.OwnDescription), text):
		return false
	case q.HasMin && abs < q.Min, q.HasMax && abs > q.Max:
		return false
	case q.Direction == rulebus.In && t.Amount < 0, q.Direction == rulebus.Out && t.Amount > 0:
		return false
	}

	return true
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
