package ledgerbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A bank's statement for two accounts in one document: imported into the
// account whose number ends as one of them does, that one is read, and
// checked by its daily balances and its beginning and ending balance.
// Into an account that says no number, the preview asks which.
func TestAStatementOfSeveralAccounts(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")

	acct := func(last4 string) types.ID {
		a, err := w.ten.CreateAccount(ctx, now, me, types.ID{}, tenancybus.AccountFields{Name: "Checking " + last4, Kind: "checking", Last4: last4})
		if err != nil {
			t.Fatal(err)
		}

		return a.ID
	}

	first := acct(pdfsourcetest.First)
	file := w.save(me, "statements.pdf", pdfsourcetest.Consolidated())

	d, err := w.ledger.Prepare(ctx, me, first, file, nil)
	if err != nil {
		t.Fatal(err)
	}

	if d.Choose || d.Part != pdfsourcetest.First || len(d.Parts) != 2 || len(d.Result.Records) != 8 {
		t.Fatalf("the draft: choose %v, part %q, %d parts, %d rows", d.Choose, d.Part, len(d.Parts), len(d.Result.Records))
	}

	if d.Check.Method != ledgerbus.ByBalances || !d.Check.OK || d.Check.Closing != money.MustParse(pdfsourcetest.FirstClosing) {
		t.Errorf("the check: %+v", d.Check)
	}

	st, err := w.ledger.Import(ctx, now, me, first, file, ledgerbus.Options{})
	if err != nil || st.Added != 8 {
		t.Fatalf("import: %+v, %v", st, err)
	}

	// An account that says no number: the preview asks, and an import
	// without an answer is refused.
	unnamed := w.account(me, "checking")
	file = w.save(me, "statements-again.pdf", pdfsourcetest.Consolidated())

	if d, err := w.ledger.Prepare(ctx, me, unnamed, file, nil); err != nil || !d.Choose || len(d.Parts) != 2 || d.Ready() {
		t.Errorf("an account with no number: choose %v, %d parts, ready %v, %v", d.Choose, len(d.Parts), d.Ready(), err)
	}

	if _, err := w.ledger.Import(ctx, now, me, unnamed, file, ledgerbus.Options{}); !errors.Is(err, ledgerbus.ErrWhichAccount) {
		t.Errorf("importing without choosing: %v", err)
	}

	d, err = w.ledger.Prepare(ctx, me, unnamed, file, &ledgerbus.Options{Part: pdfsourcetest.Second})
	if err != nil || d.Choose || len(d.Result.Records) != 2 || !d.Check.OK {
		t.Errorf("the second account chosen: %+v, %v", d.Check, err)
	}
}
