package ledgerbus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// year is the four checks of payments paid and sorted: the plumber's first
// check written in the December before and the second partly a transfer,
// Silva's first a transfer (repaying what she paid for the parish) and
// her second not sorted yet.
func year(w *world) (paid, types.ID, types.ID) {
	w.t.Helper()

	ctx := w.t.Context()
	p := newPaid(w)

	cat := func(name string, kind categorybus.Kind) types.ID {
		c, err := w.cats.Create(ctx, now, p.owner, p.org, name, kind)
		if err != nil {
			w.t.Fatal(err)
		}

		return c.ID
	}

	repairs, transfers := cat("Repairs", categorybus.Expense), cat("Between accounts", categorybus.Transfer)

	p.pay(w, "1176", "Hilltop Plumbing")
	p.pay(w, "1177", "HILLTOP PLUMBING")
	p.pay(w, "1178", "Rosa Silva")
	p.pay(w, "1179", "Rosa Silva")

	split := func(number string, parts ...ledgerbus.Part) {
		if _, err := w.ledger.SetSplits(ctx, now, p.owner, p.checks[number], parts); err != nil {
			w.t.Fatalf("splitting %s: %v", number, err)
		}
	}

	split("1176", ledgerbus.Part{Amount: -12000, CategoryID: repairs})
	split("1177", ledgerbus.Part{Amount: -2000, CategoryID: repairs}, ledgerbus.Part{Amount: -1000, CategoryID: transfers})
	split("1178", ledgerbus.Part{Amount: -4500, CategoryID: transfers})

	written, _ := types.ParseDate("2025-12-30")
	if _, err := w.ledger.SetWritten(ctx, now, p.owner, p.checks["1176"], "", written); err != nil {
		w.t.Fatal(err)
	}

	list := p.list(w)

	return p, named(list, "Hilltop Plumbing").ID, named(list, "Rosa Silva").ID
}

// A year counts each payment in the year of the day its check was written,
// where that is known, and of the day it posted otherwise; only its parts
// of expense kind, and apart, those not sorted yet; and the largest first.
func TestPaymentsByPayeeCountTheYear(t *testing.T) {
	w := newWorld(t)
	p, plumber, silva := year(w)
	ctx := t.Context()

	got, err := w.ledger.PaymentsByPayee(ctx, p.owner, p.org, 2026)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Reported) != 0 || len(got.Others) != 2 {
		t.Fatalf("2026: %+v", got)
	}

	first, second := got.Others[0], got.Others[1]

	switch {
	case first.Payee.ID != silva || len(first.Payments) != 1 || first.Payments[0].ID != p.checks["1179"]:
		t.Errorf("the largest first, and a transfer to Silva not counted: %+v", first)
	case first.Sums[0].Paid != 0 || first.Sums[0].Unsorted != -6000 || first.Payments[0].Written:
		t.Errorf("Silva's sums: %+v", first.Sums)
	case second.Payee.ID != plumber || len(second.Payments) != 1 || second.Sums[0].Paid != -2000 || second.Sums[0].Unsorted != 0:
		t.Errorf("the plumber's 2026, its transfer part not counted: %+v", second)
	case second.Payments[0].On.String() != "2026-07-09" || second.Sums[0].Currency != "USD":
		t.Errorf("the plumber's payment: %+v", second.Payments[0])
	}

	before, err := w.ledger.PaymentsByPayee(ctx, p.owner, p.org, 2025)
	if err != nil || len(before.Others) != 1 || before.Others[0].Payee.ID != plumber {
		t.Fatalf("2025: %+v, %v", before, err)
	}

	if pay := before.Others[0].Payments[0]; !pay.Written || pay.On.String() != "2025-12-30" || pay.PostedOn.String() != "2026-07-02" || pay.Paid != -12000 {
		t.Errorf("a check written in December: %+v", pay)
	}

	if one, err := w.ledger.PayeeInYear(ctx, p.owner, plumber, 2026); err != nil || len(one.Payments) != 1 || one.Sums[0].Paid != -2000 {
		t.Errorf("the plumber alone: %+v, %v", one, err)
	}

	if _, err := w.ledger.PaymentsByPayee(ctx, p.owner, p.org, 1800); !errors.Is(err, ledgerbus.ErrYear) {
		t.Errorf("a year from before: %v", err)
	}

	stranger := w.user("stranger@example.org")
	if _, err := w.ledger.PaymentsByPayee(ctx, stranger, p.org, 2026); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, err := w.ledger.PayeeInYear(ctx, stranger, plumber, 2026); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger, one payee: %v", err)
	}
}

