package pdfsource_test

import (
	"strings"
	"testing"

	"github.com/jroedel/reconcile/business/domain/importing/sources/pdfsource"
)

// Pending charges, as documents mark them (docs/clearing.md, 4). All
// invented.

// A printed page lists what has not posted at the head of its list, out of
// date order, and states their total beside the others: the rows from the
// head that add up to it are the pending ones.
func TestAStatedPendingTotal(t *testing.T) {
	const page = `
          Sep 1, 2026 to Sep 30, 2026

          Date                     Description                                Amount

          Sep 29, 2026             CITY PARKING HOLD                            $7.25
          Sep 16, 2026             CORNER BAKERY                               $12.00
          Sep 30, 2026             HARDWARE BARN                               $23.75
          Sep 30, 2026             CORNER BAKERY                               $12.00

          End of Activity          Total Activity Date range                   $55.00
                                   Pending purchases                           $19.25
                                   Purchases                                   $35.75
`

	res, err := pdfsource.Read(page)
	if err != nil {
		t.Fatal(err)
	}

	var marked []bool
	for _, r := range res.Records {
		marked = append(marked, r.Pending)
	}

	if len(marked) != 4 || !marked[0] || !marked[1] || marked[2] || marked[3] || res.Unplaced {
		t.Errorf("pending: %v, unplaced %v", marked, res.Unplaced)
	}

	// A total nothing at the head adds up to marks nothing, and says so.
	res, err = pdfsource.Read(strings.Replace(page, "$19.25", "$5.00", 1))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range res.Records {
		if r.Pending {
			t.Errorf("%s marked pending", r.Description)
		}
	}

	if !res.Unplaced || !res.Pending.Known {
		t.Errorf("an unplaced total: %+v", res.Pending)
	}
}

// A section headed "Pending" marks its rows, until the next heading.
func TestAPendingSection(t *testing.T) {
	const statement = `
          Statement period 09/01/2026 to 09/30/2026

          PENDING TRANSACTIONS

          Date         Description                              Amount
          09/29        CITY PARKING HOLD                         -7.25

          POSTED TRANSACTIONS

          Date         Description                              Amount
          09/16        CORNER BAKERY                            -12.00
`

	res, err := pdfsource.Read(statement)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Records) != 2 || !res.Records[0].Pending || res.Records[1].Pending {
		t.Errorf("records: %+v", res.Records)
	}
}
