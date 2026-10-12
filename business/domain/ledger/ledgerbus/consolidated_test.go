package ledgerbus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource/pdfsourcetest"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A bank's statement for three accounts in one document: imported into the
// account whose number ends as one of them does, that one is read, and
// checked by its daily balances and its beginning and ending balance.
// Into an account that says no number, the preview asks which. The account
// with no activity that month imports a statement with no transactions.
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

	if d.Choose || d.Part != pdfsourcetest.First || len(d.Parts) != 3 || len(d.Result.Records) != 8 {
		t.Fatalf("the draft: choose %v, part %q, %d parts, %d rows", d.Choose, d.Part, len(d.Parts), len(d.Result.Records))
	}

	if d.Check.Method != ledgerbus.ByBalances || !d.Check.OK || d.Check.Closing != money.MustParse(pdfsourcetest.FirstClosing) {
		t.Errorf("the check: %+v", d.Check)
	}

	st, err := w.ledger.Import(ctx, now, me, first, file, ledgerbus.Options{})
	if err != nil || st.Added != 8 {
		t.Fatalf("import: %+v, %v", st, err)
	}

	// The checks paid keep their numbers, and nothing else has one.
	txs, err := w.ledger.Transactions(ctx, me, first, "2026-09")
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{}

	for _, tx := range txs {
		if tx.CheckNumber != "" || strings.HasPrefix(tx.Description, "Check ") {
			checks[tx.CheckNumber] = tx.Description
		}
	}

	if len(checks) != 3 || !strings.HasPrefix(checks["1001"], "Check 1001") || !strings.HasPrefix(checks["1002"], "Check 1002") ||
		!strings.HasPrefix(checks["1003"], "Check 1003") {
		t.Errorf("the checks: %q", checks)
	}

	// An account that says no number: the preview asks, and an import
	// without an answer is refused.
	unnamed := w.account(me, "checking")
	file = w.save(me, "statements-again.pdf", pdfsourcetest.Consolidated())

	if d, err := w.ledger.Prepare(ctx, me, unnamed, file, nil); err != nil || !d.Choose || len(d.Parts) != 3 || d.Ready() {
		t.Errorf("an account with no number: choose %v, %d parts, ready %v, %v", d.Choose, len(d.Parts), d.Ready(), err)
	}

	if _, err := w.ledger.Import(ctx, now, me, unnamed, file, ledgerbus.Options{}); !errors.Is(err, ledgerbus.ErrWhichAccount) {
		t.Errorf("importing without choosing: %v", err)
	}

	d, err = w.ledger.Prepare(ctx, me, unnamed, file, &ledgerbus.Options{Part: pdfsourcetest.Second})
	if err != nil || d.Choose || len(d.Result.Records) != 2 || !d.Check.OK {
		t.Errorf("the second account chosen: %+v, %v", d.Check, err)
	}

	// The savings account had nothing that month. Its part is a statement
	// with no transactions, and the month is imported rather than missing.
	quiet := acct(pdfsourcetest.Third)

	d, err = w.ledger.Prepare(ctx, me, quiet, file, nil)
	if err != nil || d.Part != pdfsourcetest.Third || !d.Quiet() || !d.Check.OK || !d.Ready() {
		t.Fatalf("the quiet account: part %q, quiet %v, check %+v, ready %v, %v", d.Part, d.Quiet(), d.Check, d.Ready(), err)
	}

	st, err = w.ledger.Import(ctx, now, me, quiet, file, ledgerbus.Options{})
	balance := money.MustParse(pdfsourcetest.ThirdBalance)
	if err != nil || st.Added != 0 || st.Opening != balance || st.Closing != balance || st.Start.String() != "2026-09-01" || st.End.String() != "2026-09-30" {
		t.Fatalf("the quiet account's import: %+v, %v", st, err)
	}

	months, err := w.ledger.Coverage(ctx, me, quiet, types.DateOf(now))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(months, func(m ledgerbus.MonthCover) bool { return m.Month == "2026-09" && m.State == ledgerbus.Imported }) {
		t.Errorf("the quiet month: %+v", months)
	}
}
