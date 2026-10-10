package ledgerbus_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Statements a script sent to the treasurer's inbox: each file kept once
// however often it is sent, proposed and imported as a bulk import's files
// are, and gone from the inbox once everything in it is imported. One that
// needs a person stays until it is imported or put aside. Nobody else sees
// or touches the treasurer's inbox.
func TestTheStatementInbox(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	other := w.user("other@example.org")

	savings, err := w.ten.CreateAccount(ctx, now, me, types.ID{}, tenancybus.AccountFields{Name: "Savings", Kind: "savings"})
	if err != nil {
		t.Fatal(err)
	}

	// July's CSV was imported by hand, so August's, in the same layout,
	// is proposed for the same account.
	w.imports(me, savings.ID, "checking-july.csv")

	testdata := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}

		return data
	}

	august, unchecked := testdata("checking-august.csv"), testdata("no-balance.csv")

	receive := func(who types.ID, name string, content []byte, want ledgerbus.Received) {
		t.Helper()

		got, err := w.ledger.Receive(ctx, now, who, name, "gmail:"+name, bytes.NewReader(content))
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}

	receive(me, "august.csv", august, ledgerbus.Arrived)
	receive(me, "august again.csv", august, ledgerbus.AlreadyWaiting)
	receive(me, "no-balance.csv", unchecked, ledgerbus.Arrived)

	// Inboxes are each person's: the same file is new to somebody else's,
	// and theirs is not the treasurer's.
	receive(other, "august.csv", august, ledgerbus.Arrived)

	inbox, err := w.ledger.Inbox(ctx, me)
	if err != nil || len(inbox) != 2 || inbox[0].Name != "august.csv" || inbox[0].Source != "gmail:august.csv" {
		t.Fatalf("the inbox: %+v, %v", inbox, err)
	}

	// The inbox's files are a bulk import's list.
	files := []types.ID{inbox[0].FileID, inbox[1].FileID}

	props, err := w.ledger.Propose(ctx, me, files, nil)
	if err != nil || len(props) != 2 {
		t.Fatalf("proposals: %+v, %v", props, err)
	}

	if p := props[0]; p.Account != savings.ID || !p.Unattended() {
		t.Errorf("august: account %v, unattended %v", p.Account == savings.ID, p.Unattended())
	}

	if n, err := w.ledger.ImportProposals(ctx, now, me, props); err != nil || n != 1 {
		t.Fatalf("imported %d: %v", n, err)
	}

	// August is imported and leaves; the CSV nothing can check waits.
	inbox, err = w.ledger.Inbox(ctx, me)
	if err != nil || len(inbox) != 1 || inbox[0].Name != "no-balance.csv" {
		t.Fatalf("the inbox after importing: %+v, %v", inbox, err)
	}

	receive(me, "august.csv", august, ledgerbus.AlreadyImported)
	receive(me, "no-balance.csv", unchecked, ledgerbus.AlreadyWaiting)

	// Nobody else may put the treasurer's file aside.
	if err := w.ledger.Dismiss(ctx, now, other, inbox[0].ID); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("somebody else putting it aside: %v", err)
	}

	if err := w.ledger.Dismiss(ctx, now, me, inbox[0].ID); err != nil {
		t.Fatal(err)
	}

	if inbox, err := w.ledger.Inbox(ctx, me); err != nil || len(inbox) != 0 {
		t.Errorf("the inbox after putting one aside: %+v, %v", inbox, err)
	}

	// Put aside is put aside: sent again, it stays off the list.
	receive(me, "no-balance.csv", unchecked, ledgerbus.AlreadyWaiting)

	if err := w.ledger.Dismiss(ctx, now, me, inbox[0].ID); !errors.Is(err, ledgerbus.ErrNotFound) {
		t.Errorf("putting it aside twice: %v", err)
	}

	// The other person's inbox has their own file, untouched.
	if theirs, err := w.ledger.Inbox(ctx, other); err != nil || len(theirs) != 1 {
		t.Errorf("the other inbox: %+v, %v", theirs, err)
	}
}

// What the inbox does not take, it keeps nothing of.
func TestTheInboxRefusesWhatIsNotAStatement(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")

	if _, err := w.ledger.Receive(t.Context(), now, me, "huge.pdf", "", strings.NewReader(strings.Repeat("x", ledgerbus.MaxFile+1))); !errors.Is(err, filebus.ErrTooBig) {
		t.Errorf("a file too big: %v", err)
	}

	if _, err := w.ledger.Receive(t.Context(), now, me, "empty.csv", "", strings.NewReader("")); !errors.Is(err, ledgerbus.ErrEmpty) {
		t.Errorf("an empty file: %v", err)
	}

	if inbox, err := w.ledger.Inbox(t.Context(), me); err != nil || len(inbox) != 0 {
		t.Errorf("the inbox: %+v, %v", inbox, err)
	}
}
