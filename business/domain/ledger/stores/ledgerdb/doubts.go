package ledgerdb

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// The count rule, and what it remembers (docs/duplicates.md).
//
// Every rule in already is exact, and the description is part of the key:
// that is what keeps a morning and an afternoon coffee, identical in every
// field, as two charges. Its price is that one charge a bank words two ways
// -- in its CSV and its PDF, pending and posted -- is two rows that nothing
// exact can join. eumaeus met it as interest counted twice every month.
//
// The count rule needs no description. On a day the file and an earlier
// statement both list, the account should end with as many charges of an
// amount as the file lists or as it had, whichever is more -- never the
// sum. Rows of one file are never counted against each other, which is
// what keeps the coffees; rows the exact rules found are not new, which is
// what lets a wider download add a third coffee and only the third.

// initAliases creates the table of other names a stored transaction is known
// by: the content hash of a wording the count rule set aside as the same
// charge. A name goes with its transaction (ON DELETE CASCADE).
func initAliases(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS transaction_aliases (
    account_id     TEXT    NOT NULL REFERENCES accounts (id),
    hash           TEXT    NOT NULL,
    transaction_id TEXT    NOT NULL REFERENCES transactions (id) ON DELETE CASCADE,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (account_id, hash)
) STRICT;

CREATE INDEX IF NOT EXISTS transaction_aliases_transaction ON transaction_aliases (transaction_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the transaction aliases table: %w", err)
	}

	return nil
}

// countRule says which of the file's new rows (fresh, by their place in
// txs) are probably rows of other statements worded otherwise.
//
// For each day and amount: F rows in the file, S already stored from other
// statements, N of the file's rows new by every exact rule. Importing all N
// leaves S + N where max(F, S) is right, so the difference is set aside.
// Which of the N are set aside, when not all are, is the ones whose
// descriptions are likest a stored row's, and each is paired with the
// stored row it is likest, each stored row once.
func countRule(ctx context.Context, tx *sql.Tx, statement types.ID, txs []ledgerbus.Transaction, fresh []int) ([]ledgerbus.Doubt, error) {
	type key struct {
		day    string
		amount money.Amount
	}

	listed := map[key]int{}
	for _, t := range txs {
		listed[key{t.PostedOn.String(), t.Amount}]++
	}

	var order []key

	groups := map[key][]int{}

	for _, i := range fresh {
		k := key{txs[i].PostedOn.String(), txs[i].Amount}
		if groups[k] == nil {
			order = append(order, k)
		}

		groups[k] = append(groups[k], i)
	}

	var doubts []ledgerbus.Doubt

	for _, k := range order {
		group := groups[k]

		twins, err := stored(ctx, tx, txs[group[0]].AccountID, k.day, k.amount, statement)
		if err != nil {
			return nil, err
		}

		excess := len(group) - max(listed[k]-len(twins), 0)
		if len(twins) == 0 || excess <= 0 {
			continue
		}

		// The rows likest a stored one are the ones taken for it.
		best := func(i int) float64 {
			b := 0.0
			for _, tw := range twins {
				b = max(b, alike(txs[i].Description, tw.Description))
			}

			return b
		}

		chosen := slices.Clone(group)
		slices.SortStableFunc(chosen, func(a, b int) int { return cmp.Compare(best(b), best(a)) })
		chosen = chosen[:excess]
		slices.Sort(chosen)

		used := make([]bool, len(twins))

		for _, i := range chosen {
			pick := -1

			for j, tw := range twins {
				if !used[j] && (pick < 0 || alike(txs[i].Description, tw.Description) > alike(txs[i].Description, twins[pick].Description)) {
					pick = j
				}
			}

			used[pick] = true
			doubts = append(doubts, ledgerbus.Doubt{Index: i, Row: txs[i], Twin: twins[pick], Imported: txs[i].Insist})
		}
	}

	slices.SortFunc(doubts, func(a, b ledgerbus.Doubt) int { return cmp.Compare(a.Index, b.Index) })

	return doubts, nil
}

// stored is an account's rows of one day and amount from statements other
// than this one, oldest first.
func stored(ctx context.Context, tx *sql.Tx, account types.ID, day string, amount money.Amount, statement types.ID) ([]ledgerbus.Transaction, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, statement_id, description, external_id FROM transactions
WHERE account_id = ? AND posted_on = ? AND amount = ? AND statement_id <> ?
ORDER BY rowid`, account.String(), day, int64(amount), statement.String())
	if err != nil {
		return nil, fmt.Errorf("counting a day's transactions: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Transaction

	for rows.Next() {
		var id, st string

		t := ledgerbus.Transaction{AccountID: account, Amount: amount}

		if err := rows.Scan(&id, &st, &t.Description, &t.ExternalID); err != nil {
			return nil, fmt.Errorf("counting a day's transactions: %w", err)
		}

		if t.ID, err = types.ParseID(id); err != nil {
			return nil, err
		}

		if t.StatementID, err = types.ParseID(st); err != nil {
			return nil, err
		}

		if t.PostedOn, err = types.ParseDate(day); err != nil {
			return nil, err
		}

		out = append(out, t)
	}

	return out, rows.Err()
}

// alike is how alike two descriptions are, for choosing among rows the
// count rule has already decided about: the same wording most, then one
// inside the other (eumaeus' containment), then the length of what they
// begin with in common. It decides which row is which, never whether.
func alike(a, b string) float64 {
	a, b = plain(a), plain(b)

	switch {
	case a == b:
		return 3
	case a == "" || b == "":
		return 0
	case strings.Contains(a, b) || strings.Contains(b, a):
		return 2 + float64(min(len(a), len(b)))/float64(max(len(a), len(b)))
	}

	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}

	return float64(n) / float64(max(len(a), len(b)))
}

// plain is a description without what does not tell two apart: case, runs
// of spaces, and a printed page's ellipsis.
func plain(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))

	for _, e := range []string{"…", "..."} {
		s = strings.TrimSpace(strings.TrimSuffix(s, e))
	}

	return s
}

// remember keeps a row set aside as another name for the stored row it was
// taken for, and gives that row the bank's identifier if the set-aside row
// had one and it has none.
func remember(ctx context.Context, tx *sql.Tx, row, twin ledgerbus.Transaction, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO transaction_aliases (account_id, hash, transaction_id, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (account_id, hash) DO NOTHING`,
		row.AccountID.String(), row.Hash, twin.ID.String(), at.UnixMilli()); err != nil {
		return fmt.Errorf("remembering a wording: %w", err)
	}

	if row.ExternalID == "" || twin.ExternalID != "" {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `UPDATE transactions SET external_id = ? WHERE id = ? AND external_id = ''`,
		row.ExternalID, twin.ID.String()); err != nil {
		return fmt.Errorf("matching a transaction: %w", err)
	}

	return nil
}

// secondPass is the statement and rows of the preview's second import:
// the same rows under new identifiers, as a second upload of the same file
// would make them. A variable so that a test can make it unstable and see
// the tripwire fire.
var secondPass = func(st ledgerbus.Statement, txs []ledgerbus.Transaction) (ledgerbus.Statement, []ledgerbus.Transaction) {
	st.ID = types.NewID()

	out := make([]ledgerbus.Transaction, len(txs))

	for i, t := range txs {
		t.ID, t.StatementID = types.NewID(), st.ID

		splits := make([]ledgerbus.Split, len(t.Splits))
		for j, sp := range t.Splits {
			sp.ID, sp.TransactionID = types.NewID(), t.ID
			splits[j] = sp
		}

		t.Splits = splits
		out[i] = t
	}

	return st, out
}
