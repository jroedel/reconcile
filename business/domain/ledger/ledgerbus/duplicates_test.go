package ledgerbus_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// The same money twice, and two coffees on one day (docs/duplicates.md).
// Every statement here is invented.

// csvOf is a statement with no balances, from its rows.
func csvOf(rows ...string) []byte {
	return []byte("Date,Description,Amount\n" + strings.Join(rows, "\n") + "\n")
}

// preview reads a file for an account as the page first does, with the
// rows to import anyway.
func (w *world) preview(actor, account types.ID, name string, data []byte, include ...int) (ledgerbus.Draft, types.ID) {
	w.t.Helper()

	file := w.save(actor, name, data)

	d, err := w.ledger.Prepare(w.t.Context(), actor, account, file, nil)
	if err != nil {
		w.t.Fatalf("Prepare %s: %v", name, err)
	}

	if len(include) > 0 {
		opts := ledgerbus.Options{Mapping: d.Mapping, Opening: d.Opening, Closing: d.Closing, Invert: d.Invert, Import: include}

		if d, err = w.ledger.Prepare(w.t.Context(), actor, account, file, &opts); err != nil {
			w.t.Fatalf("Prepare %s again: %v", name, err)
		}
	}

	return d, file
}

// add imports a file as previewed, and returns the statement.
func (w *world) add(actor, account types.ID, name string, data []byte, include ...int) ledgerbus.Statement {
	w.t.Helper()

	d, file := w.preview(actor, account, name, data, include...)

	w.tick++

	st, err := w.ledger.Import(w.t.Context(), now.Add(time.Duration(w.tick)*time.Minute), actor, account, file,
		ledgerbus.Options{Mapping: d.Mapping, Opening: d.Opening, Closing: d.Closing, Invert: d.Invert, Import: include})
	if err != nil {
		w.t.Fatalf("Import %s: %v (check %+v)", name, err, d.Check)
	}

	return st
}

func counts(st ledgerbus.Statement) [3]int { return [3]int{st.Added, st.Already, st.SetAside()} }

// A morning and an afternoon coffee, alike in every field, are two
// charges; and a wider download of the same days, either way round, adds
// nothing.
func TestTwoCoffeesOnOneDay(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	narrow := csvOf(
		"2026-09-02,SQ *COFFEE CART 0902,-3.50",
		"2026-09-02,SQ *COFFEE CART 0902,-3.50",
		"2026-09-03,CORNER GROCERY,-12.00",
	)
	wide := csvOf(
		"2026-09-01,PARISH OFFERTORY,80.00",
		"2026-09-02,SQ *COFFEE CART 0902,-3.50",
		"2026-09-02,SQ *COFFEE CART 0902,-3.50",
		"2026-09-03,CORNER GROCERY,-12.00",
		"2026-09-04,ELECTRIC CO,-60.00",
	)

	if got := counts(w.add(me, acct, "narrow.csv", narrow)); got != [3]int{3, 0, 0} {
		t.Errorf("narrow: added, already, set aside = %v", got)
	}

	if got := counts(w.add(me, acct, "wide.csv", wide)); got != [3]int{2, 3, 0} {
		t.Errorf("wide after narrow: %v", got)
	}

	// The narrow one again, downloaded another day: the same rows.
	if got := counts(w.add(me, acct, "narrow-again.csv", append(narrow, '\n'))); got != [3]int{0, 3, 0} {
		t.Errorf("narrow again: %v", got)
	}

	// The other way round, in another account.
	other := w.account(me, "checking")

	w.add(me, other, "wide.csv", wide)

	if got := counts(w.add(me, other, "narrow.csv", narrow)); got != [3]int{0, 3, 0} {
		t.Errorf("narrow after wide: %v", got)
	}
}

// A later download with a third coffee adds the third, and only that.
func TestAThirdCoffeeIsAdded(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	coffee := "2026-09-02,SQ *COFFEE CART 0902,-3.50"

	w.add(me, acct, "morning.csv", csvOf(coffee, coffee))

	if got := counts(w.add(me, acct, "evening.csv", csvOf(coffee, coffee, coffee))); got != [3]int{1, 2, 0} {
		t.Errorf("added, already, set aside = %v", got)
	}
}

