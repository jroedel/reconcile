package ledgerbus

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Explaining an amount (docs/clearing.md, 2).
//
// This is clearing: one transaction -- a withdrawal that pays a card held by
// somebody else, a collection deposited as one sum, a payout of many sales
// less their fees -- set beside the transactions that make it up, with the
// difference. Nothing about a line changes by being in an explanation: not
// its amount, its sorting nor its reconciliation. What is kept is which
// amount each line explains, and a line explains one amount at most.

// The actions this writes into the explained account's history.
const (
	ExplanationChanged eventbus.Action = "explanation.changed"
	ExplanationSettled eventbus.Action = "explanation.settled"
)

// The errors a page tells apart.
var (
	// ErrExplained is a line that explains another amount already, or one
	// that is explained itself: an explanation's lines are what it is made
	// of, and are not made of anything here.
	ErrExplained = errors.New("that transaction is part of another explanation")

	// ErrElsewhere is a line from an account outside the explained
	// account's organization.
	ErrElsewhere = errors.New("an explanation's lines come from the same organization's accounts")

	// ErrExplainNote is a note too long.
	ErrExplainNote = errors.New("the note is too long")
)

// MaxExplainNote is the longest note on an explanation.
const MaxExplainNote = 500

// Stored is an explanation as the store keeps it.
type Stored struct {
	TransactionID types.ID
	Lines         []types.ID
	Note          string
	Accepted      bool

	// Sources and Back are how its lines were last gathered: from which
	// accounts, and from how many months before the explained transaction's
	// to how many (2 and 1: the two months before it).
	Sources []types.ID
	Back    [2]int
}

// Remembered is how an account's earlier explanation was gathered, with
// its transaction's description, for gathering the next like it.
type Remembered struct {
	Description string
	Sources     []types.ID
	Back        [2]int
}

// Explanation is a transaction's explanation as one reader sees it.
type Explanation struct {
	Transaction Transaction
	Account     tenancybus.Account
	Access      tenancybus.Access

	// Lines are those the reader may see, by account and then by date.
	Lines []Line

	// Hidden is how many lines are in accounts the reader may not see, and
	// HiddenSum what they come to: shown as one sum, never their contents.
	Hidden    int
	HiddenSum money.Amount

	// Sum is what every line comes to, hidden ones too.
	Sum money.Amount

	Note     string
	Accepted bool

	// Sources and Back are how to gather lines now: as last time, or as the
	// last explanation of a transaction like this one in the account.
	Sources []types.ID
	Back    [2]int

	// Choices is the accounts lines may come from, for the gathering form.
	Choices []tenancybus.Account
}

// Line is one transaction of an explanation, with its account.
type Line struct {
	Transaction Transaction
	Account     tenancybus.Account
}

// Difference is what the lines leave unexplained, in the explained
// transaction's sign.
func (x Explanation) Difference() money.Amount { return x.Transaction.Amount - x.Sum }

// Count is how many lines it has, seen or not.
func (x Explanation) Count() int { return len(x.Lines) + x.Hidden }

// Settled reports an explanation whose lines come to the amount, or whose
// difference a person accepted.
func (x Explanation) Settled() bool {
	return x.Count() > 0 && (x.Difference() == 0 || x.Accepted)
}

// CanChange reports whether the reader may gather and settle it.
func (x Explanation) CanChange() bool { return x.Access.Can(tenancybus.Bookkeep) }

// From and To are the months Back names, as "2026-08".
func (x Explanation) From() string { return monthBack(x.Transaction.PostedOn, x.Back[0]) }

// To is the last month Back names.
func (x Explanation) To() string { return monthBack(x.Transaction.PostedOn, x.Back[1]) }

