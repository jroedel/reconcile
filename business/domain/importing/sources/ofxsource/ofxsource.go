// Package ofxsource reads an OFX or QFX statement download. Adapted from
// eumaeus.
//
// Prefer this over CSV wherever a bank offers both, and the reason is one
// field: OFX carries FITID, an identifier the institution assigns to each
// transaction and keeps stable across downloads. That becomes the ledger's
// ExternalID and lets deduplication be exact.
//
// A CSV export has no such field, so the ledger falls back to hashing content
// and numbering identical rows within one file (ledgerbus). OFX does not
// need to.
//
// The format buys smaller things too: dates and amounts are typed rather than
// guessed at, there is no column mapping, and the file states its period and
// its closing balance.
//
// The account number inside the file is read and thrown away. This app keeps
// the last four digits an owner chooses to type, and never the number.
package ofxsource

import (
	"strings"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types/money"
)

// Read parses an OFX document into records.
func Read(data []byte) (importbus.Result, error) {
	statements, err := parse(strings.NewReader(string(data)))
	if err != nil {
		return importbus.Result{}, err
	}

	var res importbus.Result

	// A download usually holds one statement, but a bank that exports
	// several accounts at once produces more, and they would all land in the
	// one account it is being uploaded to.
	if len(statements) > 1 {
		res.Warn(importbus.Warning{Problem: importbus.SeveralStatements})
	}

	for _, st := range statements {
		// The first statement to state a balance wins: a later account's
		// balance would be a wrong number rather than a missing one.
		if !res.Closing.Known && st.LedgerBalance != "" {
			if balance, ok := readBalance(st); ok {
				res.Closing = balance
			} else {
				res.Warn(importbus.Warning{Problem: importbus.BadLedgerBalance, Value: st.LedgerBalance})
			}
		}

		if res.Start.IsZero() && st.Start != "" {
			res.Start, _ = parseDate(st.Start)
		}

		if res.End.IsZero() && st.End != "" {
			res.End, _ = parseDate(st.End)
		}

		for _, t := range st.Transactions {
			rec, warning := toRecord(t)
			if warning != nil && warning.Problem != importbus.NoFITID {
				res.Skipped++
				res.Warn(*warning)

				continue
			}

			// No FITID is not a reason to lose the transaction: the ledger
			// falls back to the content hash, as for CSV. It is worth
			// saying, because it removes the reason to prefer this format.
			if warning != nil {
				res.Warn(*warning)
			}

			res.Records = append(res.Records, rec)
		}
	}

	return res, nil
}

// toRecord converts one OFX transaction.
func toRecord(t ofxTransaction) (importbus.Record, *importbus.Warning) {
	date, err := parseDate(t.Posted)
	if err != nil {
		return importbus.Record{}, &importbus.Warning{Problem: importbus.BadDate, Value: t.Posted}
	}

	amount, err := money.Parse(t.Amount)
	if err != nil {
		return importbus.Record{}, &importbus.Warning{Problem: importbus.BadAmount, Value: t.Amount}
	}

	rec := importbus.Record{
		Date:        date,
		Description: describe(t),
		Amount:      amount,
		ExternalID:  strings.TrimSpace(t.FITID),
	}

	if rec.ExternalID == "" {
		return rec, &importbus.Warning{Problem: importbus.NoFITID, Value: date.Format("2006-01-02")}
	}

	return rec, nil
}

// Looks reports whether the bytes look like an OFX document. Banks ship OFX
// under .ofx, .qfx and occasionally .txt, so the content decides, not the
// name.
func Looks(head []byte) bool {
	upper := strings.ToUpper(string(head))

	return strings.Contains(upper, "<OFX>") || strings.Contains(upper, "OFXHEADER")
}

// readBalance reads the ledger balance. One with no date is not usable: a
// balance is what an account held at a moment, and a figure that cannot say
// which moment cannot be checked against anything.
func readBalance(st statement) (importbus.Balance, bool) {
	amount, err := money.Parse(st.LedgerBalance)
	if err != nil || st.BalanceAsOf == "" {
		return importbus.Balance{}, false
	}

	asOf, err := parseDate(st.BalanceAsOf)
	if err != nil {
		return importbus.Balance{}, false
	}

	return importbus.Balance{Amount: amount, AsOf: asOf, Known: true}, true
}
