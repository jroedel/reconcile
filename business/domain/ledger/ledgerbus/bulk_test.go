package ledgerbus_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A month's files at once: a consolidated statement's two parts go to the
// accounts whose numbers end as theirs, an OFX download to the one account
// its number fits -- not one the treasurer only views -- a CSV and a card's
// printed activity to the accounts their layouts went to before, and a CSV
// in a layout never seen is asked about. Those that need nobody are
// imported together; the rest wait.
func TestABulkImport(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	other := w.user("other@example.org")

	acct := func(owner types.ID, name, kind, last4 string) types.ID {
		a, err := w.ten.CreateAccount(ctx, now, owner, types.ID{}, tenancybus.AccountFields{Name: name, Kind: kind, Last4: last4})
		if err != nil {
			t.Fatal(err)
		}

		return a.ID
	}

	first := acct(me, "Checking A", "checking", pdfsourcetest.First)
	second := acct(me, "Checking B", "checking", pdfsourcetest.Second)
	savings := acct(me, "Savings", "savings", "")
	card := acct(me, "Card", "card", "")
	downloaded := acct(me, "Downloaded", "checking", "1234")

	// The same number on an account the treasurer only views.
	viewed := acct(other, "Somebody else's", "checking", "1234")
	w.grant(other, types.AccountScope(viewed), "treasurer@example.org", tenancybus.Viewer)

	// What the site has seen before: July's CSV into savings, and the
	// card's activity into the card.
	w.imports(me, savings, "checking-july.csv")

	activity := printout("$1,246.56")
	if _, err := w.ledger.Import(ctx, now, me, card, w.save(me, "activity.pdf", activity), ledgerbus.Options{Invert: true}); err != nil {
		t.Fatal(err)
	}

	testdata := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}

		return data
	}

	files := []types.ID{
		w.save(me, "statements.pdf", pdfsourcetest.Consolidated()),
		w.save(me, "august.csv", testdata("checking-august.csv")),
		w.save(me, "july.ofx", testdata("checking-july.ofx")),
		w.save(me, "activity again.pdf", activity),
		w.save(me, "no-balance.csv", testdata("no-balance.csv")),
		w.save(other, "not mine.csv", testdata("checking-august.csv")),
	}

	props, err := w.ledger.Propose(ctx, me, files, nil)
	if err != nil {
		t.Fatal(err)
	}

	type want struct {
		account    types.ID
		by         ledgerbus.Proposed
		unattended bool
	}

	wants := map[string]want{
		files[0].String() + "-" + pdfsourcetest.First:  {first, ledgerbus.ByNumber, true},
		files[0].String() + "-" + pdfsourcetest.Second: {second, ledgerbus.ByNumber, true},
		files[1].String(): {savings, ledgerbus.ByMemory, true},
		files[2].String(): {downloaded, ledgerbus.ByNumber, false},
		files[3].String(): {card, ledgerbus.ByMemory, false},
		files[4].String(): {},
	}

	if len(props) != len(wants) {
		t.Fatalf("%d proposals", len(props))
	}

	for _, p := range props {
		want, ok := wants[p.Key()]
		if !ok || p.Account != want.account || p.By != want.by || p.Unattended() != want.unattended || p.Problem != nil {
			t.Errorf("%s: account %v by %q, unattended %v, problem %v; check %+v ready %v",
				p.File.Name, p.Account == want.account, p.By, p.Unattended(), p.Problem, p.Draft.Check, p.Draft.Ready())
		}
	}

	// The card's activity is in its account already, and says so.
	if p := props[4]; !p.Imported() {
		t.Errorf("the card's activity again: imported %v", p.Imported())
	}

	n, err := w.ledger.ImportProposals(ctx, now, me, props)
	if err != nil || n != 3 {
		t.Fatalf("imported %d: %v", n, err)
	}

	// Asked, and answered: the CSV with no balances is for savings, where
	// it waits for its preview, since nothing in it can be checked.
	key := files[4].String()

	props, err = w.ledger.Propose(ctx, me, files, map[string]types.ID{key: savings, files[2].String(): viewed})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range props {
		switch p.Key() {
		case key:
			if p.Account != savings || p.By != ledgerbus.ByPerson || p.Unattended() {
				t.Errorf("chosen: account %v by %q, unattended %v", p.Account == savings, p.By, p.Unattended())
			}
		case files[2].String():
			// An account the treasurer only views is not theirs to choose.
			if p.Account != downloaded || p.By != ledgerbus.ByNumber {
				t.Errorf("chosen an account only viewed: %v by %q", p.Account == viewed, p.By)
			}
		default:
			if !p.Imported() {
				t.Errorf("%s %s: not imported", p.File.Name, p.Part)
			}
		}
	}
}
