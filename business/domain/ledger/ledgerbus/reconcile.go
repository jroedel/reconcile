package ledgerbus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Reconciling (docs/plan.md, "Reconcile").
//
// A reconciliation is a person's word that a statement agrees with the
// paper one the bank sent, for a period they name. It is kept beside the
// statement rather than as a state of it, so that "reconciled" is one row
// that exists or does not: marking is an insert that a second person
// marking at the same moment finds already done, and reopening is a delete
// with a line of history giving the reason.
//
// The period is the person's, not the file's. A CSV states no period, so a
// statement read from one spans its first row to its last, and a July
// export whose first charge is on the 2nd would leave the 1st in no
// statement at all -- every month of an account amber forever, for a day
// on which nothing happened. Reconciling against the paper is exactly the
// moment somebody can say "nothing happened on the 1st", so the period may
// be widened then, and never narrowed below what the file listed.
//
// While a period is reconciled, every transaction of the account posted in
// it is locked: its parts, their categories and projects cannot change,
// a statement cannot bring new transactions into it, and a statement that
// brought some in cannot be removed. The lock is on the period rather than
// on the statement's own transactions because statements overlap: a card's
// June statement lists what May's already brought in, and reconciling June
// must hold those too. Receipts are not locked. They change nothing an
// accountant adds up, and a receipt found after the month closed is still
// welcome (plan: "Receipts are never required").

// The errors reconciling adds.
var (
	// ErrLocked is a change to a transaction in a reconciled period, or one
	// that would add or remove some.
	ErrLocked = errors.New("that is in a reconciled period; reopen it to change it")

	// ErrReconciled is reconciling a statement that already is.
	ErrReconciled = errors.New("the statement is already reconciled")

	// ErrNotReconciled is reopening a statement that is not reconciled.
	ErrNotReconciled = errors.New("the statement is not reconciled")

	// ErrPeriod is a period that does not cover the statement's own.
	ErrPeriod = errors.New("the period must include every day of the statement")

	// ErrReason is a reopening without a reason, or one too long.
	ErrReason = errors.New("say why it is being reopened")

	// ErrNote is a note too long to keep.
	ErrNote = errors.New("the note is too long")
)

// The actions reconciling writes into the history.
const (
	StatementReconciled eventbus.Action = "statement.reconciled"
	StatementReopened   eventbus.Action = "statement.reopened"
)

// MaxNote is the longest note on a reconciliation, or reason for reopening
// one.
const MaxNote = 500

// MaxWiden is how far a reconciled period may reach beyond its statement's
// own, either side. A month's paper statement against a file that began on
// the 3rd needs days; a year needs a mistake.
const MaxWiden = 31

// Reconciliation is a statement checked against the paper, for a period.
type Reconciliation struct {
	StatementID types.ID
	AccountID   types.ID
	Start, End  types.Date
	Note        string
	By          types.ID
	At          time.Time
}

// Made reports whether there is one.
func (r Reconciliation) Made() bool { return !r.StatementID.Zero() }

// Covers reports whether the day is in its period.
func (r Reconciliation) Covers(d types.Date) bool {
	return r.Made() && !d.Before(r.Start) && !r.End.Before(d)
}

// Reconciled reports whether the statement has been reconciled.
func (s Statement) Reconciled() bool { return s.Reconciliation.Made() }

// Period is the days a statement answers for: its own, or the wider one it
// was reconciled for.
func (s Statement) Period() (types.Date, types.Date) {
	if s.Reconciled() {
		return s.Reconciliation.Start, s.Reconciliation.End
	}

	return s.Start, s.End
}

// Review is everything the statement's page shows about reconciling it.
type Review struct {
	Statement Statement
	Account   tenancybus.Account
	Access    tenancybus.Access

	// Transactions is every one of the account's posted in the
	// statement's period, whichever statement brought it in, oldest first.
	Transactions []Transaction
	In, Out      money.Amount

	// Unsorted is those of them with a part that has no category.
	Unsorted []Transaction

	// Overlapping is other reconciliations of the account whose periods
	// share a day with this one's: reopening this one does not unlock
	// those days.
	Overlapping []Reconciliation
}

// Net is what the period did to the account.
func (r Review) Net() money.Amount { return r.In + r.Out }

