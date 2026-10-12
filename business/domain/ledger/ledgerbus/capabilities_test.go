package ledgerbus_test

import (
	"slices"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// cans is a draft's capabilities as name=answer, in order.
func cans(caps []ledgerbus.Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = c.Name + "=" + string(c.Can)
	}

	return out
}

// The issue's case (#111): a card's OFX with the bank's identifiers and
// names cut short, and its CSV of the same days with the whole names and
// no identifier. Neither has balances. Each says what it has, and the two
// together say what each brings and that the balances are still missing.
func TestWhatAnOFXAndACSVCanDoTogether(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	card := w.account(me, "card")

	ofx, csv := w.save(me, "december.ofx", cardOFX()), w.save(me, "december.csv", cardCSV())
	props := w.propose(me, card, ofx, csv)

	if len(props) != 2 {
		t.Fatalf("%d proposals", len(props))
	}

	for i, want := range [][]string{
		{"balances=no", "identifiers=yes", "whole=no", "checks=unknown", "number=no"},
		{"balances=no", "identifiers=no", "whole=yes", "checks=unknown", "number=no"},
	} {
		if got := cans(props[i].Draft.Capabilities()); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", props[i].File.Name, got, want)
		}
	}

	together := ledgerbus.Togethers(props)
	if len(together) != 1 {
		t.Fatalf("together: %+v", together)
	}

	g := together[0]
	if got := cans(g.Capabilities); !slices.Equal(got, []string{"balances=no", "identifiers=yes", "whole=yes"}) {
		t.Errorf("together: %v", got)
	}

	if g.Everything() || !slices.Equal(g.Missing(), []string{"balances"}) || g.Account != card || len(g.Keys) != 2 {
		t.Errorf("together: %+v", g)
	}

	if !slices.Equal(g.Bring["identifiers"], []string{"december.ofx"}) || !slices.Equal(g.Bring["whole"], []string{"december.csv"}) || g.Bring["balances"] != nil {
		t.Errorf("what each brings: %v", g.Bring)
	}

	// One file alone, or files of days that do not meet, have nobody to
	// be together with.
	if got := ledgerbus.Togethers(props[:1]); len(got) != 0 {
		t.Errorf("one file together: %+v", got)
	}

	january := w.save(me, "january.csv", csvOf("2026-01-05,BAKERY,-4.00"))
	if got := ledgerbus.Togethers(w.propose(me, card, ofx, january)); len(got) != 0 {
		t.Errorf("December and January together: %+v", got)
	}
}

// A consolidated PDF that checks by its balances has them, and has no
// identifiers because no PDF does; it names its account.
func TestWhatAPDFCanDo(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	me := w.user("treasurer@example.org")

	a, err := w.ten.CreateAccount(t.Context(), now, me, types.ID{}, tenancybus.AccountFields{Name: "Checking", Kind: "checking", Last4: pdfsourcetest.First})
	if err != nil {
		t.Fatal(err)
	}

	d, err := w.ledger.Prepare(t.Context(), me, a.ID, w.save(me, "statements.pdf", pdfsourcetest.Consolidated()), nil)
	if err != nil {
		t.Fatal(err)
	}

	caps := d.Capabilities()
	if got := cans(caps[:2]); !slices.Equal(got, []string{"balances=yes", "identifiers=na"}) || caps[len(caps)-1].Can != ledgerbus.Has {
		t.Errorf("the PDF: %v", cans(caps))
	}

	// A card's printed page: no balances, only its total; a name cut short;
	// and every charge's holder, said once the card keeps its holders
	// apart -- for a card of one holder it is nothing to say.
	card := w.account(me, "card")

	page := w.save(me, "activity.pdf", printout("$1,246.56"))

	d, err = w.ledger.Prepare(t.Context(), me, card, page, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := cans(d.Capabilities()); !slices.Equal(got, []string{"balances=no", "identifiers=na", "whole=no", "checks=unknown", "number=no"}) {
		t.Errorf("the printed page, one holder: %v", got)
	}

	if err := w.ledger.SetByHolder(t.Context(), now, me, card, true); err != nil {
		t.Fatal(err)
	}

	if d, err = w.ledger.Prepare(t.Context(), me, card, page, nil); err != nil {
		t.Fatal(err)
	}

	if got := cans(d.Capabilities()); !slices.Equal(got, []string{"balances=no", "identifiers=na", "whole=no", "checks=unknown", "holders=yes", "number=no"}) {
		t.Errorf("the printed page: %v", got)
	}
}

// A checking account's CSV with a balance on every row has balances, and
// says nothing of holders, which are a card's.
func TestACSVWithABalanceOnEveryRow(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	checking := w.account(me, "checking")

	d, _ := w.prepare(me, checking, "checking-july.csv")

	if got := cans(d.Capabilities()); !slices.Equal(got, []string{"balances=yes", "identifiers=no", "whole=yes", "checks=unknown", "number=no"}) {
		t.Errorf("the CSV: %v", got)
	}
}
