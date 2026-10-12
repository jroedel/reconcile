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
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Pending charges (docs/clearing.md, 4; ledgerbus/pending.go).

// fit is how well a posted row fits a pending charge as its posted form,
// or 0 for not at all: a row of the same holder (or naming nobody), not
// pending, dated on the pending one's day or up to PostWithin after, of
// an amount the pending one may have become.
//
//   - 3: the same wording, and any amount the same way round
//     (ledgerbus.PostsAs): a fuel hold of 1.00 that posts at 48.20.
//   - 2 and up to 3: one wording inside the other, the more of it the
//     likelier, or one cut short, within a third or PostSlack.
//   - 1: only the first two words the same (ledgerbus.SameMerchant), and
//     the same amount or more by a tip (ledgerbus.Tipped): a printed
//     page's "CAFE EXAMPLE PAL A…" and the download's "CAFE EXAMPLE CITY
//     ST" (issue #104).
//
// The same holder whatever the account's option (issue #81): with it off,
// another holder's posted charge at the same shop took a pending charge's
// place, and the first holder's charge was gone from their month. Two
// rows that both name a holder, and not the same one, are two people's.
func fit(p, t ledgerbus.Transaction) float64 {
	if t.Pending || !ledgerbus.SameHolder(p.Holder, t.Holder) ||
		t.PostedOn.Before(p.PostedOn) || p.PostedOn.AddDays(ledgerbus.PostWithin).Before(t.PostedOn) {
		return 0
	}

	switch like := alike(p.Description, t.Description); {
	case like == 3:
		if ledgerbus.PostsAs(p.Amount, t.Amount, true) {
			return 3
		}
	case like >= 2:
		if ledgerbus.PostsAs(p.Amount, t.Amount, false) {
			return like
		}
	case ledgerbus.Truncated(p.Description, t.Description):
		if ledgerbus.PostsAs(p.Amount, t.Amount, false) {
			return 2
		}
	case ledgerbus.SameMerchant(p.Description, t.Description):
		if ledgerbus.Tipped(p.Amount, t.Amount) {
			return 1
		}
	}

	return 0
}

// pair matches pending charges with posted rows where the match is plain:
// a pending charge and a posted row that each fit the other better than
// anything else fits either (fit). Two holds at one shop and one charge,
// or one hold and two charges, fit equally, and are left for a person
// (Store.Pairs), as the issue asked: a wrong match hides a charge, where
// a pending one left waiting is on the page to see. It returns, for each
// pending charge paired, the posted row's index.
//
// It used to take, for each posted row in the file's order, the likest
// pending charge and then the nearest in days. That paired two holds at
// one shop by their dates, which are the bank's and not the shop's.
func pair(holds, posted []ledgerbus.Transaction) map[int]int {
	score := make([][]float64, len(holds))
	for i, p := range holds {
		score[i] = make([]float64, len(posted))
		for j, t := range posted {
			score[i][j] = fit(p, t)
		}
	}

	// best is the one index whose score is highest and above nothing, or
	// -1 for none or a tie.
	best := func(n int, at func(int) float64) int {
		b, tied := -1, false

		for k := range n {
			switch v := at(k); {
			case v == 0:
			case b < 0 || v > at(b):
				b, tied = k, false
			case v == at(b):
				tied = true
			}
		}

		if tied {
			return -1
		}

		return b
	}

	out := map[int]int{}

	for i := range holds {
		j := best(len(posted), func(j int) float64 { return score[i][j] })
		if j < 0 {
			continue
		}

		if best(len(holds), func(k int) float64 { return score[k][j] }) == i {
			out[i] = j
		}
	}

	return out
}

// posting is what a file's import clears: each pending charge, stored or
// one of the file's own new rows, and the new posted row it became.
type posting struct {
	hold   ledgerbus.Transaction
	posted ledgerbus.Transaction
	stored bool // the pending charge is from an earlier statement
}

