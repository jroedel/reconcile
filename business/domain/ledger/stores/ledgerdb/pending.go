package ledgerdb

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// Pending charges (docs/clearing.md, 4; ledgerbus/pending.go).

// post finds, among a file's new rows, the posted form of pending charges
// the account already holds, and gives each pending charge its posted
// date, amount and description in place: the rows that did so are new to
// nothing after, and what is left of fresh is returned.
//
// A pending charge is taken by the first new row, in the file's order,
// that is not pending, is dated on its day or up to PostWithin after, is
// the same holder's, whose description is its own or contains it, or one
// is the other cut short (alike, from eumaeus' containment), and whose
// amount it may have become (ledgerbus.PostsAs: any, the same way round,
// for the same wording; within a third, or PostSlack, for wording only
// alike). Of several, the likest wording, then the nearest day. Each
// pending charge is taken once, and none in a reconciled period, which
// nothing changes.
//
// The same holder whatever the account's option (issue #81): with it off,
// another holder's posted charge at the same shop took a pending charge's
// place, and the first holder's charge was gone from their month. Two
// rows that both name a holder, and not the same one, are two people's.
func post(ctx context.Context, tx *sql.Tx, st *ledgerbus.Statement, txs []ledgerbus.Transaction, fresh []int) ([]int, error) {
	taken := map[types.ID]bool{}
	left := fresh[:0:0]

	for _, i := range fresh {
		t := txs[i]
		if t.Pending {
			left = append(left, i)

			continue
		}

		candidates, err := pendingBetween(ctx, tx, t.AccountID, t.PostedOn.AddDays(-ledgerbus.PostWithin), t.PostedOn, st.ID)
		if err != nil {
			return nil, err
		}

		candidates = slices.DeleteFunc(candidates, func(p ledgerbus.Transaction) bool {
			like := alike(p.Description, t.Description)

			return taken[p.ID] || !ledgerbus.SameHolder(p.Holder, t.Holder) ||
				like < 2 && !ledgerbus.Truncated(p.Description, t.Description) ||
				!ledgerbus.PostsAs(p.Amount, t.Amount, like == 3)
		})

		if len(candidates) == 0 {
			left = append(left, i)

			continue
		}

		slices.SortStableFunc(candidates, func(a, b ledgerbus.Transaction) int {
			return cmp.Or(cmp.Compare(alike(b.Description, t.Description), alike(a.Description, t.Description)),
				cmp.Compare(b.PostedOn.String(), a.PostedOn.String()))
		})

		p := candidates[0]
		taken[p.ID] = true

		if err := replace(ctx, tx, *st, p, t); err != nil {
			return nil, err
		}

		st.Posted++
	}

	return left, nil
}

