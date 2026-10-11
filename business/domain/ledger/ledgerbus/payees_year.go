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

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Reported payees and the year (docs/payees.md, 3 and 4).
//
// The app does not know which payments a law says must be declared, nor of
// whom: that differs by country and changes with the years, and is the
// accountant's to know. So it keeps one mark a person sets on a payee --
// report this payee's payments to the accountant -- and one fact about
// papers -- their tax details are on file, yes or no, never the tax number
// itself -- with a note, and it adds up the calendar year.
//
// # The marks are a person's
//
// They are the organization's word about real people, which the accountant
// relies on, so they are set by a bookkeeper or an owner on the site, and
// never through a key: Claude can read them and say which payee looks as
// though it should be reported, but not say so for the organization. An
// accountant reads them and changes none.
//
// # What a year counts
//
// A payment is in the year of the day written on its check, where that is
// known, and otherwise of the day it posted: a check written on 30
// December and cleared on 3 January belongs to the year it was written.
// Which day a law uses is the accountant's call, so each payment says which
// it was given.
//
// What counts of a payment is its parts of expense kind, and apart from
// them its parts not sorted yet, which may be expenses once they are.
// Transfers and pass-through are not payments to a payee -- repaying
// somebody what they paid on the organization's behalf is not paying them
// -- and nor is income. A payment none of whose parts count is left out.

// The history's lines for the marks, in the list's owner's history.
const (
	PayeeReported   eventbus.Action = "payee.reported"
	PayeeUnreported eventbus.Action = "payee.unreported"
	PayeeDetails    eventbus.Action = "payee.details-on-file"
	PayeeNoDetails  eventbus.Action = "payee.details-not-on-file"
	PayeeNoted      eventbus.Action = "payee.noted"
)

// MaxPayeeNote is how long a payee's note may be.
const MaxPayeeNote = 500

// ErrPayeeNote is a note longer than MaxPayeeNote.
var ErrPayeeNote = errors.New("a payee's note is at most 500 characters")

// ErrYear is a year the page cannot show.
var ErrYear = errors.New("choose a year from 1900 to 9999")

// PayeeMarks is what a person says about a payee.
type PayeeMarks struct {
	Reported      bool
	DetailsOnFile bool
	Note          string
}

// SetPayeeMarks saves the marks on a payee, a line of history for each
// that changed. It is a bookkeeper's or an owner's, on the site: through a
// key it is refused, since the marks are the organization's word about
// real people. Saving them says a person has looked at the payee, so a
// program's marks come off it, changed or not.
func (b *Business) SetPayeeMarks(ctx context.Context, now time.Time, actor, id types.ID, m PayeeMarks) (Payee, error) {
	if eventbus.ViaFrom(ctx) != "" {
		return Payee{}, ErrForbidden
	}

	p, err := b.keptPayee(ctx, actor, id)
	if err != nil {
		return Payee{}, err
	}

	m.Note = strings.TrimSpace(m.Note)
	if utf8.RuneCountInString(m.Note) > MaxPayeeNote {
		return p, ErrPayeeNote
	}

	on := func(v bool) string { return strconv.FormatBool(v) }

	var events []eventbus.Event

	add := func(action eventbus.Action, detail map[string]string) {
		detail["name"] = p.Name
		events = append(events, eventbus.New(now, actor, p.Owner, action, detail))
	}

	if m.Reported != p.Reported {
		add(map[bool]eventbus.Action{true: PayeeReported, false: PayeeUnreported}[m.Reported], map[string]string{"on": on(m.Reported)})
	}

	if m.DetailsOnFile != p.DetailsOnFile {
		add(map[bool]eventbus.Action{true: PayeeDetails, false: PayeeNoDetails}[m.DetailsOnFile], map[string]string{"on": on(m.DetailsOnFile)})
	}

	if m.Note != p.Note {
		add(PayeeNoted, map[string]string{"note": m.Note, "before": p.Note})
	}

	if len(events) == 0 && !p.Marked() {
		return p, nil
	}

	p.Reported, p.DetailsOnFile, p.Note = m.Reported, m.DetailsOnFile, m.Note
	p.Via, p.BroughtVia = "", nil

	return p, b.store.SetPayeeMarks(ctx, p, events)
}

// YearPayment is a payment to a payee, as a year counts it.
type YearPayment struct {
	PayeePayment

	// On is the day that put it in the year, and Written whether that is
	// the day written on its check rather than the day it posted.
	On      types.Date
	Written bool

	// Paid is its parts of expense kind, Unsorted its parts not sorted
	// yet: signed, money out negative, as everywhere in the books.
	Paid, Unsorted money.Amount
}

// PayeeSum is a payee's year in one currency.
type PayeeSum struct {
	Currency       string
	Paid, Unsorted money.Amount
}

// Total is what it counts all together, sorted or not.
func (s PayeeSum) Total() money.Amount { return s.Paid + s.Unsorted }

// PayeeYear is one payee's year: its sums, one per currency in the order
// of the alphabet, and its payments, oldest first.
type PayeeYear struct {
	Payee    Payee
	Sums     []PayeeSum
	Payments []YearPayment
}

// largest is the most it was paid in any one currency, as money out
// counts: the larger the payments, the larger this is.
func (y PayeeYear) largest() money.Amount {
	var most money.Amount
	for _, s := range y.Sums {
		most = max(most, -s.Total())
	}

	return most
}

// PaymentsByPayee is a list's year.
type PaymentsByPayee struct {
	Year   int
	Owner  types.Scope
	Access tenancybus.Access

	// Reported is the payees marked to be reported, every one of them
	// whether it was paid in the year or not; Others is everybody else
	// who was. Each largest first, so that someone who should have been
	// reported is seen.
	Reported, Others []PayeeYear
}

