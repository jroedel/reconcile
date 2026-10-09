package ledgerbus

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// A personal charge in July and its repayment in August come back to zero,
// with both months shown; a card payment that left checking and arrived on
// the card the same month is settled and shows no month; one that has
// only left is a balance.
func TestWhatComesBackToZero(t *testing.T) {
	personal := categorybus.Category{ID: types.NewID(), Name: "Personal, repaid", Kind: categorybus.PassThrough}
	transfers := categorybus.Category{ID: types.NewID(), Name: "Transfers", Kind: categorybus.Transfer}
	card := categorybus.Category{ID: types.NewID(), Name: "Card payments", Kind: categorybus.Transfer}

	got := settle([]SettlingRow{
		{Category: personal, Currency: "USD", Month: "2026-07", Sum: money.MustParse("-50.00")},
		{Category: personal, Currency: "USD", Month: "2026-08", Sum: money.MustParse("50.00")},
		{Category: transfers, Currency: "USD", Month: "2026-07", Sum: money.MustParse("-300.00")},
		{Category: card, Currency: "USD", Month: "2026-07", Sum: money.MustParse("300.00")},
		{Category: transfers, Currency: "USD", Month: "2026-08", Sum: money.MustParse("-120.00")},
	})

	if len(got.PassThrough) != 1 || got.PassThrough[0].Balance != 0 || len(got.PassThrough[0].Months) != 2 || got.PassThrough[0].Months[0].Month != "2026-08" {
		t.Errorf("pass-through: %+v", got.PassThrough)
	}

	// The transfer categories are one balance: what left one account
	// should arrive in another, whichever category each side was sorted
	// into.
	if len(got.Transfers) != 1 || got.Transfers[0].Balance != money.MustParse("-120.00") ||
		len(got.Transfers[0].Months) != 1 || got.Transfers[0].Months[0].Month != "2026-08" || !got.Transfers[0].Category.ID.Zero() {
		t.Errorf("transfers: %+v", got.Transfers)
	}
}