// Review reads a statement for reconciling. Anyone who may read the account
// may see it; only a bookkeeper may act on it.
func (b *Business) Review(ctx context.Context, actor, id types.ID) (Review, error) {
	st, err := b.store.StatementByID(ctx, id)
	if err != nil {
		return Review{}, err
	}

	account, access, err := b.accounts.Account(ctx, actor, st.AccountID)
	if err != nil {
		return Review{}, err
	}

	rv := Review{Statement: st, Account: account, Access: access}

	start, end := st.Period()

	if rv.Transactions, err = b.store.Transactions(ctx, st.AccountID, start, end.AddDays(1)); err != nil {
		return Review{}, err
	}

	for _, t := range rv.Transactions {
		if t.Amount > 0 {
			rv.In += t.Amount
		} else {
			rv.Out += t.Amount
		}

		if !t.Sorted() {
			rv.Unsorted = append(rv.Unsorted, t)
		}
	}

	recs, err := b.store.Reconciliations(ctx, st.AccountID)
	if err != nil {
		return Review{}, err
	}

	for _, r := range recs {
		if r.StatementID != st.ID && !r.End.Before(start) && !end.Before(r.Start) {
			rv.Overlapping = append(rv.Overlapping, r)
		}
	}

	return rv, nil
}

// Reconcile marks a statement reconciled for the period from..to, which
// must include the statement's own and reach no more than MaxWiden days
// beyond it. Bookkeep on the account.
func (b *Business) Reconcile(ctx context.Context, now time.Time, actor, id types.ID, from, to types.Date, note string) (Statement, error) {
	st, access, err := b.Statement(ctx, actor, id)
	if err != nil {
		return Statement{}, err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return Statement{}, ErrForbidden
	}

	if st.Reconciled() {
		return st, ErrReconciled
	}

	if st.Format == Hand {
		return st, ErrHand
	}

	if from.Zero() {
		from = st.Start
	}

	if to.Zero() {
		to = st.End
	}

	if st.Start.Before(from) || to.Before(st.End) ||
		from.Before(st.Start.AddDays(-MaxWiden)) || st.End.AddDays(MaxWiden).Before(to) {
		return st, ErrPeriod
	}

	if note = strings.TrimSpace(note); utf8.RuneCountInString(note) > MaxNote {
		return st, ErrNote
	}

	r := Reconciliation{StatementID: st.ID, AccountID: st.AccountID, Start: from, End: to, Note: note, By: actor, At: now}

	ev := eventbus.New(now, actor, types.AccountScope(st.AccountID), StatementReconciled, reconcileDetail(st, r, ""))

	made, err := b.store.Reconcile(ctx, r, ev)
	if err != nil {
		return Statement{}, err
	}

	if !made {
		return st, ErrReconciled
	}

	st.Reconciliation = r

	b.log.Info("a statement was reconciled", "account_id", st.AccountID.String(), "statement_id", st.ID.String())

	return st, nil
}

// Reopen takes a statement's reconciliation away, unlocking its period
// unless another reconciliation also covers it. A reason is required, and
// kept in the history: reopening a month the accountant has already been
// given is the thing an audit trail is for. Bookkeep on the account.
func (b *Business) Reopen(ctx context.Context, now time.Time, actor, id types.ID, reason string) (Statement, error) {
	st, access, err := b.Statement(ctx, actor, id)
	if err != nil {
		return Statement{}, err
	}

	if !access.Can(tenancybus.Bookkeep) {
		return Statement{}, ErrForbidden
	}

	if !st.Reconciled() {
		return st, ErrNotReconciled
	}

	if reason = strings.TrimSpace(reason); reason == "" || utf8.RuneCountInString(reason) > MaxNote {
		return st, ErrReason
	}

	ev := eventbus.New(now, actor, types.AccountScope(st.AccountID), StatementReopened, reconcileDetail(st, st.Reconciliation, reason))

	gone, err := b.store.Reopen(ctx, st.ID, ev)
	if err != nil {
		return Statement{}, err
	}

	if !gone {
		return st, ErrNotReconciled
	}

	st.Reconciliation = Reconciliation{}

	b.log.Info("a statement was reopened", "account_id", st.AccountID.String(), "statement_id", st.ID.String())

	return st, nil
}

func reconcileDetail(st Statement, r Reconciliation, reason string) map[string]string {
	d := map[string]string{"name": st.FileName, "start": r.Start.String(), "end": r.End.String()}
	if reason != "" {
		d["reason"] = reason
	}

	return d
}

// lock is the reconciliation that holds a day of an account still, if one
// does.
func (b *Business) lock(ctx context.Context, account types.ID, day types.Date) (Reconciliation, error) {
	recs, err := b.store.Reconciliations(ctx, account)
	if err != nil {
		return Reconciliation{}, err
	}

	for _, r := range recs {
		if r.Covers(day) {
			return r, nil
		}
	}

	return Reconciliation{}, nil
}

// --- month by month -----------------------------------------------------------

// Cover is how a month of an account stands.
type Cover string

