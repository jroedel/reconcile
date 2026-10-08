package ledgerbus

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/importing/importbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Format is what kind of file a statement was read from.
type Format string

// The formats read so far. PDF is step 9 (docs/plan.md).
const (
	CSV Format = "csv"
	OFX Format = "ofx"
)

// Method is how a statement was checked before it was imported.
type Method string

const (
	// ByBalances: the file printed a running balance on its rows and every
	// one followed from the one before. The strongest check there is: a row
	// missing from the middle of the file breaks the chain where it was.
	ByBalances Method = "balances"

	// ByTotals: the opening balance plus everything in the file is the
	// closing balance, both from the file (OFX) or typed from the paper
	// statement. A missing row is caught, but not where.
	ByTotals Method = "totals"

	// Unchecked: nothing to check against. Imported, and said plainly on
	// every page that shows the statement.
	Unchecked Method = "none"
)

// Statement is one file imported into one account.
type Statement struct {
	ID        types.ID
	AccountID types.ID
	FileID    types.ID
	FileName  string // the file's, for pages; read with the statement
	Format    Format

	Start, End types.Date

	// Opening and Closing are the balances the statement was checked
	// against, when there were any (ByTotals).
	Opening, Closing       money.Amount
	HasOpening, HasClosing bool

	Checked Method

	// Added is how many transactions this statement brought in; Already is
	// how many it listed that an earlier one had.
	Added, Already int

	ImportedBy types.ID
	ImportedAt time.Time
}

// Transaction is one line of an account's history, as a statement reported
// it. Splits, categories and receipts hang off it from step 6.
type Transaction struct {
	ID          types.ID
	AccountID   types.ID
	StatementID types.ID // the statement that brought it in

	PostedOn    types.Date
	Description string
	Amount      money.Amount // money in is positive

	Balance    money.Amount // the running balance the file printed beside it
	HasBalance bool

	// ExternalID is the bank's identifier (OFX's FITID), or empty.
	ExternalID string

	// Hash and Occurrence are the fallback identity (hash).
	Hash       string
	Occurrence int
}

// Month is one month of an account's transactions, summed.
type Month struct {
	Month    string // "2026-07"
	Count    int
	In, Out  money.Amount
	Earliest types.Date
}

// Net is what the month did to the account.
func (m Month) Net() money.Amount { return m.In + m.Out }

// transactions turns what a file listed into rows for one account, with
// their identities.
func transactions(account types.ID, recs []importbus.Record) []Transaction {
	out := make([]Transaction, len(recs))
	seen := make(map[string]int, len(recs))

	for i, r := range recs {
		t := Transaction{
			ID:          types.NewID(),
			AccountID:   account,
			PostedOn:    types.DateOf(r.Date),
			Description: r.Description,
			Amount:      r.Amount,
			Balance:     r.Balance,
			HasBalance:  r.HasBalance,
			ExternalID:  r.ExternalID,
		}

		key := contentKey(t)
		seen[key]++
		t.Occurrence = seen[key]
		t.Hash = hash(t)

		out[i] = t
	}

	return out
}

// hash is the identity of a transaction whose bank gave it none -- every
// CSV row, and the odd OFX row without a FITID. From eumaeus, where it was
// measured.
//
// Content alone cannot tell two $3.50 coffees at one shop on one day apart,
// and would lose one. Position in the file can, but breaks the other way: a
// re-export of the same period shifts every line, and an overlapping
// download doubles everything it covers. Numbering identical rows within one
// file (Occurrence) does both: on five overlapping exports of one card,
// content alone kept 377 of 378 real transactions, line numbers produced
// 392, and this produces 378.
//
// The unit of numbering is one file, which is why transactions is given one
// file's records and nothing else.
func hash(t Transaction) string {
	h := sha256.Sum256([]byte(contentKey(t) + "\x00" + strconv.Itoa(max(t.Occurrence, 1))))

	return hex.EncodeToString(h[:])
}

// contentKey is what makes two rows indistinguishable to a bank export: the
// same account, day, description and amount.
func contentKey(t Transaction) string {
	return t.AccountID.String() + "\x00" + t.PostedOn.String() + "\x00" +
		normalizeDescription(t.Description) + "\x00" + t.Amount.String()
}

// normalizeDescription removes what banks change between exports without
// meaning anything -- case, and runs of spaces -- so the hash survives them.
func normalizeDescription(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
