package ledgerbus_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
)

// A PDF is read once and remembered (readings.go), and each draft of it
// still has rows of its own: a card's preview turns them round, and the
// next preview of the same file, for an account that is not a card, reads
// them as the file prints them -- however many times either is read.
func TestARememberedReadingIsEachDraftsOwn(t *testing.T) {
	needPoppler(t)

	w := newWorld(t)
	ctx := t.Context()
	me := w.user("treasurer@example.org")
	card, checking := w.account(me, "card"), w.account(me, "checking")
	file := w.save(me, "activity.pdf", printout("$1,246.56"))

	first := func(account ledgerbus.Draft) string { return account.Result.Records[0].Amount.String() }

	var asCard, asChecking []string

	for range 3 {
		d, err := w.ledger.Prepare(ctx, me, card, file, nil)
		if err != nil || !d.Invert {
			t.Fatalf("the card's preview: invert %v, %v", d.Invert, err)
		}

		asCard = append(asCard, first(d))

		if d, err = w.ledger.Prepare(ctx, me, checking, file, nil); err != nil || d.Invert {
			t.Fatalf("the checking account's preview: invert %v, %v", d.Invert, err)
		}

		asChecking = append(asChecking, first(d))
	}

	for i := range asCard {
		if asCard[i] != asCard[0] || asChecking[i] != asChecking[0] || asCard[i] == asChecking[i] {
			t.Errorf("the first row, read as a card and not: %v and %v", asCard, asChecking)

			break
		}
	}
}
