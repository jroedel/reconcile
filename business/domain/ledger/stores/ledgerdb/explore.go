package ledgerdb

import (
	"context"
	"fmt"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Explore reads what a search looks through (ledgerbus/explore.go,
// docs/explorer.md): the transactions of some accounts, or those with a
// part in a project, posted in a span of days, with their parts and the
// kind of each category the parts are in.
//
// Only the accounts, the project and the days are asked of the database.
// Everything else a person may choose -- words, the amount, a category or
// a kind -- is decided in Go, part by part, where words are folded as the
// sorting rules fold them and a part's category and project are judged
// together. The project is asked here because it is the scope: a
// project's list must not read every account in the site to find its
// parts.
//
// The project's transactions are "id IN (SELECT transaction_id FROM
// splits WHERE project_id = ?)", which SQLite builds once from the index
// on splits' project. That is not the slow shape rules.go warns of, which
// was a list rebuilt for every row of an UPDATE.
func (s *Store) Explore(ctx context.Context, w ledgerbus.Within) (ledgerbus.Explorable, error) {
	var (
		where = `posted_on >= ? AND posted_on < ?`
		args  = []any{w.From.String(), w.To.String()}
	)

	switch {
	case !w.Project.Zero():
		where += ` AND id IN (SELECT transaction_id FROM splits WHERE project_id = ?)` +
			` AND account_id NOT IN (SELECT id FROM accounts WHERE reference = 1)`
		args = append(args, w.Project.String())
	case len(w.Accounts) == 0:
		// Neither a project nor any account is nothing, never the whole
		// database.
		return ledgerbus.Explorable{}, nil
	}

	if len(w.Accounts) > 0 {
		in, ids := inList(w.Accounts)
		where += ` AND account_id IN (` + in + `)`
		args = append(args, ids...)
	}

	txs, err := s.withSplits(ctx, where, `posted_on, rowid`, args...)
	if err != nil || len(txs) == 0 {
		return ledgerbus.Explorable{}, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, kind FROM categories WHERE id IN (
SELECT category_id FROM splits WHERE category_id IS NOT NULL AND transaction_id IN (SELECT id FROM transactions WHERE `+where+`))`, args...)
	if err != nil {
		return ledgerbus.Explorable{}, fmt.Errorf("reading the kinds of the parts' categories: %w", err)
	}
	defer rows.Close()

	kinds := map[types.ID]categorybus.Kind{}

	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return ledgerbus.Explorable{}, fmt.Errorf("reading the kinds of the parts' categories: %w", err)
		}

		cat, err := types.ParseID(id)
		if err != nil {
			return ledgerbus.Explorable{}, fmt.Errorf("a category is unreadable: %w", err)
		}

		kinds[cat] = categorybus.Kind(kind)
	}

	if err := rows.Err(); err != nil {
		return ledgerbus.Explorable{}, fmt.Errorf("reading the kinds of the parts' categories: %w", err)
	}

	return ledgerbus.Explorable{Transactions: txs, Kinds: kinds}, nil
}

// Latest is the n newest transactions of the accounts, newest first, with
// their parts: the index on (account_id, posted_on) answers it without
// reading the rest. The parts are read for those n alone, by their IDs.
func (s *Store) Latest(ctx context.Context, accounts []types.ID, n int) ([]ledgerbus.Transaction, error) {
	if len(accounts) == 0 || n <= 0 {
		return nil, nil
	}

	in, args := inList(accounts)

	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE account_id IN (`+in+`) ORDER BY posted_on DESC, rowid DESC LIMIT ?`, append(args, n)...)
	if err != nil || len(txs) == 0 {
		return txs, err
	}

	ids := make([]types.ID, len(txs))
	for i, t := range txs {
		ids[i] = t.ID
	}

	in, args = inList(ids)

	splits, err := s.splits(ctx, `transaction_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}

	for i := range txs {
		txs[i].Splits = splits[txs[i].ID]
	}

	return txs, nil
}

// ProjectAccounts is the accounts a project has parts in, by name, not
// those for reference: what a project's list may be narrowed to, and what
// its rows are named by. Read here rather than asked of tenancybus,
// because a role on a project shows its parts in accounts the reader is
// given nothing on, and tenancybus rightly answers nothing about those.
// Only what a row shows is read: the name, the currency and the
// organization.
func (s *Store) ProjectAccounts(ctx context.Context, projectID types.ID) ([]tenancybus.Account, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, coalesce(org_id, ''), name, currency FROM accounts
WHERE reference = 0 AND id IN (SELECT t.account_id FROM splits s JOIN transactions t ON t.id = s.transaction_id WHERE s.project_id = ?)
ORDER BY lower(name), id`, projectID.String())
	if err != nil {
		return nil, fmt.Errorf("reading a project's accounts: %w", err)
	}
	defer rows.Close()

	var out []tenancybus.Account

	for rows.Next() {
		var (
			a       tenancybus.Account
			id, org string
		)

		if err := rows.Scan(&id, &org, &a.Name, &a.Currency); err != nil {
			return nil, fmt.Errorf("reading a project's accounts: %w", err)
		}

		if a.ID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("an account is unreadable: %w", err)
		}

		if org != "" {
			if a.OrgID, err = types.ParseID(org); err != nil {
				return nil, fmt.Errorf("an account is unreadable: %w", err)
			}
		}

		out = append(out, a)
	}

	return out, rows.Err()
}