// YearSpan is the first and last day of a calendar year, or ErrYear.
func YearSpan(year int) (from, to types.Date, err error) {
	if year < 1900 || year > 9999 {
		return types.Date{}, types.Date{}, ErrYear
	}

	y := strconv.Itoa(year)
	from, _ = types.ParseDate(y + "-01-01")
	to, _ = types.ParseDate(y + "-12-31")

	return from, to, nil
}

// PaymentsByPayee is the payments to a list's payees in a calendar year,
// for whoever may read the list.
func (b *Business) PaymentsByPayee(ctx context.Context, actor types.ID, owner types.Scope, year int) (PaymentsByPayee, error) {
	from, to, err := YearSpan(year)
	if err != nil {
		return PaymentsByPayee{}, err
	}

	list, access, err := b.Payees(ctx, actor, owner)
	if err != nil {
		return PaymentsByPayee{}, err
	}

	payments, err := b.store.YearPayments(ctx, owner, types.ID{}, from, to)
	if err != nil {
		return PaymentsByPayee{}, err
	}

	kinds, err := b.kindsOf(ctx, owner)
	if err != nil {
		return PaymentsByPayee{}, err
	}

	years := yearsOf(list, payments, kinds)
	out := PaymentsByPayee{Year: year, Owner: owner, Access: access}

	for _, p := range list {
		y, paid := years[p.ID]

		switch {
		case p.Reported:
			if !paid {
				y = PayeeYear{Payee: p}
			}

			out.Reported = append(out.Reported, y)
		case paid:
			out.Others = append(out.Others, y)
		}
	}

	for _, ys := range [][]PayeeYear{out.Reported, out.Others} {
		slices.SortStableFunc(ys, func(a, b PayeeYear) int {
			if c := cmp.Compare(b.largest(), a.largest()); c != 0 {
				return c
			}

			return compareFold(a.Payee.Name, b.Payee.Name)
		})
	}

	return out, nil
}

// PayeeInYear is one payee's payments in a calendar year, for whoever may
// read its list.
func (b *Business) PayeeInYear(ctx context.Context, actor, id types.ID, year int) (PayeeYear, error) {
	from, to, err := YearSpan(year)
	if err != nil {
		return PayeeYear{}, err
	}

	p, _, err := b.Payee(ctx, actor, id)
	if err != nil {
		return PayeeYear{}, err
	}

	payments, err := b.store.YearPayments(ctx, p.Owner, p.ID, from, to)
	if err != nil {
		return PayeeYear{}, err
	}

	kinds, err := b.kindsOf(ctx, p.Owner)
	if err != nil {
		return PayeeYear{}, err
	}

	if y, ok := yearsOf([]Payee{p}, payments, kinds)[p.ID]; ok {
		return y, nil
	}

	return PayeeYear{Payee: p}, nil
}

// PayeesByID is the payees with these ids, asking nobody's permission:
// for the accountant's package, which has asked about the account whose
// transactions name them.
func (b *Business) PayeesByID(ctx context.Context, ids []types.ID) (map[types.ID]Payee, error) {
	var once []types.ID

	for _, id := range ids {
		if !id.Zero() && !slices.Contains(once, id) {
			once = append(once, id)
		}
	}

	list, err := b.store.PayeesByID(ctx, once)
	if err != nil {
		return nil, err
	}

	out := make(map[types.ID]Payee, len(list))
	for _, p := range list {
		out[p.ID] = p
	}

	return out, nil
}

// kindsOf is the kind of each category of an owner's list, which is the
// list a payee list's payments are sorted with: they share an owner.
func (b *Business) kindsOf(ctx context.Context, owner types.Scope) (map[types.ID]categorybus.Kind, error) {
	cats, err := b.categories.Of(ctx, owner)
	if err != nil {
		return nil, err
	}

	kinds := make(map[types.ID]categorybus.Kind, len(cats))
	for _, c := range cats {
		kinds[c.ID] = c.Kind
	}

	return kinds, nil
}

// yearsOf counts payments into their payees' years. A payment with nothing
// that counts is left out, and a payee with no payment left has no year.
func yearsOf(list []Payee, payments []PayeePayment, kinds map[types.ID]categorybus.Kind) map[types.ID]PayeeYear {
	byID := make(map[types.ID]Payee, len(list))
	for _, p := range list {
		byID[p.ID] = p
	}

	out := map[types.ID]PayeeYear{}

	for _, pp := range payments {
		yp := YearPayment{PayeePayment: pp, On: pp.PostedOn}
		if !pp.WrittenOn.Zero() {
			yp.On, yp.Written = pp.WrittenOn, true
		}

		counted := false

		for _, s := range pp.Splits {
			switch kind := kinds[s.CategoryID]; {
			case s.CategoryID.Zero() || kind == categorybus.Unsaid:
				yp.Unsorted += s.Amount
				counted = true
			case kind == categorybus.Expense:
				yp.Paid += s.Amount
				counted = true
			}
		}

		p, ok := byID[pp.PayeeID]
		if !counted || !ok {
			continue
		}

		y := out[p.ID]
		y.Payee = p
		y.Payments = append(y.Payments, yp)

		i := slices.IndexFunc(y.Sums, func(s PayeeSum) bool { return s.Currency == pp.Currency })
		if i < 0 {
			y.Sums = append(y.Sums, PayeeSum{Currency: pp.Currency})
			slices.SortFunc(y.Sums, func(a, b PayeeSum) int { return strings.Compare(a.Currency, b.Currency) })
			i = slices.IndexFunc(y.Sums, func(s PayeeSum) bool { return s.Currency == pp.Currency })
		}

		y.Sums[i].Paid += yp.Paid
		y.Sums[i].Unsorted += yp.Unsorted

		out[p.ID] = y
	}

	return out
}