// postings pairs the file's new posted rows with the pending charges they
// clear: the account's from other statements, not yet cleared and not in
// a reconciled period, and the file's own new pending rows, since a
// bank's page goes on listing an authorization for days after its charge
// has posted -- one card's printed month had a coffee pending on the 28th
// and posted on the 29th, a car hire's hold of 925.92 beside its charge
// of 753.64.
func postings(ctx context.Context, tx *sql.Tx, st ledgerbus.Statement, txs []ledgerbus.Transaction, fresh []int) ([]posting, error) {
	var (
		holds, posted []ledgerbus.Transaction
		stored        int
		from, to      types.Date
	)

	for _, i := range fresh {
		t := txs[i]
		if t.Pending {
			continue
		}

		posted = append(posted, t)

		if from.Zero() || t.PostedOn.Before(from) {
			from = t.PostedOn
		}

		if to.Zero() || to.Before(t.PostedOn) {
			to = t.PostedOn
		}
	}

	if len(posted) == 0 {
		return nil, nil
	}

	holds, err := pendingBetween(ctx, tx, st.AccountID, from.AddDays(-ledgerbus.PostWithin), to, st.ID)
	if err != nil {
		return nil, err
	}

	stored = len(holds)

	for _, i := range fresh {
		if txs[i].Pending {
			holds = append(holds, txs[i])
		}
	}

	var out []posting

	for i, j := range pair(holds, posted) {
		out = append(out, posting{hold: holds[i], posted: posted[j], stored: i < stored})
	}

	// In the posted charges' order, so that the history reads as the
	// month does.
	slices.SortFunc(out, func(a, b posting) int {
		return cmp.Or(cmp.Compare(a.posted.PostedOn.String(), b.posted.PostedOn.String()), cmp.Compare(a.posted.ID.String(), b.posted.ID.String()))
	})

	return out, nil
}

