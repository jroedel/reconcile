package reportbus

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// --- a project ------------------------------------------------------------------------

// ProjectChoices is what a person chooses of a project's report. A field
// added later gets a default here, as IncomeExpensesChoices' do.
type ProjectChoices struct {
	// Gap is how many days with nothing in the project end a run of its
	// activity (an Episode): 30 unless chosen. A retreat whose deposits
	// trickle in for six weeks wants a longer one, and that is the saved
	// report's choice, not the counter volunteer's: nobody labels a
	// charge with its retreat while sorting.
	Gap int

	// HideAccounts leaves the account column out, for a project that
	// draws on one account and a sheet that needs the width.
	HideAccounts bool
}

// The quiet gap's default and its bounds. A day is the shortest that
// means anything; past a year, a project's runs are its years, which the
// period already chooses.
const (
	DefaultGap = 30
	MaxGap     = 365
)

func (c ProjectChoices) normal() ProjectChoices {
	if c.Gap < 1 || c.Gap > MaxGap {
		c.Gap = DefaultGap
	}

	return c
}

// Figures is what some of a project's parts came to, each kind apart:
// the questions a clearing account's reader asks of each retreat -- was it
// real income and expenses, or only money passing through, and did it come
// out even. PassThroughOut is as signed as the money, negative.
type Figures struct {
	Income, Expenses              money.Amount
	PassThroughIn, PassThroughOut money.Amount
	Transfers                     money.Amount
	Unsorted                      money.Amount
	Count                         int
}

func (f *Figures) add(kind categorybus.Kind, a money.Amount) {
	f.Count++

	switch kind {
	case categorybus.Income:
		f.Income += a
	case categorybus.Expense:
		f.Expenses += a
	case categorybus.Transfer:
		f.Transfers += a
	case categorybus.PassThrough:
		if a > 0 {
			f.PassThroughIn += a
		} else {
			f.PassThroughOut += a
		}
	default:
		f.Unsorted += a
	}
}

// Net is income and expenses together: what the project did, as Total's
// is.
func (f Figures) Net() money.Amount { return f.Income + f.Expenses }

// PassThrough is pass-through's net: what is still to come back (below
// zero) or still to pass on (above).
func (f Figures) PassThrough() money.Amount { return f.PassThroughIn + f.PassThroughOut }

// Operated reports whether any income or expenses moved.
func (f Figures) Operated() bool { return f.Income != 0 || f.Expenses != 0 }

// PassedThrough reports whether any pass-through moved.
func (f Figures) PassedThrough() bool { return f.PassThroughIn != 0 || f.PassThroughOut != 0 }

// Sum is everything together: what the parts did to the running balance.
func (f Figures) Sum() money.Amount { return f.Net() + f.PassThrough() + f.Transfers + f.Unsorted }

// Part is one of the project's parts, the kind it counts as -- Unsaid for
// one not sorted yet -- and the running balance after it.
type Part struct {
	ledgerbus.ProjectLine
	Kind    categorybus.Kind
	Balance money.Amount
}

// Episode is a run of the project's activity: parts with no quiet gap
// between them, named by their first and last day. Balance is the running
// balance after its last part, which is the question of a retreat: whether
// the project was back where it started when it was over.
type Episode struct {
	From, To types.Date
	Parts    []Part
	Figures
	Balance money.Amount
}

// ProjectSection is a project's report in one currency.
type ProjectSection struct {
	Currency string

	// BroughtForward is the running balance of every part before the
	// period, and Before how many parts it adds up: a period that starts
	// after the project did opens with what it had done by then.
	BroughtForward money.Amount
	Before         int

	Episodes []Episode

	// Figures is the period's, and Balance the running balance at its end:
	// zero is a project that came back to where it started.
	Figures
	Balance money.Amount
}

// ProjectReport is the style's report.
type ProjectReport struct {
	Frame
	Choices  ProjectChoices
	Sections []ProjectSection
}

