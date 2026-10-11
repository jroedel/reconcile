package ledgerdb

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Statements split by holder (docs/clearing.md, 3).

// initHolders creates the table of accounts whose statements arrive one
// file per holder: a row is the option on. The ledger's own table
// rather than a column of accounts, because turning it on or off and
// working out every row's identity again must be one transaction, and the
// rows are the ledger's.
func initHolders(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS holder_accounts (
    account_id TEXT    PRIMARY KEY REFERENCES accounts (id),
    set_by     TEXT    NOT NULL,
    set_at     INTEGER NOT NULL
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the holder accounts table: %w", err)
	}

	return nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func byHolder(ctx context.Context, q querier, account types.ID) (bool, error) {
	var one int

	err := q.QueryRowContext(ctx, `SELECT 1 FROM holder_accounts WHERE account_id = ?`, account.String()).Scan(&one)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading the account's holder option: %w", err)
	}

	return true, nil
}

// ByHolder reports whether the account's option is on.
func (s *Store) ByHolder(ctx context.Context, account types.ID) (bool, error) {
	return byHolder(ctx, s.db, account)
}

// SetByHolder turns the option on or off and gives every row of the
// account its identity under the new setting, in one transaction, with ev.
//
// The identity is computed in Go (ledgerbus.Hash), so the rows are read and
// written back here rather than updated by one statement; eumaeus did the
// same when its hash changed. Each statement's rows are numbered again
// among the rows alike under the new setting, in the order they had: two
// holders' identical coffees in one file were coffee 1 and coffee 2 to
// an account that ignored holders, and are each one's coffee 1 to an
// account that does not.
func (s *Store) SetByHolder(ctx context.Context, account types.ID, on bool, by types.ID, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to change the holder option: %w", err)
	}
	defer tx.Rollback()

	was, err := byHolder(ctx, tx, account)
	if err != nil {
		return err
	}

	if was == on {
		return nil
	}

	if on {
		_, err = tx.ExecContext(ctx, `INSERT INTO holder_accounts (account_id, set_by, set_at) VALUES (?, ?, ?)`,
			account.String(), by.String(), ev.At.UnixMilli())
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM holder_accounts WHERE account_id = ?`, account.String())
	}

	if err != nil {
		return fmt.Errorf("changing the holder option: %w", err)
	}

	if err := rehash(ctx, tx, account, on); err != nil {
		return err
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("changing the holder option: %w", err)
	}

	return nil
}

// rehash gives every row of the account without a bank identifier its
// occurrence and hash under the setting.
func rehash(ctx context.Context, tx *sql.Tx, account types.ID, on bool) error {
	rows, err := tx.QueryContext(ctx, `
SELECT rowid, statement_id, posted_on, description, amount, holder, occurrence FROM transactions
WHERE account_id = ? ORDER BY statement_id, occurrence, rowid`, account.String())
	if err != nil {
		return fmt.Errorf("reading the account's transactions: %w", err)
	}

	type row struct {
		rowid     int64
		statement string
		t         ledgerbus.Transaction
	}

	var all []row

	for rows.Next() {
		var (
			r      row
			posted string
			amount int64
		)

		if err := rows.Scan(&r.rowid, &r.statement, &posted, &r.t.Description, &amount, &r.t.Holder, &r.t.Occurrence); err != nil {
			rows.Close()

			return fmt.Errorf("reading the account's transactions: %w", err)
		}

		if r.t.PostedOn, err = types.ParseDate(posted); err != nil {
			rows.Close()

			return fmt.Errorf("a stored transaction is unreadable: %w", err)
		}

		r.t.AccountID, r.t.Amount = account, money.Amount(amount)
		all = append(all, r)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading the account's transactions: %w", err)
	}

	slices.SortStableFunc(all, func(a, b row) int { return cmp.Compare(a.statement, b.statement) })

	seen := map[string]int{}
	statement := ""

	for _, r := range all {
		if r.statement != statement {
			statement = r.statement
			clear(seen)
		}

		key := ledgerbus.ContentKey(r.t, on)
		seen[key]++
		r.t.Occurrence = seen[key]

		if _, err := tx.ExecContext(ctx, `UPDATE transactions SET occurrence = ?, hash = ? WHERE rowid = ?`,
			r.t.Occurrence, ledgerbus.Hash(r.t, on), r.rowid); err != nil {
			return fmt.Errorf("giving a transaction its identity: %w", err)
		}
	}

	return nil
}

// Holders is the holders the account's rows have named, by name.
func (s *Store) Holders(ctx context.Context, account types.ID) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT holder FROM transactions WHERE account_id = ? AND holder <> '' ORDER BY holder`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the holders: %w", err)
	}
	defer rows.Close()

	var out []string

	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("reading the holders: %w", err)
		}

		out = append(out, h)
	}

	return out, rows.Err()
}

// HolderTotals is what each holder's rows come to from start up to,
// not including, end, by name; rows that name nobody under "".
func (s *Store) HolderTotals(ctx context.Context, account types.ID, start, end types.Date) ([]ledgerbus.HolderTotal, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT holder, count(*), sum(amount) FROM transactions
WHERE account_id = ? AND posted_on >= ? AND posted_on < ?
GROUP BY holder ORDER BY holder`, account.String(), start.String(), end.String())
	if err != nil {
		return nil, fmt.Errorf("adding up the holders: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.HolderTotal

	for rows.Next() {
		var (
			h   ledgerbus.HolderTotal
			sum int64
		)

		if err := rows.Scan(&h.Holder, &h.Count, &sum); err != nil {
			return nil, fmt.Errorf("adding up the holders: %w", err)
		}

		h.Sum = money.Amount(sum)
		out = append(out, h)
	}

	return out, rows.Err()
}

// HoldersBetween is the holders the account's rows dated from one day to
// another, both included, have named, by name.
func (s *Store) HoldersBetween(ctx context.Context, account types.ID, from, to types.Date) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT holder FROM transactions
WHERE account_id = ? AND holder <> '' AND posted_on BETWEEN ? AND ?
ORDER BY holder`, account.String(), from.String(), to.String())
	if err != nil {
		return nil, fmt.Errorf("reading the holders: %w", err)
	}
	defer rows.Close()

	var out []string

	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("reading the holders: %w", err)
		}

		out = append(out, h)
	}

	return out, rows.Err()
}

// NamedInParts is the account's transactions that name a holder and have a
// part with a memo, with their parts, oldest first.
func (s *Store) NamedInParts(ctx context.Context, account types.ID) ([]ledgerbus.Transaction, error) {
	return s.withSplits(ctx, `account_id = ? AND holder <> '' AND id IN (SELECT transaction_id FROM splits WHERE memo <> '')`,
		"posted_on, rowid", account.String())
}
