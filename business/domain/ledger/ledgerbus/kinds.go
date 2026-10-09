package ledgerbus

import (
	"context"
	"slices"
	"strings"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Operations is money by kind (docs/plan.md, "Kinds of money"), in one
// currency: what it was, beside the cash view's money in and out.
//
// Each is a signed sum. Expenses is negative, and a refund in an expense
// category makes it less so; that is the point of adding up by kind rather
// than by sign. Unsorted is the parts nobody has given a category, or whose
// category's kind its owner has not said: counted nowhere else until they
// are, and shown so that it is plain the totals are not finished.
type Operations struct {
	Income, Expenses       money.Amount
	Transfers, PassThrough money.Amount
	Unsorted               money.Amount
}

// Net is income and expenses together: what the operations did. Transfers
// and pass-through are never in it.
func (o Operations) Net() money.Amount { return o.Income + o.Expenses }

// Add counts an amount of a kind; Unsaid, which is also a part with no
// category, is unsorted.
func (o *Operations) Add(kind categorybus.Kind, a money.Amount) {
	switch kind {
	case categorybus.Income:
		o.Income += a
	case categorybus.Expense:
		o.Expenses += a
	case categorybus.Transfer:
		o.Transfers += a
	case categorybus.PassThrough:
		o.PassThrough += a
	default:
		o.Unsorted += a
	}
}

// --- what should come back to zero ----------------------------------------------

// Settled is one balance that should come back to zero, in one currency:
// a pass-through category's, or a list's transfers.
type Settled struct {
	Category categorybus.Category // zero for the transfers together
	Currency string
	Balance  money.Amount

	// Months is the months whose own sum is not zero, newest first: for
	// transfers, a payment that left one account and did not arrive in
	// another that month. One that crosses a month's end shows in both
	// until the other side is imported.
	Months []MonthBalance
}

// MonthBalance is a month's sum.
type MonthBalance struct {
	Month   string
	Balance money.Amount
}

// Settling is what should come back to zero on a category list: each
// pass-through category's balance, and the transfers across the list's
// accounts (every account of an organization, or the one personal
// account). The pass-through balances are listed whatever they are, so
// that a zero is visible as settled; the transfers, too.
type Settling struct {
	PassThrough []Settled
	Transfers   []Settled
}

// SettlingRow is the store's sum of one category, in one currency, in one
// month.
type SettlingRow struct {
	Category categorybus.Category
	Currency string
	Month    string
	Sum      money.Amount
}

// Settling is the balances of a category list that should come back to
// zero. Whoever may read the list's owner may read them: they are sums
// over accounts the owner's readers already see.
func (b *Business) Settling(ctx context.Context, actor types.ID, owner types.Scope) (Settling, error) {
	var err error

	switch owner.Kind {
	case types.ScopeOrg:
		var access tenancybus.Access

		if access, err = b.accounts.AccessTo(ctx, actor, owner); err == nil && !access.Can(tenancybus.Read) {
			err = ErrNotFound
		}
	case types.ScopeAccount:
		_, _, err = b.accounts.Account(ctx, actor, owner.ID)
	default:
		err = ErrNotFound
	}

	if err != nil {
		return Settling{}, err
	}

	rows, err := b.store.Settling(ctx, owner)
	if err != nil {
		return Settling{}, err
	}

	return settle(rows), nil
}

// settle adds the rows up: a balance per pass-through category and
// currency, and one per currency for the transfers together.
func settle(rows []SettlingRow) Settling {
	type key struct {
		category types.ID // zero for transfers
		currency string
	}

	balances := map[key]*Settled{}
	months := map[key]map[string]money.Amount{}

	var order []key

	for _, r := range rows {
		k := key{currency: r.Currency}
		if r.Category.Kind == categorybus.PassThrough {
			k.category = r.Category.ID
		}

		s, ok := balances[k]
		if !ok {
			s = &Settled{Currency: r.Currency}
			if !k.category.Zero() {
				s.Category = r.Category
			}

			balances[k], months[k] = s, map[string]money.Amount{}
			order = append(order, k)
		}

		s.Balance += r.Sum
		months[k][r.Month] += r.Sum
	}

	var out Settling

	for _, k := range order {
		s := balances[k]

		for m, sum := range months[k] {
			if sum != 0 {
				s.Months = append(s.Months, MonthBalance{Month: m, Balance: sum})
			}
		}

		slices.SortFunc(s.Months, func(a, b MonthBalance) int { return strings.Compare(b.Month, a.Month) })

		if k.category.Zero() {
			out.Transfers = append(out.Transfers, *s)
		} else {
			out.PassThrough = append(out.PassThrough, *s)
		}
	}

	slices.SortFunc(out.PassThrough, func(a, b Settled) int {
		if c := strings.Compare(a.Category.Name, b.Category.Name); c != 0 {
			return c
		}

		return strings.Compare(a.Currency, b.Currency)
	})
	slices.SortFunc(out.Transfers, func(a, b Settled) int { return strings.Compare(a.Currency, b.Currency) })

	return out
}
