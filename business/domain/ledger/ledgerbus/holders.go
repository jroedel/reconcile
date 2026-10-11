package ledgerbus

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Statements split by holder (docs/clearing.md, 3).
//
// An account with several holders -- a person or card on it, most often
// each person on a shared card -- whose statements arrive one file per
// holder: a printout of each person's month, sent by whoever holds the
// account. "Holder" rather than "cardholder" because nothing here is about
// cards, and rather than "segment" or "sub-account", which an accountant
// reads as parts of a chart of accounts. Imported into one account, each file is compared with the
// others as if they were downloads of the same thing, and two people who
// buy the same coffee on the same day for the same price are one charge
// to every rule that does not know whose it was. The option makes the
// holder part of what a charge is: its identity (ContentKey), and the
// count and cut-short rules, compare one holder's rows only with that
// holder's and with rows that name nobody.
//
// It is an option because, without it, nothing about any other account
// may change: the holder is read and kept whatever the account, and is
// part of nothing unless it is on.

// HolderKey is a holder's name as identities compare it: without case
// or runs of spaces, as a description is.
func HolderKey(h string) string { return normalizeDescription(h) }

// SameHolder reports whether two rows may be one charge as far as their
// holders say: the same one, or either names nobody -- a file for the
// whole account is compared with every holder's.
func SameHolder(a, b string) bool { return a == "" || b == "" || HolderKey(a) == HolderKey(b) }

// AccountSplit is the history line of turning the option on or off.
const AccountSplit eventbus.Action = "account.split"

// MaxHolder is the longest holder's name a person may type.
const MaxHolder = 100

// holders fills in what a draft needs to know about holders, and
// gives a file that names none the holder a person chose for it.
func (b *Business) holders(ctx context.Context, d *Draft, opts *Options) error {
	on, err := b.store.ByHolder(ctx, d.Account.ID)
	if err != nil || !on {
		return err
	}

	d.ByHolder = true

	if d.Holders, err = b.store.Holders(ctx, d.Account.ID); err != nil {
		return err
	}

	if opts != nil {
		if h := strings.Join(strings.Fields(opts.Holder), " "); utf8.RuneCountInString(h) <= MaxHolder {
			d.Holder = h
		}
	}

	for i, r := range d.Result.Records {
		if r.Holder != "" {
			continue
		}

		d.Unnamed++

		if d.Holder != "" {
			d.Result.Records[i].Holder = d.Holder
		}
	}

	return nil
}

// looksByHolder notices a file that looks like one holder's month on an
// account whose statements are not kept apart by holder (issue #81).
//
// Imported without the option, a holder's file is compared with the
// other holders' as if all were downloads of one thing: two people's
// charges of one amount on one day are one charge to the count rule, and
// one person's pending charge can be taken by another's posted one. So
// before the first such file goes in, the preview says what it sees and
// asks for the option. What it sees is narrow on purpose: every row of
// the file names the same holder, and the account already has rows that
// name somebody else on the file's days. A file of the whole account
// names several holders or none, and the first holder's file of an
// account has nobody else to be confused with.
func (b *Business) looksByHolder(ctx context.Context, d *Draft) error {
	if d.ByHolder || len(d.Result.Records) == 0 {
		return nil
	}

	holder := d.Result.Records[0].Holder

	from, to := types.DateOf(d.Result.Records[0].Date), types.DateOf(d.Result.Records[0].Date)

	for _, r := range d.Result.Records {
		if r.Holder == "" || HolderKey(r.Holder) != HolderKey(holder) {
			return nil
		}

		day := types.DateOf(r.Date)
		if day.Before(from) {
			from = day
		}

		if to.Before(day) {
			to = day
		}
	}

	named, err := b.store.HoldersBetween(ctx, d.Account.ID, from, to)
	if err != nil {
		return err
	}

	others := slices.DeleteFunc(named, func(h string) bool { return HolderKey(h) == HolderKey(holder) })
	if len(others) == 0 {
		return nil
	}

	d.LooksByHolder, d.FileHolder, d.OtherHolders = true, holder, others

	return nil
}

// OtherNames is the other holders, for a sentence: names, which no
// translation changes.
func (d Draft) OtherNames() string { return strings.Join(d.OtherHolders, ", ") }