// Several fees of one amount on one day, in one file, are all kept: rows
// of one file are never counted against each other.
func TestIdenticalRowsOfOneFileAreKept(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	fee := "2026-09-08,RETURNED DEPOSIT FEE,-12.00"

	w.add(me, acct, "one.csv", csvOf(fee))

	if got := counts(w.add(me, acct, "five.csv", csvOf(fee, fee, fee, fee, fee))); got != [3]int{4, 1, 0} {
		t.Errorf("added, already, set aside = %v", got)
	}
}

// One charge worded two ways in two files is set aside, paired with the
// row it is taken for; the wording is remembered, so that a third file
// worded the second way matches without asking; and nothing of another
// day or amount is touched.
func TestOneChargeWordedTwoWays(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.add(me, acct, "export.csv", csvOf(
		"2026-09-02,STARBUCKS 1234,-4.10",
		"2026-09-02,STARBUCKS 1234,-4.10",
		"2026-09-25,INTEREST PAID,0.07",
	))

	statement := csvOf(
		"2026-09-02,STARBUCKS STORE 1234 SEATTLE,-4.10",
		"2026-09-02,STARBUCKS STORE 1234 SEATTLE,-4.10",
		"2026-09-25,INTEREST PAID 8/25 THROUGH 9/24,0.07",
		"2026-09-26,BANK FEE,-5.00",
	)

	d, _ := w.preview(me, acct, "statement.csv", statement)
	if got := counts(d.Statement); got != [3]int{1, 3, 3} {
		t.Fatalf("preview: added, already, set aside = %v", got)
	}

	var pairs []string
	for _, x := range d.Statement.Doubts {
		pairs = append(pairs, x.Row.Description+" = "+x.Twin.Description)
	}

	want := []string{
		"STARBUCKS STORE 1234 SEATTLE = STARBUCKS 1234",
		"STARBUCKS STORE 1234 SEATTLE = STARBUCKS 1234",
		"INTEREST PAID 8/25 THROUGH 9/24 = INTEREST PAID",
	}
	if !slices.Equal(pairs, want) || d.Statement.Doubts[0].Twin.ID == d.Statement.Doubts[1].Twin.ID {
		t.Errorf("pairs = %q (twins %v, %v)", pairs, d.Statement.Doubts[0].Twin.ID, d.Statement.Doubts[1].Twin.ID)
	}

	if got := counts(w.add(me, acct, "statement.csv", statement)); got != [3]int{1, 3, 3} {
		t.Errorf("import: %v", got)
	}

	// The next statement worded the same way: matched by the remembered
	// wording, nothing to ask.
	if got := counts(w.add(me, acct, "statement-again.csv", append(statement, '\n'))); got != [3]int{0, 4, 0} {
		t.Errorf("worded the remembered way: %v", got)
	}

	txs, err := w.ledger.Transactions(t.Context(), me, acct, "2026-09")
	if err != nil || len(txs) != 4 {
		t.Errorf("the month holds %d transactions, want 4 (%v)", len(txs), err)
	}
}

// A third coffee in a file worded the other way: one of the three is new,
// and the other two are taken for the two already there.
func TestAThirdCoffeeWordedTheOtherWay(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.add(me, acct, "export.csv", csvOf("2026-09-02,STARBUCKS 1234,-4.10", "2026-09-02,STARBUCKS 1234,-4.10"))

	row := "2026-09-02,STARBUCKS STORE 1234 SEATTLE,-4.10"

	if got := counts(w.add(me, acct, "statement.csv", csvOf(row, row, row))); got != [3]int{1, 2, 2} {
		t.Errorf("added, already, set aside = %v", got)
	}
}