// settle takes the pending mark off the stored charge a posted row of the
// file is already (ledgerdb.already), and not cleared by another row: by
// its identity, a remembered wording, or one of the two cut short -- the
// same holder's, or one that names nobody, as already matches. Nothing else about it changes, so the
// history has nothing to say.
//
// Three lookups joined by UNION, each on an index of its own, rather than
// one WHERE with the three joined by OR. That reads the same and is not:
// the OR with a subquery in it keeps SQLite from using either index, so
// each call read every transaction of the account, and an import calls it
// for every row it already holds -- every row of the file, in the second
// pass a preview makes (Store.Import). A statement of two thousand rows
// into a full account took longer than the server lets a request run, and
// the person importing it saw the proxy's "Bad Gateway".
func settle(ctx context.Context, tx *sql.Tx, st *ledgerbus.Statement, t ledgerbus.Transaction) error {
	rows, err := tx.QueryContext(ctx, `
SELECT rowid, holder FROM transactions WHERE account_id = ? AND hash = ? AND pending = 1 AND posted_as IS NULL
UNION
SELECT rowid, holder FROM transactions WHERE account_id = ? AND external_id = ? AND external_id <> '' AND pending = 1 AND posted_as IS NULL
UNION
SELECT t.rowid, t.holder FROM transaction_aliases a JOIN transactions t ON t.id = a.transaction_id
WHERE a.account_id = ? AND a.hash = ? AND t.pending = 1 AND t.posted_as IS NULL
ORDER BY 1`,
		t.AccountID.String(), t.Hash, t.AccountID.String(), t.ExternalID, t.AccountID.String(), t.Hash)
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
		res, err := tx.ExecContext(ctx, `UPDATE transactions SET pending = 0 WHERE rowid = ? AND posted_as IS NULL`, match[0])
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
			res, err := tx.ExecContext(ctx, `UPDATE transactions SET pending = 0 WHERE rowid = ? AND pending = 1 AND posted_as IS NULL`, id)
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

// pendingBetween is the account's pending charges from other statements,
// not cleared, dated from one day to another, outside every reconciled
// period.
func pendingBetween(ctx context.Context, tx *sql.Tx, account types.ID, from, to types.Date, statement types.ID) ([]ledgerbus.Transaction, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, posted_on, description, amount, holder FROM transactions t
WHERE t.account_id = ? AND t.pending = 1 AND t.posted_as IS NULL AND t.statement_id <> ? AND t.posted_on BETWEEN ? AND ? AND NOT `+lockedWhere+`
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

// clearHold marks a pending charge as having posted as t, and gives t what a
// person made of it, with ev: its parts, its place in an explanation. A
// pending charge's receipts follow it on their side (receiptbus), which
// the ledger does not reach. One statement claims the charge, so that two
// imports at once cannot clear it twice, and a posted charge clears one
// pending charge at most (transactions_posted_as).
//
// Parts a person chose -- a category, a project or a memo, not a rule's --
// go to the posted charge: one part follows its amount, and of several
// the last takes the difference, since the tip on a restaurant's charge
// is the last thing added to it, unless that would leave it nothing or
// turn it round, when the posted charge keeps the one part it came with,
// for a person to split again. A pending charge only a rule sorted, or
// nobody, leaves the posted charge as it came, sorted by today's rules.
// Either way the pending charge keeps no parts, so that it is in no total.
func clearHold(ctx context.Context, tx *sql.Tx, p, t ledgerbus.Transaction, ev eventbus.Event) error {
	res, err := tx.ExecContext(ctx, `UPDATE transactions SET posted_as = ? WHERE id = ? AND pending = 1 AND posted_as IS NULL`,
		t.ID.String(), p.ID.String())
	if sqldb.IsUniqueViolation(err) {
		return ledgerbus.ErrNotPosted
	}

	if err != nil {
		return fmt.Errorf("clearing a pending charge: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmpErr(err, ledgerbus.ErrNotPending)
	}

	type part struct {
		id     string
		amount money.Amount
		chosen bool
	}

	rows, err := tx.QueryContext(ctx, `
SELECT id, amount, rule_id IS NULL AND (category_id IS NOT NULL OR project_id IS NOT NULL OR memo <> '')
FROM splits WHERE transaction_id = ? ORDER BY position`, p.ID.String())
	if err != nil {
		return fmt.Errorf("reading a pending charge's parts: %w", err)
	}

	var (
		parts  []part
		chosen bool
	)

	for rows.Next() {
		var (
			pt part
			a  int64
		)

		if err := rows.Scan(&pt.id, &a, &pt.chosen); err != nil {
			rows.Close()

			return fmt.Errorf("reading a pending charge's parts: %w", err)
		}

		pt.amount = money.Amount(a)
		parts = append(parts, pt)
		chosen = chosen || pt.chosen
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading a pending charge's parts: %w", err)
	}

	resplit, last := "0", len(parts)-1

	switch {
	case !chosen:
	case len(parts) == 1:
		err = moveParts(ctx, tx, p.ID, t.ID, parts[0].id, t.Amount)
	case sameSign(parts[last].amount+t.Amount-p.Amount, t.Amount):
		err = moveParts(ctx, tx, p.ID, t.ID, parts[last].id, parts[last].amount+t.Amount-p.Amount)
	default:
		resplit = "1"
	}

	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM splits WHERE transaction_id = ?`, p.ID.String()); err != nil {
		return fmt.Errorf("clearing a pending charge's parts: %w", err)
	}

	// A line of an explanation: the posted charge is the line now, unless
	// it is one already; the explanation's difference follows its amount.
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE explanation_lines SET transaction_id = ? WHERE transaction_id = ?`,
		t.ID.String(), p.ID.String()); err != nil {
		return fmt.Errorf("moving a pending charge's explanation: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM explanation_lines WHERE transaction_id = ?`, p.ID.String()); err != nil {
		return fmt.Errorf("moving a pending charge's explanation: %w", err)
	}

	ev.Detail["parts"] = strconv.Itoa(len(parts))
	if !chosen {
		ev.Detail["parts"] = "0"
	}

	ev.Detail["resplit"] = resplit

	return eventdb.Insert(ctx, tx, ev)
}

// moveParts gives the posted charge the pending charge's parts in place
// of its own, the one named taking amount.
func moveParts(ctx context.Context, tx *sql.Tx, from, to types.ID, partID string, amount money.Amount) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM splits WHERE transaction_id = ?`, to.String()); err != nil {
		return fmt.Errorf("giving a posted charge its parts: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE splits SET amount = ? WHERE id = ?`, int64(amount), partID); err != nil {
		return fmt.Errorf("giving a posted charge its parts: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE splits SET transaction_id = ? WHERE transaction_id = ?`, to.String(), from.String()); err != nil {
		return fmt.Errorf("giving a posted charge its parts: %w", err)
	}

	return nil
}

// sameSign reports a part that is neither nothing nor turned round from
// the whole.
func sameSign(part, whole money.Amount) bool {
	return part != 0 && (part < 0) == (whole < 0)
}

// postedEvent is the history line of an import's clearing, as
// ledgerbus.PostedEvent is a person's.
func postedEvent(ctx context.Context, tx *sql.Tx, st ledgerbus.Statement, p, t ledgerbus.Transaction) (eventbus.Event, error) {
	var currency string
	if err := tx.QueryRowContext(ctx, `SELECT currency FROM accounts WHERE id = ?`, st.AccountID.String()).Scan(&currency); err != nil {
		return eventbus.Event{}, fmt.Errorf("reading the account's currency: %w", err)
	}

	return ledgerbus.PostedEvent(st.ImportedAt, st.ImportedBy, tenancybus.Account{ID: st.AccountID, Currency: currency}, p, t), nil
}

// StillPending is the account's pending charges not cleared, dated more
// than within before the end of its latest statement -- not an entry by hand, which
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

	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE account_id = ? AND pending = 1 AND posted_as IS NULL AND posted_on < ? ORDER BY posted_on, rowid`,
		account.String(), before.String())
	if err != nil {
		return nil, err
	}

	return txs, nil
}

// Release removes a pending charge, with ev, in one statement that refuses
// one that is not pending, or cleared, or is in a reconciled period.
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

	res, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE id = ? AND pending = 1 AND posted_as IS NULL`, id.String())
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

// Pairs is the account's pending charges not cleared and outside every
// reconciled period, each with the posted charges that fit it (fit),
// themselves not another's posted form nor in a reconciled period: what
// an import could not decide, and what a person may.
func (s *Store) Pairs(ctx context.Context, account types.ID, within int) ([]ledgerbus.Pair, error) {
	holds, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions t
WHERE t.account_id = ? AND t.pending = 1 AND t.posted_as IS NULL AND NOT `+lockedWhere+` ORDER BY t.posted_on, t.rowid`, account.String())
	if err != nil || len(holds) == 0 {
		return nil, err
	}

	// The posted charges of each pending charge's days, one query each
	// on the account's dates, rather than every one from the first
	// pending charge to the last: one hold that never posted, years ago,
	// would otherwise read years of the account on every view of its
	// page. Pending charges waiting are few.
	var out []ledgerbus.Pair

	for _, p := range holds {
		posted, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions t
WHERE t.account_id = ? AND t.posted_on BETWEEN ? AND ? AND t.pending = 0 AND NOT `+lockedWhere+`
AND NOT EXISTS (SELECT 1 FROM transactions h WHERE h.posted_as = t.id) ORDER BY t.posted_on, t.rowid`,
			account.String(), p.PostedOn.String(), p.PostedOn.AddDays(within).String())
		if err != nil {
			return nil, err
		}

		for _, t := range posted {
			if fit(p, t) > 0 {
				out = append(out, ledgerbus.Pair{Pending: p, Posted: t})
			}
		}
	}

	return out, nil
}