// ByHolder reports whether an account's statements arrive one file per
// holder. Anyone who may read the account may know.
func (b *Business) ByHolder(ctx context.Context, actor, accountID types.ID) (bool, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return false, err
	}

	return b.store.ByHolder(ctx, accountID)
}

// SetByHolder turns the option on or off: an owner's choice, because it
// changes what every stored row of the account is, and the store works out
// each one's identity again in the same transaction.
func (b *Business) SetByHolder(ctx context.Context, now time.Time, actor, accountID types.ID, on bool) error {
	account, access, err := b.accounts.Account(ctx, actor, accountID)
	if err != nil {
		return err
	}

	if !access.Can(tenancybus.Manage) {
		return ErrForbidden
	}

	value := "0"
	if on {
		value = "1"
	}

	ev := eventbus.New(now, actor, account.Scope(), AccountSplit, map[string]string{"on": value})

	return b.store.SetByHolder(ctx, accountID, on, actor, ev)
}

// HolderTotal is what one holder's rows come to in a month.
type HolderTotal struct {
	Holder string
	Count  int
	Sum    money.Amount
}

// HolderMonth is a month of a split account by holder: each one's
// total, and who is missing -- a holder with rows in either of the two
// months before and none in this one, which is how a file that never
// arrived shows. "September: 5 of 6 holders."
type HolderMonth struct {
	Totals  []HolderTotal
	Missing []string
}

// Expected is how many holders the month should have.
func (m HolderMonth) Expected() int {
	n := len(m.Missing)

	for _, t := range m.Totals {
		if t.Holder != "" {
			n++
		}
	}

	return n
}

// MissingNames is who is missing, for a sentence: names, which no
// translation changes.
func (m HolderMonth) MissingNames() string { return strings.Join(m.Missing, ", ") }

// Present is how many of them it has.
func (m HolderMonth) Present() int { return m.Expected() - len(m.Missing) }

// Holders is a month of a split account by holder; a zero
// HolderMonth for an account without the option.
func (b *Business) Holders(ctx context.Context, actor, accountID types.ID, month string) (HolderMonth, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return HolderMonth{}, err
	}

	on, err := b.store.ByHolder(ctx, accountID)
	if err != nil || !on {
		return HolderMonth{}, err
	}

	start, end, err := MonthRange(month)
	if err != nil {
		return HolderMonth{}, ErrNotFound
	}

	totals, err := b.store.HolderTotals(ctx, accountID, start, end)
	if err != nil {
		return HolderMonth{}, err
	}

	before, _, _ := MonthRange(monthBack(start, 2))

	earlier, err := b.store.HolderTotals(ctx, accountID, before, start)
	if err != nil {
		return HolderMonth{}, err
	}

	m := HolderMonth{Totals: totals}

	for _, e := range earlier {
		if e.Holder != "" && !slices.ContainsFunc(totals, func(t HolderTotal) bool { return t.Holder == e.Holder }) {
			m.Missing = append(m.Missing, e.Holder)
		}
	}

	return m, nil
}

// Disagreement is a charge whose holder and whose part's memo name two
// different holders of the account (Disagreeing): Named is the one the
// memo names.
type Disagreement struct {
	Transaction Transaction
	Named       string
}

// Disagreeing is the account's charges whose holder and whose part's memo
// name two different holders of the account, oldest first (issue #81).
//
// A card's printout names who made each charge, and the import writes the
// name both as the row's holder and as its one part's memo. They part
// company only when something changed the row after it was read: before
// this was fixed, one holder's posted charge could take the place of
// another's pending one on an account that did not keep holders apart,
// carrying its own holder in and leaving the first holder's name in the
// memo -- and the first holder's charge out of their month. This is the
// mark that left, so that a person can look. Only a memo that is another
// holder's name counts; a memo a person wrote is nobody's.
func (b *Business) Disagreeing(ctx context.Context, actor, accountID types.ID) ([]Disagreement, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	names, err := b.store.Holders(ctx, accountID)
	if err != nil || len(names) < 2 {
		return nil, err
	}

	known := map[string]string{}
	for _, n := range names {
		known[HolderKey(n)] = n
	}

	txs, err := b.store.NamedInParts(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var out []Disagreement

	for _, t := range txs {
		for _, s := range t.Splits {
			if m := HolderKey(s.Memo); known[m] != "" && m != HolderKey(t.Holder) {
				out = append(out, Disagreement{Transaction: t, Named: known[m]})

				break
			}
		}
	}

	return out, nil
}