const (
	// Reconciled: every day is in a reconciled period.
	Reconciled Cover = "reconciled"

	// Imported: every day is in some statement, and not all of them are
	// reconciled yet.
	Imported Cover = "imported"

	// Partial: some days are in no statement. Gaps says which.
	Partial Cover = "partial"

	// Missing: no statement reaches the month at all. The state worth
	// acting on, and the one a list of transactions cannot show: a month
	// with nothing in it looks the same as a month nobody imported.
	Missing Cover = "missing"

	// Current: the month is still going. Not missing, merely not over; a
	// month flagged for the twenty-nine days before it could be complete
	// is a flag nobody reads by the thirtieth.
	Current Cover = "current"
)

// Span is a run of days, both ends included.
type Span struct {
	From, To types.Date
}

// MonthCover is one month of an account, for the month-by-month page.
type MonthCover struct {
	Month string // "2026-07"
	State Cover

	// Gaps is the runs of the month's days in no statement, for Partial.
	Gaps []Span

	// Statements is those whose period shares a day with the month,
	// oldest first.
	Statements []Statement

	// Count and Unsorted are the month's transactions, and those with a
	// part that has no category.
	Count, Unsorted int
}

// Coverage is an account's months, newest first, from the month of its
// earliest statement to the month of today.
func (b *Business) Coverage(ctx context.Context, actor, accountID types.ID, today types.Date) ([]MonthCover, error) {
	if _, _, err := b.accounts.Account(ctx, actor, accountID); err != nil {
		return nil, err
	}

	statements, err := b.store.Statements(ctx, accountID)
	if err != nil {
		return nil, err
	}

	// An entry by hand covers its one day for nobody: the bank's statement
	// for that month is still what is missing or not.
	statements = slices.DeleteFunc(statements, func(st Statement) bool { return st.Format == Hand })

	months, err := b.store.Months(ctx, accountID)
	if err != nil {
		return nil, err
	}

	return BuildCoverage(statements, months, today), nil
}

// BuildCoverage works out each month from the statements' periods. A
// function of its arguments alone, so that the edges of months can be
// tested without a database.
func BuildCoverage(statements []Statement, months []Month, today types.Date) []MonthCover {
	if len(statements) == 0 {
		return nil
	}

	statements = slices.Clone(statements)
	slices.SortFunc(statements, func(a, b Statement) int { return strings.Compare(a.Start.String(), b.Start.String()) })

	sums := make(map[string]Month, len(months))
	for _, m := range months {
		sums[m.Month] = m
	}

	// The account's records begin with its earliest statement. The days
	// of that month before it are not a gap: nobody was asked for them.
	origin := statements[0].Start
	for _, st := range statements {
		if s, _ := st.Period(); s.Before(origin) {
			origin = s
		}
	}

	first := monthOf(origin)
	last := monthOf(today)

	for _, st := range statements {
		if _, end := st.Period(); last.Before(monthOf(end)) {
			last = monthOf(end)
		}
	}

	var out []MonthCover

	for m := first; !last.Before(m); m = nextMonth(m) {
		mc := MonthCover{Month: m.String()[:7], Count: sums[m.String()[:7]].Count, Unsorted: sums[m.String()[:7]].Unsorted}
		end := nextMonth(m).AddDays(-1)

		for _, st := range statements {
			if s, e := st.Period(); !e.Before(m) && !end.Before(s) {
				mc.Statements = append(mc.Statements, st)
			}
		}

		if monthOf(today) == m {
			mc.State = Current
			out = append(out, mc)

			continue
		}

		covered, reconciled := 0, 0
		days := 0

		var gap *Span

		from := m
		if m.Before(origin) {
			from = origin
		}

		for d := from; !end.Before(d); d = d.AddDays(1) {
			days++

			in, locked := false, false

			for _, st := range mc.Statements {
				if s, e := st.Period(); !d.Before(s) && !e.Before(d) {
					in = true
					locked = locked || st.Reconciled()
				}
			}

			switch {
			case in:
				covered++
				gap = nil
			case gap == nil:
				mc.Gaps = append(mc.Gaps, Span{From: d, To: d})
				gap = &mc.Gaps[len(mc.Gaps)-1]
			default:
				gap.To = d
			}

			if locked {
				reconciled++
			}
		}

		switch {
		case reconciled == days:
			mc.State = Reconciled
		case covered == days:
			mc.State = Imported
		case covered > 0:
			mc.State = Partial
		default:
			mc.State, mc.Gaps = Missing, nil
		}

		out = append(out, mc)
	}

	slices.Reverse(out)

	return out
}

// monthOf is the first day of d's month.
func monthOf(d types.Date) types.Date {
	if d.Zero() {
		return d
	}

	m, _ := types.ParseDate(d.String()[:7] + "-01")

	return m
}

// nextMonth is the first day of the month after m, which is a first day.
func nextMonth(m types.Date) types.Date { return monthOf(m.AddDays(32)) }