// Clear marks a pending charge as having posted as t (clearHold), with ev,
// refusing either in a reconciled period: the pending charge's totals
// and the posted one's parts would change in a month somebody has said
// agrees with the bank.
func (s *Store) Clear(ctx context.Context, p, t ledgerbus.Transaction, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to clear a pending charge: %w", err)
	}
	defer tx.Rollback()

	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT count(*) > 0 FROM transactions t WHERE t.id IN (?, ?) AND `+lockedWhere,
		p.ID.String(), t.ID.String()).Scan(&locked); err != nil {
		return fmt.Errorf("checking the reconciled periods: %w", err)
	}

	if locked {
		return ledgerbus.ErrLocked
	}

	if err := clearHold(ctx, tx, p, t, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clearing a pending charge: %w", err)
	}

	return nil
}

// Unclear undoes Clear, with ev: the pending charge counts again, with
// one part for its whole amount. Refused in a reconciled period.
func (s *Store) Unclear(ctx context.Context, p ledgerbus.Transaction, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to undo a clearing: %w", err)
	}
	defer tx.Rollback()

	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT `+lockedWhere+` FROM transactions t WHERE t.id = ?`, p.ID.String()).Scan(&locked); err != nil {
		return fmt.Errorf("undoing a clearing: %w", err)
	}

	if locked {
		return ledgerbus.ErrLocked
	}

	res, err := tx.ExecContext(ctx, `UPDATE transactions SET posted_as = NULL WHERE id = ? AND posted_as IS NOT NULL`, p.ID.String())
	if err != nil {
		return fmt.Errorf("undoing a clearing: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmpErr(err, ledgerbus.ErrNotPending)
	}

	if err := insertSplits(ctx, tx, []ledgerbus.Split{{ID: types.NewID(), TransactionID: p.ID, Amount: p.Amount}}); err != nil {
		return err
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("undoing a clearing: %w", err)
	}

	return nil
}