// settle clears the pending mark of the stored charge a posted row of the
// file is already (ledgerdb.already): by its identity, a remembered
// wording, or one of the two cut short -- the same holder's, or one that
// names nobody, as already matches. Nothing else about it changes, so the
// history has nothing to say.
func settle(ctx context.Context, tx *sql.Tx, st *ledgerbus.Statement, t ledgerbus.Transaction) error {
	rows, err := tx.QueryContext(ctx, `
SELECT rowid, holder FROM transactions WHERE account_id = ? AND pending = 1 AND (hash = ?
    OR external_id <> '' AND external_id = ?
    OR id IN (SELECT transaction_id FROM transaction_aliases WHERE account_id = ? AND hash = ?))
ORDER BY rowid`,
		t.AccountID.String(), t.Hash, t.ExternalID, t.AccountID.String(), t.Hash)
	if err != nil {
		return fmt.Errorf("posting a pending charge: %w", err)
	}

	var match []int64

	for rows.Next() {
		var (
			id     int64
			holder string
		)

		if err := rows.Scan(&id, &holder); err != nil {
			rows.Close()

			return fmt.Errorf("posting a pending charge: %w", err)
		}

		if ledgerbus.SameHolder(holder, t.Holder) {
			match = append(match, id)
		}
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("posting a pending charge: %w", err)
	}

	var n int64

	if len(match) > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE transactions SET pending = 0 WHERE rowid = ?`, match[0])
		if err != nil {
			return fmt.Errorf("posting a pending charge: %w", err)
		}

		if n, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("posting a pending charge: %w", err)
		}
	}

	if n == 0 {
		short, err := cutShort(ctx, tx, t, false)
		if err != nil {
			return err
		}

		for _, id := range short {
			res, err := tx.ExecContext(ctx, `UPDATE transactions SET pending = 0 WHERE rowid = ? AND pending = 1`, id)
			if err != nil {
				return fmt.Errorf("posting a pending charge: %w", err)
			}

			if n, err = res.RowsAffected(); err != nil {
				return fmt.Errorf("posting a pending charge: %w", err)
			}

			if n == 1 {
				break
			}
		}
	}

	if n == 1 {
		st.Posted++
	}

	return nil
}

// pendingBetween is the account's pending charges from other statements
// dated from one day to another, outside every reconciled period.
func pendingBetween(ctx context.Context, tx *sql.Tx, account types.ID, from, to types.Date, statement types.ID) ([]ledgerbus.Transaction, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, posted_on, description, amount, holder FROM transactions t
WHERE t.account_id = ? AND t.pending = 1 AND t.statement_id <> ? AND t.posted_on BETWEEN ? AND ? AND NOT `+lockedWhere+`
ORDER BY t.posted_on, t.rowid`, account.String(), statement.String(), from.String(), to.String())
	if err != nil {
		return nil, fmt.Errorf("looking for pending charges: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Transaction

	for rows.Next() {
		var (
			id, posted string
			amount     int64
			p          = ledgerbus.Transaction{AccountID: account, Pending: true}
		)

		if err := rows.Scan(&id, &posted, &p.Description, &amount, &p.Holder); err != nil {
			return nil, fmt.Errorf("looking for pending charges: %w", err)
		}

		var e [2]error
		p.ID, e[0] = types.ParseID(id)
		p.PostedOn, e[1] = types.ParseDate(posted)

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a stored transaction is unreadable: %w", err)
		}

		p.Amount = money.Amount(amount)
		out = append(out, p)
	}

	return out, rows.Err()
}

// replace gives a pending charge its posted form, and its parts the posted
// amount. One part follows the amount. Of several, the last takes the
// difference -- the tip on a restaurant's charge is the last thing added
// to it -- unless that would leave it nothing or turn it round, when the
// charge goes back to one part, unsorted, for a person to split again.
// Either way the history says so.
func replace(ctx context.Context, tx *sql.Tx, st ledgerbus.Statement, p, t ledgerbus.Transaction) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE transactions SET statement_id = ?, posted_on = ?, description = ?, amount = ?, balance = ?, external_id = ?,
    hash = ?, occurrence = ?, holder = ?, pending = 0, check_number = ?
