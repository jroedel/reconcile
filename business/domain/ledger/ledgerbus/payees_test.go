package ledgerbus_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// payments is an invented statement of four checks, to be paid to payees.
var payments = []byte("Date,Description,Amount,Balance,Check Number\n" +
	"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n" +
	"2026-07-02,CHECK,-120.00,880.00,1176\n" +
	"2026-07-09,CHECK,-30.00,850.00,1177\n" +
	"2026-07-16,CHECK,-45.00,805.00,1178\n" +
	"2026-07-23,CHECK,-60.00,745.00,1179\n")

// paid is a parish with two accounts and the checks above in the first,
// each found by its number.
type paid struct {
	owner           types.ID
	org             types.Scope
	checking, other types.ID
	checks          map[string]types.ID
}

func newPaid(w *world) paid {
	w.t.Helper()

	ctx := w.t.Context()
	owner := w.user("treasurer@example.org")

	o, err := w.ten.CreateOrg(ctx, now, owner, "St. Joseph Parish")
	if err != nil {
		w.t.Fatal(err)
	}

	p := paid{owner: owner, org: o.Scope(), checks: map[string]types.ID{}}

	for i, name := range []string{"Parish checking", "School checking"} {
		a, err := w.ten.CreateAccount(ctx, now, owner, o.ID, tenancybus.AccountFields{Name: name, Kind: "checking"})
		if err != nil {
			w.t.Fatal(err)
		}

		if i == 0 {
			p.checking = a.ID
		} else {
			p.other = a.ID
		}
	}

	w.add(owner, p.checking, "payments.csv", payments)

	for _, n := range []string{"1176", "1177", "1178", "1179"} {
		found, err := w.ledger.WithCheck(ctx, p.checking, n)
		if err != nil || len(found) != 1 {
			w.t.Fatalf("check %s: %+v, %v", n, found, err)
		}

		p.checks[n] = found[0].ID
	}

	return p
}

func (p paid) pay(w *world, number, payee string) ledgerbus.Transaction {
	w.t.Helper()

	t, err := w.ledger.SetPayee(w.t.Context(), now, p.owner, p.checks[number], payee)
	if err != nil {
		w.t.Fatalf("paying check %s to %q: %v", number, payee, err)
	}

	return t
}

func (p paid) list(w *world) []ledgerbus.Payee {
	w.t.Helper()

	list, _, err := w.ledger.Payees(w.t.Context(), p.owner, p.org)
	if err != nil {
		w.t.Fatal(err)
	}

	return list
}

func named(list []ledgerbus.Payee, name string) ledgerbus.Payee {
	i := slices.IndexFunc(list, func(p ledgerbus.Payee) bool { return p.Name == name })
	if i < 0 {
		return ledgerbus.Payee{}
	}

	return list[i]
}

// Writing whom a payment was made to gives it the payee written so: one
// payee for one spelling whatever its case, a new payee for a new one, on
// the organization's one list.
func TestAPaymentsPayeeFollowsItsSpelling(t *testing.T) {
	w := newWorld(t)
	p := newPaid(w)

	first := p.pay(w, "1176", "Hilltop Plumbing")
	again := p.pay(w, "1177", "HILLTOP   plumbing")
	other := p.pay(w, "1178", "Hilltop Plumbing, LLC")

	switch {
	case first.PayeeID.Zero() || first.PayeeID != again.PayeeID:
		t.Errorf("one spelling, two cases: %v, %v", first.PayeeID, again.PayeeID)
	case other.PayeeID == first.PayeeID:
		t.Error("another spelling was the same payee without anybody joining them")
	case again.Payee != "HILLTOP plumbing":
		t.Errorf("the text as written, its spaces tidied: %q", again.Payee)
	}

	list := p.list(w)
	if len(list) != 2 || named(list, "Hilltop Plumbing").Payments != 2 {
		t.Fatalf("the list: %+v", list)
	}

	// Taken away, and the payee keeps its other payment.
	if none := p.pay(w, "1177", ""); !none.PayeeID.Zero() {
		t.Errorf("no payee: %+v", none)
	}

	if got := named(p.list(w), "Hilltop Plumbing"); got.Payments != 1 {
		t.Errorf("after one was taken away: %+v", got)
	}

	// The history of the parish says the payee was added.
	events, _ := w.history.Recent(t.Context(), p.org, 20)
	if !slices.ContainsFunc(events, func(e eventbus.Event) bool { return e.Action == ledgerbus.PayeeAdded }) {
		t.Errorf("the parish's history: %+v", events)
	}
}