// unclearAll gives back their parts to the pending charges cleared by the
// rows of a statement about to be removed, which then count again: the
// posted charge is going, and the hold is all the account has of it.
func unclearAll(ctx context.Context, tx *sql.Tx, statement types.ID) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO splits (id, transaction_id, position, amount)
SELECT lower(hex(randomblob(16))), h.id, 0, h.amount FROM transactions h
WHERE h.posted_as IN (SELECT id FROM transactions WHERE statement_id = ?) AND h.statement_id <> ?`,
		statement.String(), statement.String()); err != nil {
		return fmt.Errorf("giving pending charges back their parts: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE transactions SET posted_as = NULL WHERE posted_as IN (SELECT id FROM transactions WHERE statement_id = ?)`,
		statement.String()); err != nil {
		return fmt.Errorf("giving pending charges back their parts: %w", err)
	}

	return nil
}

// ClearedHolds is the account's cleared pending charges, each with the
// posted charge it became.
func (s *Store) ClearedHolds(ctx context.Context, account types.ID) (map[types.ID]types.ID, error) {
	out := map[types.ID]types.ID{}

	err := scanPairs(ctx, s.db, `SELECT id, posted_as FROM transactions WHERE account_id = ? AND posted_as IS NOT NULL`,
		[]any{account.String()}, func(hold, posted types.ID) { out[hold] = posted })

	return out, err
}

// HoldOf is the pending charge a posted charge cleared, if one did.
func (s *Store) HoldOf(ctx context.Context, posted types.ID) (ledgerbus.Transaction, bool, error) {
	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE posted_as = ?`, posted.String())
	if err != nil || len(txs) == 0 {
		return ledgerbus.Transaction{}, false, err
	}

	return txs[0], true, nil
}