// The marks are a bookkeeper's or an owner's, each change a line of
// history, never through a key; an accountant reads them. A reported payee
// is first in its year whether it was paid in it or not.
func TestReportedPayees(t *testing.T) {
	w := newWorld(t)
	p, plumber, silva := year(w)
	ctx := t.Context()

	accountant := w.user("accountant@example.org")
	w.grant(p.owner, p.org, "accountant@example.org", tenancybus.Accountant)

	marks := ledgerbus.PayeeMarks{Reported: true, DetailsOnFile: true, Note: "  Form received in March.  "}

	if _, err := w.ledger.SetPayeeMarks(ctx, now, accountant, silva, marks); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("an accountant: %v", err)
	}

	if _, err := w.ledger.SetPayeeMarks(eventbus.WithVia(ctx, "Claude"), now, p.owner, silva, marks); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("through a key: %v", err)
	}

	if _, err := w.ledger.SetPayeeMarks(ctx, now, p.owner, silva, ledgerbus.PayeeMarks{Note: strings.Repeat("x", ledgerbus.MaxPayeeNote+1)}); !errors.Is(err, ledgerbus.ErrPayeeNote) {
		t.Errorf("a long note: %v", err)
	}

	got, err := w.ledger.SetPayeeMarks(ctx, now, p.owner, silva, marks)
	if err != nil || !got.Reported || !got.DetailsOnFile || got.Note != "Form received in March." {
		t.Fatalf("marked: %+v, %v", got, err)
	}

	if read, _, err := w.ledger.Payee(ctx, accountant, silva); err != nil || !read.Reported || !read.DetailsOnFile || read.Note != "Form received in March." {
		t.Errorf("the accountant reads: %+v, %v", read, err)
	}

	for action, want := range map[eventbus.Action]int{ledgerbus.PayeeReported: 1, ledgerbus.PayeeDetails: 1, ledgerbus.PayeeNoted: 1} {
		if n := w.lines(p.org, action); n != want {
			t.Errorf("%d lines of %s, want %d", n, action, want)
		}
	}

	// Saved again unchanged: no line of history.
	if _, err := w.ledger.SetPayeeMarks(ctx, now, p.owner, silva, marks); err != nil {
		t.Fatal(err)
	}

	if n := w.lines(p.org, ledgerbus.PayeeReported); n != 1 {
		t.Errorf("saved unchanged: %d lines", n)
	}

	years, err := w.ledger.PaymentsByPayee(ctx, accountant, p.org, 2026)
	if err != nil || len(years.Reported) != 1 || years.Reported[0].Payee.ID != silva || len(years.Others) != 1 || years.Others[0].Payee.ID != plumber {
		t.Errorf("2026 with Silva reported: %+v, %v", years, err)
	}

	if empty, err := w.ledger.PaymentsByPayee(ctx, p.owner, p.org, 2024); err != nil || len(empty.Reported) != 1 || len(empty.Reported[0].Payments) != 0 || len(empty.Others) != 0 {
		t.Errorf("a year nobody was paid in: %+v, %v", empty, err)
	}

	// Unmarked, a line each.
	if _, err := w.ledger.SetPayeeMarks(ctx, now, p.owner, silva, ledgerbus.PayeeMarks{}); err != nil {
		t.Fatal(err)
	}

	for action, want := range map[eventbus.Action]int{ledgerbus.PayeeUnreported: 1, ledgerbus.PayeeNoDetails: 1, ledgerbus.PayeeNoted: 2} {
		if n := w.lines(p.org, action); n != want {
			t.Errorf("%d lines of %s, want %d", n, action, want)
		}
	}

	// The package's question: which payees are these, asking nobody.
	byID, err := w.ledger.PayeesByID(ctx, []types.ID{plumber, plumber, silva, {}})
	if err != nil || len(byID) != 2 || byID[plumber].Name != "Hilltop Plumbing" {
		t.Errorf("by id: %+v, %v", byID, err)
	}
}