// ProjectReport is a project's every part, with its running balance, in
// runs of activity (docs/reports.md, "A project"), for anyone who may read
// the project. A zero period is the project's life.
//
// The running balance counts everything sorted into the project -- income,
// expenses, pass-through, the leg of a transfer that arrives in it, and
// what is not sorted yet -- because that is what the project has done to
// the organization's accounts: a clearing account's balance, which is
// Total.WithTransfers with the unsorted in too, since they moved the money
// all the same.
func (b *Business) ProjectReport(ctx context.Context, now time.Time, actor types.ID, scope types.Scope, p Period, c ProjectChoices) (ProjectReport, error) {
	if scope.Kind != types.ScopeProject {
		return ProjectReport{}, ErrNotFound
	}

	if p.Zero() {
		p = Period{All: true}
	}

	c = c.normal()

	// Every part, not only the period's: the balance brought forward is
	// those before it.
	a, err := b.read(ctx, actor, Question{Scope: scope, Period: Period{All: true}})
	if err != nil {
		return ProjectReport{}, err
	}

	from, to := p.span()

	var before, during []ledgerbus.ProjectLine

	for _, l := range a.lines {
		switch {
		case l.PostedOn.Before(from):
			before = append(before, l)
		case l.PostedOn.Before(to):
			during = append(during, l)
		}
	}

	a.lines, a.Period = during, p
	a.First, a.Last = months(during)
	a.Cells = sum(during, []Dimension{ByKind, ByCategory, ByAccount, ByMonth}, nil, nil)

	return ProjectReport{Frame: frame(a, types.DateOf(now)), Choices: c, Sections: projectSections(before, during, c.Gap)}, nil
}

// projectSections lays a project's parts out a currency at a time: what
// came before the period as one balance, and the period's in episodes.
func projectSections(before, during []ledgerbus.ProjectLine, gap int) []ProjectSection {
	byCurrency := map[string]*ProjectSection{}

	section := func(cur string) *ProjectSection {
		s, ok := byCurrency[cur]
		if !ok {
			s = &ProjectSection{Currency: cur}
			byCurrency[cur] = s
		}

		return s
	}

	for _, l := range before {
		s := section(l.Currency)
		s.BroughtForward += l.Split.Amount
		s.Balance += l.Split.Amount
		s.Before++
	}

	for _, l := range during {
		s := section(l.Currency)
		kind := kindOf(l)

		s.Balance += l.Split.Amount
		s.add(kind, l.Split.Amount)

		// More than gap days since the last part: a quiet gap, and a new
		// run. Thirty days from the 1st is the 31st, still the same run.
		if n := len(s.Episodes); n == 0 || s.Episodes[n-1].To.AddDays(gap).Before(l.PostedOn) {
			s.Episodes = append(s.Episodes, Episode{From: l.PostedOn})
		}

		e := &s.Episodes[len(s.Episodes)-1]
		e.To, e.Balance = l.PostedOn, s.Balance
		e.add(kind, l.Split.Amount)
		e.Parts = append(e.Parts, Part{ProjectLine: l, Kind: kind, Balance: s.Balance})
	}

	out := make([]ProjectSection, 0, len(byCurrency))
	for _, s := range byCurrency {
		out = append(out, *s)
	}

	slices.SortFunc(out, func(a, b ProjectSection) int { return strings.Compare(a.Currency, b.Currency) })

	return out
}

// months is the earliest and latest months the parts are in, "" for none.
func months(lines []ledgerbus.ProjectLine) (first, last string) {
	for _, l := range lines {
		m := l.PostedOn.String()[:7]
		if first == "" || m < first {
			first = m
		}

		last = max(last, m)
	}

	return first, last
}

// figures is the report's fingerprint for a note: the period's sums, and
// what was brought forward into it, since sorting a part before the
// period changes the balances the note was written beside. The quiet gap
// is not in it: where the runs fall is a way of reading, not money.
func (r ProjectReport) figures() string {
	var more []string
	for _, s := range r.Sections {
		more = append(more, fmt.Sprintf("forward|%s|%d|%d", s.Currency, s.BroughtForward, s.Before))
	}

	return fingerprint(r.Answer, more...)
}
