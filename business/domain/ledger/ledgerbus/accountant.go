package ledgerbus

import (
	"context"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// What the accountant needs explained (docs/accountant.md): which
// transactions a bank statement cannot explain on its own, and whether
// anybody has explained them since. Both are read from what is stored, when
// a list is asked for, and nothing is written when a receipt is attached
// or a payee named, so the lists cannot fall out of step with the books.
// There is no score: a transaction is important for one of four stored
// reasons or it is not, and it has been given context in one of eight
// stored ways or it has not.
//
// The two lists are a choice of the search (explore.go), not a search of
// their own, so that they combine with every other choice as those combine
// with each other, and the API's find_transactions has them for nothing.

// ForAccountant is one of the two lists for the accountant.
type ForAccountant string

// The lists: important transactions nobody has explained yet, the
// bookkeeper's to do; and those somebody has, what the accountant reads.
const (
	MoreContextNeeded ForAccountant = "needed"
	ContextAdded      ForAccountant = "added"
)

// Reason is why a transaction is important to the accountant: what makes
// its bank statement unable to say what it was for.
type Reason string

// The reasons, in the order a row says them.
const (
	// BecauseCheck is a check: it says whom at most, never what for.
	BecauseCheck Reason = "check"

	// BecauseTransfer and BecausePassThrough are a part in a category of
	// that kind: money between our own accounts, and money held for
	// somebody else.
	BecauseTransfer    Reason = "transfer"
	BecausePassThrough Reason = "passthrough"

	// BecauseReference is a transaction an explanation joins to one in an
	// account for reference, in either direction: its other side is kept
	// where these books only mirror it.
	BecauseReference Reason = "reference"

	// BecauseReported is a payment to a payee whose payments are reported
	// to the accountant (docs/payees.md, 3).
	BecauseReported Reason = "reported"
)

// Given is context somebody gave the accountant that the bank did not.
type Given string

// The kinds of context, in the order a row says them. The bank's own
// description is not one, however complete, nor a category: a rule's is
// the app's guess from the same words the accountant already reads, and a
// person's says what kind of money it was, not what it was for.
const (
	GaveDescription Given = "description"
	GaveReceipt     Given = "receipt"
	GaveCheckMemo   Given = "check-memo"
	GaveExplanation Given = "explanation"
	GaveProject     Given = "project"
	GavePartMemo    Given = "part-memo"
	GavePayee       Given = "payee"
	GaveAnswer      Given = "answer"
)

// Facts is what the store reads of a transaction for the lists that is not
// in its own columns or its parts.
type Facts struct {
	// Reported is a payee whose payments are reported.
	Reported bool

	// Explained is an explanation whose lines come to the amount, or one
	// accepted with a note. One accepted without a note says only that the
	// difference was let go, which tells the accountant nothing.
	Explained bool

	// Reference is a transaction an explanation joins to one in an account
	// for reference, as the line or as what the line explains.
	Reference bool
}

// Answered is which of some transactions have a question about them that
// has been answered.
type Answered func(ctx context.Context, ids []types.ID) (map[types.ID]bool, error)

// OnAnswered sets how answered questions are found. Questions are asked
// about transactions, so the ledger is told how to ask them rather than
// made from them, as with receipts (OnReceipts). main sets it once, before
// anything is served; without it, no question has been answered.
func (b *Business) OnAnswered(fn Answered) { b.answered = fn }

// explaining is why a transaction is important to the accountant, and
// what context it has been given, judged on the parts its reader may see:
// on a project's list, the project's parts and never the rest of a charge.
func explaining(t Transaction, parts []Split, kinds map[types.ID]categorybus.Kind, f Facts, receipts int, answered bool) ([]Reason, []Given) {
	var (
		why   []Reason
		given []Given
	)

	has := func(ok func(Split) bool) bool {
		for _, s := range parts {
			if ok(s) {
				return true
			}
		}

		return false
	}

	if t.CheckNumber != "" {
		why = append(why, BecauseCheck)
	}

	for _, k := range []struct {
		kind   categorybus.Kind
		reason Reason
	}{{categorybus.Transfer, BecauseTransfer}, {categorybus.PassThrough, BecausePassThrough}} {
		if has(func(s Split) bool { return !s.CategoryID.Zero() && kinds[s.CategoryID] == k.kind }) {
			why = append(why, k.reason)
		}
	}

	if f.Reference {
		why = append(why, BecauseReference)
	}

	if f.Reported {
		why = append(why, BecauseReported)
	}

	for _, g := range []struct {
		ok    bool
		given Given
	}{
		{t.OwnDescription != "", GaveDescription},
		{receipts > 0, GaveReceipt},
		{t.CheckMemo != "", GaveCheckMemo},
		{f.Explained, GaveExplanation},
		{has(func(s Split) bool { return !s.ProjectID.Zero() }), GaveProject},
		{has(func(s Split) bool { return s.Memo != "" }), GavePartMemo},
		{!t.PayeeID.Zero(), GavePayee},
		{answered, GaveAnswer},
	} {
		if g.ok {
			given = append(given, g.given)
		}
	}

	return why, given
}

// forAccountant keeps the rows in the list asked for, each with its
// reasons and what it was given; rows is what fits the rest of the choices.
// project is the scope's project when it is one, whose parts alone are
// judged. A pending charge since posted is in neither list, as it is in no
// total: the posted charge is the one that counts. A transaction in an
// account for reference is never to do, as nothing there is ever to sort
// (docs/plan.md, "Whose books").
func (b *Business) forAccountant(ctx context.Context, rows []Row, list ForAccountant, project types.ID, kinds map[types.ID]categorybus.Kind, facts map[types.ID]Facts, receipts map[types.ID]int) ([]Row, error) {
	answered := map[types.ID]bool{}

	if b.answered != nil && len(rows) > 0 {
		ids := make([]types.ID, len(rows))
		for i, r := range rows {
			ids[i] = r.Transaction.ID
		}

		var err error
		if answered, err = b.answered(ctx, ids); err != nil {
			return nil, err
		}
	}

	var out []Row

	for _, r := range rows {
		t := r.Transaction
		if t.Cleared() {
			continue
		}

		parts := t.Splits
		if !project.Zero() {
			parts = nil

			for _, s := range t.Splits {
				if s.ProjectID == project {
					parts = append(parts, s)
				}
			}
		}

		r.Why, r.Given = explaining(t, parts, kinds, facts[t.ID], receipts[t.ID], answered[t.ID])

		if listOf(r.Why, r.Given, r.Account) == list {
			out = append(out, r)
		}
	}

	return out, nil
}

// listOf is the list a transaction is in, from why it is important and
// what it was given: none when it is not important, nor when it is in an
// account for reference with nothing given, since nothing there is to do.
func listOf(why []Reason, given []Given, account tenancybus.Account) ForAccountant {
	switch {
	case len(why) == 0:
		return ""
	case len(given) > 0:
		return ContextAdded
	case account.Reference:
		return ""
	default:
		return MoreContextNeeded
	}
}

// Accounting is a transaction's place in the accountant's lists, for its
// page: the list it is in, if any, why, and what it was given.
type Accounting struct {
	List  ForAccountant
	Why   []Reason
	Given []Given
}

// AccountingOf is where the transaction an editor shows stands with the
// accountant. The editor is what Transaction gave the actor, which is the
// actor's leave to read it; the transaction's page already holds one, and
// asking tenancybus again would answer the same.
func (b *Business) AccountingOf(ctx context.Context, e Editor) (Accounting, error) {
	t := e.Transaction
	if t.Cleared() {
		return Accounting{}, nil
	}

	ids := []types.ID{t.ID}

	facts, err := b.store.Facts(ctx, ids)
	if err != nil {
		return Accounting{}, err
	}

	receipts := map[types.ID]int{}
	if b.receipted != nil {
		if receipts, err = b.receipted(ctx, ids); err != nil {
			return Accounting{}, err
		}
	}

	answered := map[types.ID]bool{}
	if b.answered != nil {
		if answered, err = b.answered(ctx, ids); err != nil {
			return Accounting{}, err
		}
	}

	kinds := make(map[types.ID]categorybus.Kind, len(e.Categories))
	for _, c := range e.Categories {
		kinds[c.ID] = c.Kind
	}

	var a Accounting

	a.Why, a.Given = explaining(t, t.Splits, kinds, facts[t.ID], receipts[t.ID], answered[t.ID])
	a.List = listOf(a.Why, a.Given, e.Account)

	return a, nil
}