func monthBack(d types.Date, n int) string {
	t, err := time.Parse("2006-01-02", d.String())
	if err != nil {
		return ""
	}

	return time.Date(t.Year(), t.Month()-time.Month(n), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
}

// back is how many months before d each of two months is.
func back(d types.Date, from, to string) ([2]int, bool) {
	t, err1 := time.Parse("2006-01-02", d.String())
	f, err2 := time.Parse("2006-01", from)
	l, err3 := time.Parse("2006-01", to)

	if err1 != nil || err2 != nil || err3 != nil || l.Before(f) {
		return [2]int{}, false
	}

	months := func(m time.Time) int { return (t.Year()-m.Year())*12 + int(t.Month()-m.Month()) }

	return [2]int{months(f), months(l)}, true
}

// Explain is a transaction's explanation, or an empty one to start. Anyone
// who may read the transaction may read it.
func (b *Business) Explain(ctx context.Context, actor, id types.ID) (Explanation, error) {
	t, err := b.store.TransactionByID(ctx, id)
	if err != nil {
		return Explanation{}, err
	}

	account, access, err := b.accounts.Account(ctx, actor, t.AccountID)
	if err != nil {
		return Explanation{}, err
	}

	x := Explanation{Transaction: t, Account: account, Access: access, Back: [2]int{1, 1}}

	stored, found, err := b.store.Explanation(ctx, id)
	if err != nil {
		return Explanation{}, err
	}

	if found {
		x.Note, x.Accepted, x.Sources, x.Back = stored.Note, stored.Accepted, stored.Sources, stored.Back
	}

	if len(x.Sources) == 0 {
		if err := b.remembered(ctx, &x); err != nil {
			return Explanation{}, err
		}
	}

	lines, err := b.LookupAll(ctx, stored.Lines)
	if err != nil {
		return Explanation{}, err
	}

	names := map[types.ID]tenancybus.Account{}

	for _, l := range lines {
		x.Sum += l.Amount

		a, ok := names[l.AccountID]
		if !ok {
			var acc tenancybus.Access

			a, acc, err = b.accounts.Account(ctx, actor, l.AccountID)
			switch {
			case errors.Is(err, ErrNotFound):
				a = tenancybus.Account{}
			case err != nil:
				return Explanation{}, err
			case !acc.Can(tenancybus.Read):
				a = tenancybus.Account{}
			}

			names[l.AccountID] = a
		}

		if a.ID.Zero() {
			x.Hidden++
			x.HiddenSum += l.Amount

			continue
		}

		x.Lines = append(x.Lines, Line{Transaction: l, Account: a})
	}

	slices.SortFunc(x.Lines, func(a, b Line) int {
		return cmp.Or(strings.Compare(a.Account.Name, b.Account.Name), strings.Compare(a.Transaction.PostedOn.String(), b.Transaction.PostedOn.String()))
	})

	if x.CanChange() {
		if x.Choices, err = b.sources(ctx, actor, account); err != nil {
			return Explanation{}, err
		}
	}

	return x, nil
}

// remembered fills in how to gather from the account's last explanation
// of a transaction like this one: the same payee (rulebus.Payee), so that
// next month's withdrawal is gathered as this month's was.
func (b *Business) remembered(ctx context.Context, x *Explanation) error {
	earlier, err := b.store.Explained(ctx, x.Account.ID)
	if err != nil {
		return err
	}

	payee := rulebus.Payee(x.Transaction.Description)

	for _, r := range earlier {
		if rulebus.Payee(r.Description) == payee {
			x.Sources, x.Back = r.Sources, r.Back

			return nil
		}
	}

	return nil
}

// sources is the accounts lines may come from: those of the explained
// account's organization -- or, for a personal account, the personal
// accounts -- that the actor may read.
func (b *Business) sources(ctx context.Context, actor types.ID, of tenancybus.Account) ([]tenancybus.Account, error) {
	o, err := b.accounts.Overview(ctx, actor)
	if err != nil {
		return nil, err
	}

	seen := map[types.ID]bool{}

	var out []tenancybus.Account

	add := func(as []tenancybus.Account) {
		for _, a := range as {
			if a.OrgID == of.OrgID && !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}

	for _, org := range o.Orgs {
		add(org.Accounts)
	}

	add(o.Accounts)

	slices.SortFunc(out, func(a, b tenancybus.Account) int { return strings.Compare(a.Name, b.Name) })

	return out, nil
}

// Candidates is what may be added to an explanation from some accounts
// and months: their transactions not in an explanation and not explained
// themselves, by account and date.
func (b *Business) Candidates(ctx context.Context, actor, id types.ID, accounts []types.ID, from, to string) ([]Line, error) {
	x, err := b.Explain(ctx, actor, id)
	if err != nil {
		return nil, err
	}

	if !x.CanChange() {
		return nil, ErrForbidden
	}

	start, _, err := MonthRange(from)
	if err != nil {
		return nil, ErrNotFound
	}

	_, end, err := MonthRange(to)
	if err != nil || end.Before(start) {
		return nil, ErrNotFound
	}

	allowed := map[types.ID]tenancybus.Account{}
	for _, a := range x.Choices {
		allowed[a.ID] = a
	}

	var (
		out []Line
		ids []types.ID
	)

	for _, aid := range accounts {
		a, ok := allowed[aid]
		if !ok {
			return nil, ErrElsewhere
		}

		txs, err := b.store.Transactions(ctx, aid, start, end)
		if err != nil {
			return nil, err
		}

		for _, t := range txs {
			if t.ID != id {
				out = append(out, Line{Transaction: t, Account: a})
				ids = append(ids, t.ID)
			}
		}
	}

	lineOf, explained, err := b.store.Clearing(ctx, ids)
	if err != nil {
		return nil, err
	}

	out = slices.DeleteFunc(out, func(l Line) bool {
		_, taken := lineOf[l.Transaction.ID]
		_, explains := explained[l.Transaction.ID]

		return taken || explains
	})

	return out, nil
}

// Gather adds lines to an explanation and takes others out, and remembers
// the accounts and months they were gathered from. Every line added must
// be one Candidates offers.
func (b *Business) Gather(ctx context.Context, now time.Time, actor, id types.ID, add, remove []types.ID, accounts []types.ID, from, to string) error {
	x, err := b.Explain(ctx, actor, id)
	if err != nil {
		return err
	}

	if !x.CanChange() {
		return ErrForbidden
	}

	// A line is what an explanation is made of, not made of anything here:
	// one level, so that no charge is counted in two amounts by way of a
	// third.
	if lineOf, _, err := b.store.Clearing(ctx, []types.ID{id}); err != nil {
		return err
	} else if _, ok := lineOf[id]; ok {
		return ErrExplained
	}

	stored := Stored{TransactionID: id, Sources: x.Sources, Back: x.Back}

	if len(accounts) > 0 {
		bk, ok := back(x.Transaction.PostedOn, from, to)
		if !ok {
			return ErrNotFound
		}

		stored.Sources, stored.Back = accounts, bk
	}

	if len(add) > 0 {
		offered, err := b.Candidates(ctx, actor, id, accounts, from, to)
		if err != nil {
			return err
		}

		ok := map[types.ID]bool{}
		for _, l := range offered {
			ok[l.Transaction.ID] = true
		}

		for _, a := range add {
			if !ok[a] {
				return ErrExplained
			}
		}
	}

	// Only lines the reader can see can be taken out by them.
	visible := map[types.ID]bool{}
	for _, l := range x.Lines {
		visible[l.Transaction.ID] = true
	}

	remove = slices.DeleteFunc(slices.Clone(remove), func(r types.ID) bool { return !visible[r] })

	ev := eventbus.New(now, actor, x.Account.Scope(), ExplanationChanged, map[string]string{
		"description": x.Transaction.Description,
		"date":        x.Transaction.PostedOn.String(),
		"added":       strconv.Itoa(len(add)),
		"removed":     strconv.Itoa(len(remove)),
	})

	return b.store.SaveExplanation(ctx, stored, add, remove, actor, ev)
}

// AddEntry adds a transaction just entered by hand (Enter) to an
// explanation, from any account the explanation may draw on.
func (b *Business) AddEntry(ctx context.Context, now time.Time, actor, id types.ID, entry Transaction) error {
	x, err := b.Explain(ctx, actor, id)
	if err != nil {
		return err
	}

	if !x.CanChange() {
		return ErrForbidden
	}

	if !slices.ContainsFunc(x.Choices, func(a tenancybus.Account) bool { return a.ID == entry.AccountID }) {
		return ErrElsewhere
	}

	ev := eventbus.New(now, actor, x.Account.Scope(), ExplanationChanged, map[string]string{
		"description": x.Transaction.Description,
		"date":        x.Transaction.PostedOn.String(),
		"added":       "1",
		"removed":     "0",
	})

	return b.store.SaveExplanation(ctx, Stored{TransactionID: id, Sources: x.Sources, Back: x.Back}, []types.ID{entry.ID}, nil, actor, ev)
}

// Settle writes an explanation's note, and whether its difference is
// accepted. The history keeps both, and the difference as it stood.
func (b *Business) Settle(ctx context.Context, now time.Time, actor, id types.ID, note string, accept bool) error {
	x, err := b.Explain(ctx, actor, id)
	if err != nil {
		return err
	}

	if !x.CanChange() {
		return ErrForbidden
	}

	if note = strings.TrimSpace(note); utf8.RuneCountInString(note) > MaxExplainNote {
		return ErrExplainNote
	}

	accepted := "0"
	if accept {
		accepted = "1"
	}

	ev := eventbus.New(now, actor, x.Account.Scope(), ExplanationSettled, map[string]string{
		"description": x.Transaction.Description,
		"date":        x.Transaction.PostedOn.String(),
		"difference":  x.Difference().String(),
		"currency":    x.Account.Currency,
		"accepted":    accepted,
		"note":        note,
	})

	return b.store.Settle(ctx, id, note, accept, actor, ev)
}

// Clearing is what a page or the export shows of some transactions'
// explanations, beside each: what each line is cleared by, and whether
// each explained one is settled.
type Clearing struct {
	By       map[types.ID]Clearer
	Explains map[types.ID]bool
}

// State is "explained" for a transaction whose explanation is settled,
// "open" for one whose is not, and "" for one with none: for a page.
func (c Clearing) State(id types.ID) string {
	settled, ok := c.Explains[id]

	switch {
	case !ok:
		return ""
	case settled:
		return "explained"
	}

	return "open"
}

// Clearer is the transaction a line explains, as a reader of the line sees
// it: its date and amount, or, in an account they may not read, only that
// there is one.
type Clearer struct {
	Transaction Transaction
	Hidden      bool
}

// Clearing is ClearedBy and the explained transactions' states, for some
// transactions of an account the actor has been asked about already.
func (b *Business) Clearing(ctx context.Context, actor types.ID, ids []types.ID) (Clearing, error) {
	lineOf, explained, err := b.store.Clearing(ctx, ids)
	if err != nil {
		return Clearing{}, err
	}

	c := Clearing{By: make(map[types.ID]Clearer, len(lineOf)), Explains: explained}

	if len(lineOf) == 0 {
		return c, nil
	}

	var explaining []types.ID
	for _, e := range lineOf {
		explaining = append(explaining, e)
	}

	txs, err := b.LookupAll(ctx, explaining)
	if err != nil {
		return Clearing{}, err
	}

	readable := map[types.ID]bool{}
	byID := map[types.ID]Clearer{}

	for _, t := range txs {
		ok, seen := readable[t.AccountID]
		if !seen {
			_, access, err := b.accounts.Account(ctx, actor, t.AccountID)
			switch {
			case errors.Is(err, ErrNotFound):
			case err != nil:
				return Clearing{}, err
			default:
				ok = access.Can(tenancybus.Read)
			}

			readable[t.AccountID] = ok
		}

		if !ok {
			byID[t.ID] = Clearer{Hidden: true}

			continue
		}

		byID[t.ID] = Clearer{Transaction: Transaction{ID: t.ID, AccountID: t.AccountID, PostedOn: t.PostedOn, Description: t.Description, Amount: t.Amount}}
	}

	for line, e := range lineOf {
		c.By[line] = byID[e]
	}

	return c, nil
}