// A row set aside that the person says is another charge is imported, and
// is still listed, as imported.
func TestImportingOneAnyway(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.add(me, acct, "export.csv", csvOf("2026-09-02,STARBUCKS 1234,-4.10"))

	data := csvOf("2026-09-01,PARISH OFFERTORY,80.00", "2026-09-02,HILLTOP BAKERY,-4.10")

	d, _ := w.preview(me, acct, "statement.csv", data)
	if len(d.Statement.Doubts) != 1 || d.Statement.Doubts[0].Index != 1 {
		t.Fatalf("doubts = %+v", d.Statement.Doubts)
	}

	st := w.add(me, acct, "statement.csv", data, 1)
	if got := counts(st); got != [3]int{2, 0, 0} || len(st.Doubts) != 1 || !st.Doubts[0].Imported {
		t.Errorf("counts %v, doubts %+v", got, st.Doubts)
	}
}

// Every statement fixture imports the same the second time it is
// downloaded, and with its rows the other way up: nothing added, nothing
// set aside. The preview's own second pass would refuse it otherwise; this
// is the property said for every reader at once.
func TestEveryFixtureIsIdempotent(t *testing.T) {
	files, err := filepath.Glob("testdata/*")
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v", err)
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			w := newWorld(t)
			me := w.user("treasurer@example.org")

			kind := "checking"
			if strings.HasPrefix(filepath.Base(path), "card") {
				kind = "card"
			}

			acct := w.account(me, kind)

			d, _ := w.preview(me, acct, filepath.Base(path), data)
			if !d.Ready() {
				t.Skipf("does not import as it is (check %+v)", d.Check)
			}

			first := w.add(me, acct, filepath.Base(path), data)

			again := w.add(me, acct, "again-"+filepath.Base(path), append(slices.Clone(data), '\n'))
			if again.Added != 0 || again.SetAside() != 0 || again.Already != first.Added {
				t.Errorf("downloaded again: added %d, set aside %d, already %d of %d", again.Added, again.SetAside(), again.Already, first.Added)
			}

			if !strings.HasSuffix(path, ".csv") {
				return
			}

			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			slices.Reverse(lines[1:])

			upside := w.add(me, acct, "reversed-"+filepath.Base(path), []byte(strings.Join(lines, "\n")+"\n"))
			if upside.Added != 0 || upside.SetAside() != 0 {
				t.Errorf("the other way up: added %d, set aside %d", upside.Added, upside.SetAside())
			}
		})
	}
}

// An OFX download after a CSV worded otherwise: its rows set aside give
// their bank identifiers to the rows they are taken for, so that the next
// OFX matches them exactly.
func TestAnOFXAfterACSVWordedOtherwise(t *testing.T) {
	w := newWorld(t)
	me := w.user("treasurer@example.org")
	acct := w.account(me, "checking")

	w.add(me, acct, "export.csv", csvOf(
		"2026-07-03,CORNER GROCERY STORE 12,-33.99",
		"2026-07-03,COFFEE CART #12,-3.50",
		"2026-07-03,COFFEE CART #12,-3.50",
	))

	ofx, err := os.ReadFile("testdata/checking-july.ofx")
	if err != nil {
		t.Fatal(err)
	}

	first := w.add(me, acct, "july.ofx", ofx)
	if first.SetAside() != 3 {
		t.Fatalf("set aside %d, want the three of 3 July", first.SetAside())
	}

	day, _ := types.ParseDate("2026-07-03")

	txs, err := w.ledger.Between(t.Context(), me, acct, day, day)
	if err != nil || len(txs) != 3 {
		t.Fatalf("3 July holds %d transactions, want the CSV's 3 (%v)", len(txs), err)
	}

	for _, tx := range txs {
		if tx.ExternalID == "" {
			t.Errorf("%s was given no bank identifier", tx.Description)
		}
	}

	if again := w.add(me, acct, "july-again.ofx", append(slices.Clone(ofx), '\n')); again.Added != 0 || again.SetAside() != 0 {
		t.Errorf("the OFX again: added %d, set aside %d", again.Added, again.SetAside())
	}
}