WHERE id = ?`,
		st.ID.String(), t.PostedOn.String(), t.Description, int64(t.Amount), nullAmount(t.Balance, t.HasBalance),
		t.ExternalID, t.Hash, t.Occurrence, cmp.Or(t.Holder, p.Holder), cmp.Or(t.CheckNumber, p.CheckNumber), p.ID.String()); err != nil {
		return fmt.Errorf("posting a pending charge: %w", err)
	}

	type part struct {
		id     string
		amount money.Amount
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, amount FROM splits WHERE transaction_id = ? ORDER BY position`, p.ID.String())
	if err != nil {
		return fmt.Errorf("reading a pending charge's parts: %w", err)
	}

	var parts []part

	for rows.Next() {
		var (
			pt part
			a  int64
		)

		if err := rows.Scan(&pt.id, &a); err != nil {
			rows.Close()

			return fmt.Errorf("reading a pending charge's parts: %w", err)
		}

		pt.amount = money.Amount(a)
		parts = append(parts, pt)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading a pending charge's parts: %w", err)
	}

	resplit := "0"

	switch last := len(parts) - 1; {
	case len(parts) == 1:
		_, err = tx.ExecContext(ctx, `UPDATE splits SET amount = ? WHERE id = ?`, int64(t.Amount), parts[0].id)
	case len(parts) > 1 && sameSign(parts[last].amount+t.Amount-p.Amount, t.Amount):
		_, err = tx.ExecContext(ctx, `UPDATE splits SET amount = ? WHERE id = ?`, int64(parts[last].amount+t.Amount-p.Amount), parts[last].id)
	default:
		resplit = "1"

		if _, err = tx.ExecContext(ctx, `DELETE FROM splits WHERE transaction_id = ?`, p.ID.String()); err == nil {
			err = insertSplits(ctx, tx, []ledgerbus.Split{{ID: types.NewID(), TransactionID: p.ID, Amount: t.Amount}})
		}
	}

	if err != nil {
		return fmt.Errorf("giving a posted charge its parts: %w", err)
	}

	var currency string
	if err := tx.QueryRowContext(ctx, `SELECT currency FROM accounts WHERE id = ?`, st.AccountID.String()).Scan(&currency); err != nil {
		return fmt.Errorf("reading the account's currency: %w", err)
	}

	return eventdb.Insert(ctx, tx, eventbus.New(st.ImportedAt, st.ImportedBy, types.AccountScope(st.AccountID), ledgerbus.TransactionPosted, map[string]string{
		"description": t.Description,
		"date":        t.PostedOn.String(),
		"pending":     p.Amount.String(),
		"posted":      t.Amount.String(),
		"currency":    currency,
		"parts":       strconv.Itoa(len(parts)),
		"resplit":     resplit,
	}))
}

// sameSign reports a part that is neither nothing nor turned round from
// the whole.
func sameSign(part, whole money.Amount) bool {
	return part != 0 && (part < 0) == (whole < 0)
}

// StillPending is the account's pending charges dated more than within
// before the end of its latest statement -- not an entry by hand, which
// says nothing of what the bank has posted.
func (s *Store) StillPending(ctx context.Context, account types.ID, within int) ([]ledgerbus.Transaction, error) {
	var latest sql.NullString

	if err := s.db.QueryRowContext(ctx, `SELECT max(period_end) FROM statements WHERE account_id = ? AND format <> ?`,
		account.String(), string(ledgerbus.Hand)).Scan(&latest); err != nil {
		return nil, fmt.Errorf("reading the latest statement: %w", err)
	}

	if !latest.Valid {
		return nil, nil
	}

	end, err := types.ParseDate(latest.String)
	if err != nil {
		return nil, fmt.Errorf("a stored statement is unreadable: %w", err)
	}

	before := end.AddDays(-within)

	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE account_id = ? AND pending = 1 AND posted_on < ? ORDER BY posted_on, rowid`,
		account.String(), before.String())
	if err != nil {
		return nil, err
	}

	return txs, nil
}

// Release removes a pending charge, with ev, in one statement that refuses
// one that is not pending or is in a reconciled period.
func (s *Store) Release(ctx context.Context, id types.ID, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to remove a pending charge: %w", err)
	}
	defer tx.Rollback()

	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT `+lockedWhere+` FROM transactions t WHERE t.id = ?`, id.String()).Scan(&locked); err != nil {
		return fmt.Errorf("removing a pending charge: %w", err)
	}

	if locked {
		return ledgerbus.ErrLocked
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE id = ? AND pending = 1`, id.String())
	if err != nil {
		return fmt.Errorf("removing a pending charge: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmpErr(err, ledgerbus.ErrNotPending)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("removing a pending charge: %w", err)
	}

	return nil
}