// Joining puts one payee's spellings and payments under another, and the
// next payment written the joined way finds the payee it is in now.
// Splitting a spelling off undoes it, bringing back the payee it was.
func TestJoiningAndSplittingPayees(t *testing.T) {
	w := newWorld(t)
	p := newPaid(w)
	ctx := t.Context()

	plumber := p.pay(w, "1176", "Hilltop Plumbing").PayeeID
	llc := p.pay(w, "1177", "Hilltop Plumbing LLC").PayeeID

	joined, err := w.ledger.JoinPayees(ctx, now, p.owner, plumber, []types.ID{llc})
	if err != nil || joined.Payments != 2 || len(joined.Spellings) != 2 {
		t.Fatalf("joined: %+v, %v", joined, err)
	}

	if list := p.list(w); len(list) != 1 || list[0].ID != plumber || list[0].Payments != 2 {
		t.Errorf("the list after joining: %+v", list)
	}

	if gone, _, err := w.ledger.Payee(ctx, p.owner, llc); err != nil || gone.JoinedInto != plumber {
		t.Errorf("the joined payee: %+v, %v", gone, err)
	}

	if next := p.pay(w, "1178", "HILLTOP PLUMBING LLC"); next.PayeeID != plumber {
		t.Errorf("a payment written the joined way: %v", next.PayeeID)
	}

	// The payee's own name cannot be split off.
	if _, err := w.ledger.SplitPayee(ctx, now, p.owner, plumber, "Hilltop Plumbing", types.ID{}); !errors.Is(err, ledgerbus.ErrSplitName) {
		t.Errorf("splitting off its name: %v", err)
	}

	// Split off into a payee of its own: the one joined, back, with the
	// payments written so.
	back, err := w.ledger.SplitPayee(ctx, now, p.owner, plumber, "hilltop plumbing llc", types.ID{})
	if err != nil || back.ID != llc || back.Payments != 2 || back.Joined() {
		t.Fatalf("split off: %+v, %v", back, err)
	}

	list := p.list(w)
	if len(list) != 2 || named(list, "Hilltop Plumbing").Payments != 1 || named(list, "Hilltop Plumbing LLC").Payments != 2 {
		t.Errorf("the list after splitting: %+v", list)
	}

	// Into another payee of the list, by name of a new one.
	roofer := p.pay(w, "1179", "Summit Roofing").PayeeID
	if _, err := w.ledger.RenamePayee(ctx, now, p.owner, llc, "Hilltop Plumbing Company"); err != nil {
		t.Fatal(err)
	}

	into, err := w.ledger.SplitPayee(ctx, now, p.owner, llc, "Hilltop Plumbing LLC", roofer)
	if err != nil || into.ID != roofer || into.Payments != 3 {
		t.Errorf("split into another: %+v, %v", into, err)
	}
}

// Renaming makes the new name a spelling too, and keeps the old one; a
// name another payee is written as is refused.
func TestRenamingAPayee(t *testing.T) {
	w := newWorld(t)
	p := newPaid(w)
	ctx := t.Context()

	plumber := p.pay(w, "1176", "Hilltop Plumbing").PayeeID
	p.pay(w, "1177", "Summit Roofing")

	renamed, err := w.ledger.RenamePayee(ctx, now, p.owner, plumber, "Hilltop Plumbing and Heating")
	if err != nil || len(renamed.Spellings) != 2 {
		t.Fatalf("renamed: %+v, %v", renamed, err)
	}

	if next := p.pay(w, "1178", "hilltop plumbing and heating"); next.PayeeID != plumber {
		t.Error("a payment written as the new name is not the payee")
	}

	if _, err := w.ledger.RenamePayee(ctx, now, p.owner, plumber, "SUMMIT ROOFING"); !errors.Is(err, ledgerbus.ErrPayeeTaken) {
		t.Errorf("renamed as another payee is written: %v", err)
	}

	if _, err := w.ledger.RenamePayee(ctx, now, p.owner, plumber, " "); !errors.Is(err, ledgerbus.ErrPayeeName) {
		t.Errorf("an empty name: %v", err)
	}

	// After a join, the payee may take the joined one's name, which is one
	// of its spellings now.
	llc := p.pay(w, "1179", "Hilltop Plumbing LLC").PayeeID
	if _, err := w.ledger.JoinPayees(ctx, now, p.owner, plumber, []types.ID{llc}); err != nil {
		t.Fatal(err)
	}

	if got, err := w.ledger.RenamePayee(ctx, now, p.owner, plumber, "Hilltop Plumbing LLC"); err != nil || len(got.Spellings) != 3 {
		t.Errorf("renamed as the payee joined into it: %+v, %v", got, err)
	}
}

