package ledgerdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
)

// AccountFigures adds each account's transactions up in the database rather
// than reading them: a list shows a figure for every account, and an
// account of some years is thousands of rows. A pending charge that has
// posted is left out, as everywhere it is counted: it is the same money as
// the row it posted as.
func (s *Store) AccountFigures(ctx context.Context, ids []types.ID) (map[types.ID]ledgerbus.Figures, error) {
	out := make(map[types.ID]ledgerbus.Figures, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	in, args := idList(ids)

	rows, err := s.db.QueryContext(ctx, `
SELECT t.account_id, a.currency, min(t.posted_on), max(t.posted_on), count(*),
       coalesce(sum(CASE WHEN t.amount > 0 THEN t.amount END), 0),
       coalesce(sum(CASE WHEN t.amount < 0 THEN t.amount END), 0)
FROM transactions t JOIN accounts a ON a.id = t.account_id
WHERE t.account_id IN (`+in+`) AND t.posted_as IS NULL
GROUP BY t.account_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("adding up the accounts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, currency, first, last string
			f                         ledgerbus.Figures
			ins, outs                 int64
		)

		if err := rows.Scan(&id, &currency, &first, &last, &f.Count, &ins, &outs); err != nil {
			return nil, fmt.Errorf("adding up the accounts: %w", err)
		}

		key, err := figured(id, first, last, &f)
		if err != nil {
			return nil, err
		}

		f.Totals = []ledgerbus.Total{{Currency: currency, In: money.Amount(ins), Out: money.Amount(outs)}}
		out[key] = f
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adding up the accounts: %w", err)
	}

	return out, s.balances(ctx, in, args, out)
}

// balances adds to each account's figures the closing balance of its latest
// statement that was checked against one. A statement imported unchecked
// may carry a balance nobody compared with anything, so it is not the one
// shown. An account with a balance and no transactions -- a statement of a
// quiet month -- still gets its balance.
func (s *Store) balances(ctx context.Context, in string, args []any, out map[types.ID]ledgerbus.Figures) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT account_id, period_end, closing FROM (
    SELECT account_id, period_end, closing,
           row_number() OVER (PARTITION BY account_id ORDER BY period_end DESC, imported_at DESC) AS n
    FROM statements
    WHERE account_id IN (`+in+`) AND closing IS NOT NULL AND checked IN (?, ?)
) WHERE n = 1`, append(args, string(ledgerbus.ByBalances), string(ledgerbus.ByTotals))...)
	if err != nil {
		return fmt.Errorf("reading the accounts' balances: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, on  string
			closing int64
		)

		if err := rows.Scan(&id, &on, &closing); err != nil {
			return fmt.Errorf("reading the accounts' balances: %w", err)
		}

		key, err1 := types.ParseID(id)
		day, err2 := types.ParseDate(on)

		if err1 != nil || err2 != nil {
			return fmt.Errorf("an account's balance is unreadable: %q on %q", id, on)
		}

		f := out[key]
		f.Balance, f.BalanceOn = money.Amount(closing), day
		out[key] = f
	}

	return rows.Err()
}

// ProjectFigures adds up each project's parts: the days and the count from
// their transactions, and the totals from the parts themselves, by currency
// and kind, as the project's book does. Two queries for the reason
// monthKinds is a second one: counting transactions and summing parts in
// one would count a transaction once for each of its parts.
func (s *Store) ProjectFigures(ctx context.Context, ids []types.ID) (map[types.ID]ledgerbus.Figures, error) {
	out := make(map[types.ID]ledgerbus.Figures, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	in, args := idList(ids)

	rows, err := s.db.QueryContext(ctx, `
SELECT s.project_id, min(t.posted_on), max(t.posted_on), count(DISTINCT t.id)
FROM splits s JOIN transactions t ON t.id = s.transaction_id
WHERE s.project_id IN (`+in+`)
GROUP BY s.project_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("adding up the projects: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, first, last string
			f               ledgerbus.Figures
		)

		if err := rows.Scan(&id, &first, &last, &f.Count); err != nil {
			return nil, fmt.Errorf("adding up the projects: %w", err)
		}

		key, err := figured(id, first, last, &f)
		if err != nil {
			return nil, err
		}

		out[key] = f
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adding up the projects: %w", err)
	}

	return out, s.projectTotals(ctx, in, args, out)
}

// projectTotals is the second of ProjectFigures' queries. A part with no
// category is unsorted whatever kind it would have, as in the book
// (ledgerbus.totals); the empty kind is that.
func (s *Store) projectTotals(ctx context.Context, in string, args []any, out map[types.ID]ledgerbus.Figures) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.project_id, a.currency, CASE WHEN s.category_id IS NULL THEN '' ELSE coalesce(c.kind, '') END,
       coalesce(sum(CASE WHEN s.amount > 0 THEN s.amount END), 0),
       coalesce(sum(CASE WHEN s.amount < 0 THEN s.amount END), 0)
FROM splits s
JOIN transactions t ON t.id = s.transaction_id
JOIN accounts a ON a.id = t.account_id
LEFT JOIN categories c ON c.id = s.category_id
WHERE s.project_id IN (`+in+`)
GROUP BY 1, 2, 3
ORDER BY 1, 2`, args...)
	if err != nil {
		return fmt.Errorf("adding up the projects' parts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, currency, kind string
			ins, outs          int64
		)

		if err := rows.Scan(&id, &currency, &kind, &ins, &outs); err != nil {
			return fmt.Errorf("adding up the projects' parts: %w", err)
		}

		key, err := types.ParseID(id)
		if err != nil {
			return fmt.Errorf("a project's parts are unreadable: %w", err)
		}

		f := out[key]

		// In currency order, so the last total is the one to add to or
		// the place for a new one.
		if n := len(f.Totals); n == 0 || f.Totals[n-1].Currency != currency {
			f.Totals = append(f.Totals, ledgerbus.Total{Currency: currency})
		}

		t := &f.Totals[len(f.Totals)-1]
		t.In += money.Amount(ins)
		t.Out += money.Amount(outs)
		t.Add(categorybus.Kind(kind), money.Amount(ins+outs))

		out[key] = f
	}

	return rows.Err()
}

// figured reads the id and the days that begin each row of figures.
func figured(id, first, last string, f *ledgerbus.Figures) (types.ID, error) {
	key, e1 := types.ParseID(id)

	var e2, e3 error
	f.First, e2 = types.ParseDate(first)
	f.Last, e3 = types.ParseDate(last)

	if e1 != nil || e2 != nil || e3 != nil {
		return types.ID{}, fmt.Errorf("the figures of %q are unreadable", id)
	}

	return key, nil
}

// idList is the placeholders and the arguments for an IN list of ids.
func idList(ids []types.ID) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}

	return strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", "), args
}
