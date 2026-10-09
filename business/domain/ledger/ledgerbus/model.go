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

// The formats read.
const (
	CSV Format = "csv"
	OFX Format = "ofx"
	PDF Format = "pdf"
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

	// BySum: the rows add up to the total the document states, and it
	// states no balance. A card's printed activity is this: a missing row
	// is caught, but not where, and there is no closing balance to
	// reconcile against.
	BySum Method = "sum"

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
	// against: stated or typed (ByTotals), or before its first row and
	// after its last (ByBalances; statements imported before reconciling
	// arrived have none).
	Opening, Closing       money.Amount
	HasOpening, HasClosing bool

	Checked Method

	// Added is how many transactions this statement brought in; Already is
	// how many it listed that an earlier one had.
	Added, Already int

	ImportedBy types.ID
	ImportedAt time.Time

	// Reconciliation is its reconciliation, if it has been reconciled
	// (reconcile.go).
	Reconciliation Reconciliation

	// Locked is how many of the transactions importing it would add fall
	// in a period already reconciled. Counted with Added, and refused.
	Locked int

	// ByRule is how many of the transactions it brought in a sorting rule
	// sorted. Counted on import and in the preview; not kept.
	ByRule int

	// Doubts is the rows the count rule set aside as probably here already
	// (Doubt), with any a person imported all the same. Those set aside are
	// counted in Already. Counted on import and in the preview; not kept.
	Doubts []Doubt
}

// SetAside is how many of its rows the count rule left out.
func (s Statement) SetAside() int {
	n := 0

	for _, d := range s.Doubts {
		if !d.Imported {
			n++
		}
	}

	return n
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

	// Splits are its parts, at least one, adding up to Amount. Filled in
	// where a page needs them.
	Splits []Split

	// Insist is set, on a row of a file being imported, when a person chose
	// to import it although the count rule set it aside (Doubt). Never
	// stored.
	Insist bool
}

// Doubt is a row of a file being imported that the count rule took for one
// the account already has under another wording (docs/duplicates.md): on a
// day the file and earlier statements both cover, importing every new row
// of its amount would leave more charges of that amount than either lists.
type Doubt struct {
	// Index is the row's place among the file's rows, which Options.Import
	// names to import it anyway.
	Index int

	// Row is the row as the file has it; Twin the stored one it was taken
	// for.
	Row, Twin Transaction

	// Imported is whether it was imported all the same (Transaction.Insist).
	Imported bool
}

// Sorted reports whether every part has a category.
func (t Transaction) Sorted() bool {
	for _, s := range t.Splits {
		if s.CategoryID.Zero() {
			return false
		}
	}

	return len(t.Splits) > 0
}

// Split is one part of a transaction: how much of it went where.
//
// Every transaction has at least one, made with it for its whole amount, so
// that "what is this money for" has one answer however many parts there
// are, and a project's book is the sum of its splits and nothing else.
type Split struct {
	ID            types.ID
	TransactionID types.ID
	Position      int
	Amount        money.Amount
	CategoryID    types.ID // zero: not sorted yet
	ProjectID     types.ID // zero: in no project
	Memo          string

	// RuleID is the sorting rule that sorted it, zero for a part a person
	// sorted or nobody has (docs/sorting.md). Saving the transaction's
	// parts clears it: then a person has said.
	RuleID types.ID
}

// ByRule reports whether a rule sorted any of the transaction's parts.
func (t Transaction) ByRule() bool {
	for _, s := range t.Splits {
		if !s.RuleID.Zero() {
			return true
		}
	}

	return false
}

// Month is one month of an account's transactions, summed.
type Month struct {
	Month    string // "2026-07"
	Count    int
	In, Out  money.Amount
	Earliest types.Date

	// Unsorted is how many of its transactions have a part with no
	// category.
	Unsorted int

	// ByRule is how many of them a sorting rule sorted that nobody has
	// saved since: the ones to check.
	ByRule int

	// Operations is the month's parts by kind, beside the cash.
	Operations Operations
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
		t.Splits = []Split{{ID: types.NewID(), TransactionID: t.ID, Amount: t.Amount, Memo: r.Memo}}

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

// Truncated reports whether one description is the other cut short: it
// ends in an ellipsis, and what is before the ellipsis is how the other
// begins, ignoring case and spacing. A bank's web page prints "Hilltop
// Clinic…" where its statement and its CSV say "HILLTOP CLINIC 0412
// SPRINGFIELD", and the two are one charge (docs/pdf-statements.md, "Not
// counting a charge twice"). Two descriptions neither of which is cut
// short are never the same this way: "SHELL" and "SHELL OIL 123" may be
// two shops.
func Truncated(a, b string) bool {
	cut := func(s string) (string, bool) {
		s = normalizeDescription(s)

		for _, e := range []string{"…", "..."} {
			if rest, ok := strings.CutSuffix(s, e); ok {
				return strings.TrimSpace(rest), true
			}
		}

		return s, false
	}

	pa, ca := cut(a)
	pb, cb := cut(b)

	switch {
	case !ca && !cb:
		return false
	case ca && len(pa) >= 3 && strings.HasPrefix(pb, pa):
		return true
	case cb && len(pb) >= 3 && strings.HasPrefix(pa, pb):
		return true
	}

	return false
}