// A join asks the same of every payee in it: one list the actor keeps the
// books of, a target not archived, and no payee into itself.
func TestJoinsThatAreRefused(t *testing.T) {
	w := newWorld(t)
	p := newPaid(w)
	ctx := t.Context()

	plumber := p.pay(w, "1176", "Hilltop Plumbing").PayeeID
	roofer := p.pay(w, "1177", "Summit Roofing").PayeeID

	if _, err := w.ledger.JoinPayees(ctx, now, p.owner, plumber, []types.ID{plumber}); !errors.Is(err, ledgerbus.ErrJoinSelf) {
		t.Errorf("into itself: %v", err)
	}

	// Another list: the owner's own account, with no organization.
	own := w.account(p.owner, "checking")

	mine, err := w.ledger.CreatePayee(ctx, now, p.owner, types.AccountScope(own), "Hilltop Plumbing")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.JoinPayees(ctx, now, p.owner, plumber, []types.ID{mine.ID}); !errors.Is(err, ledgerbus.ErrJoinLists) {
		t.Errorf("across lists: %v", err)
	}

	if _, err := w.ledger.SetPayeeArchived(ctx, now, p.owner, roofer, true); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ledger.JoinPayees(ctx, now, p.owner, roofer, []types.ID{plumber}); !errors.Is(err, ledgerbus.ErrJoinArchived) {
		t.Errorf("into an archived payee: %v", err)
	}

	// A stranger finds nothing; a viewer of the parish may look, not join.
	stranger := w.user("stranger@example.org")
	if _, err := w.ledger.JoinPayees(ctx, now, stranger, plumber, []types.ID{roofer}); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}

	if _, _, err := w.ledger.Payees(ctx, stranger, p.org); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("a stranger's list: %v", err)
	}

	viewer := w.user("viewer@example.org")
	w.grant(p.owner, p.org, "viewer@example.org", tenancybus.Viewer)

	if list, _, err := w.ledger.Payees(ctx, viewer, p.org); err != nil || len(list) != 2 {
		t.Errorf("a viewer's list: %+v, %v", list, err)
	}

	if _, err := w.ledger.JoinPayees(ctx, now, viewer, plumber, []types.ID{roofer}); !errors.Is(err, ledgerbus.ErrForbidden) {
		t.Errorf("a viewer joining: %v", err)
	}

	// An account in an organization has no list of its own.
	if _, _, err := w.ledger.Payees(ctx, p.owner, types.AccountScope(p.checking)); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("an account in the parish: %v", err)
	}
}

// The suggestions pair payees whose names say they may be one, into the
// one with more payments, until somebody says they are not the same.
func TestPayeeSuggestions(t *testing.T) {
	w := newWorld(t)
	p := newPaid(w)
	ctx := t.Context()

	plumber := p.pay(w, "1176", "Hilltop Plumbing").PayeeID
	p.pay(w, "1177", "Hilltop Plumbing")
	llc := p.pay(w, "1178", "HILLTOP PLUMBING, L.L.C.").PayeeID
	p.pay(w, "1179", "Summit Roofing")

	got, err := w.ledger.PayeeSuggestions(ctx, p.owner, p.org)
	if err != nil || len(got) != 1 || got[0].Into.ID != plumber || got[0].From.ID != llc {
		t.Fatalf("the suggestions: %+v, %v", got, err)
	}

	if err := w.ledger.NotTheSame(ctx, now.Add(time.Minute), p.owner, llc, plumber); err != nil {
		t.Fatal(err)
	}

	if got, _ := w.ledger.PayeeSuggestions(ctx, p.owner, p.org); len(got) != 0 {
		t.Errorf("after not the same: %+v", got)
	}
}

func TestPayeeKey(t *testing.T) {
	for _, c := range []struct{ name, key string }{
		{"Hilltop Plumbing", "hilltop plumbing"},
		{"HILLTOP PLUMBING, LLC", "hilltop plumbing"},
		{"Hilltop Plumbing L.L.C.", "hilltop plumbing"},
		{"Hilltop  Plumbing Inc.", "hilltop plumbing"},
		{"Hilltop Plumbing Co. Ltd.", "hilltop plumbing"},
		{"Serviços Gerais Ltda.", "servicos gerais"},
		{"Construtora Silva ME", "construtora silva"},
		{"Grupo Norte S.A.", "grupo norte"},
		{"Grupo Norte S. A.", "grupo norte"},
		{"José Peña", "jose pena"},
		{"Smith & Sons Corp", "smith sons"},
		{"Co", "co"},
		{"Inc.", "inc"},
		{"", ""},
	} {
		if got := ledgerbus.PayeeKey(c.name); got != c.key {
			t.Errorf("PayeeKey(%q) = %q, want %q", c.name, got, c.key)
		}
	}
}
