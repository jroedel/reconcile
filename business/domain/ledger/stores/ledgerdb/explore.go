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

	read := ledgerbus.Explorable{Transactions: txs, Kinds: kinds}

	if w.Facts {
		if read.Facts, err = s.facts(ctx, where, args); err != nil {
			return ledgerbus.Explorable{}, err
		}
	}

	return read, nil
}

// factsQuery is, for each transaction t of a search, the three facts the
// accountant's lists need that are not its own columns (ledgerbus.Facts):
// its payee is reported; its explanation is explained, as settledWhere
// (explain.go) has it but accepted only with a note; and an explanation
// joins it to a transaction in an account for reference, the reference
// one being either the line or what the line explains. Every EXISTS is
// answered by a primary key or an index: payees' id, explanations'
// transaction, explanation_lines' transaction and its explains.
const factsQuery = `SELECT t.id,
    EXISTS (SELECT 1 FROM payees p WHERE p.id = t.payee_id AND p.reported = 1),
    EXISTS (SELECT 1 FROM explanations e WHERE e.transaction_id = t.id AND (e.accepted = 1 AND e.note <> '' OR
        (SELECT coalesce(sum(lt.amount), 0) FROM explanation_lines l JOIN transactions lt ON lt.id = l.transaction_id WHERE l.explains = e.transaction_id) = t.amount
        AND EXISTS (SELECT 1 FROM explanation_lines l WHERE l.explains = e.transaction_id))),
    EXISTS (SELECT 1 FROM explanation_lines l JOIN transactions o ON o.id = l.explains JOIN accounts a ON a.id = o.account_id
        WHERE l.transaction_id = t.id AND a.reference = 1)
    OR EXISTS (SELECT 1 FROM explanation_lines l JOIN transactions o ON o.id = l.transaction_id JOIN accounts a ON a.id = o.account_id
        WHERE l.explains = t.id AND a.reference = 1)
FROM transactions t WHERE t.id IN (SELECT id FROM transactions WHERE `

// facts reads the Facts of the transactions where says, keeping only those
// with any: most have none, and a missing one is the zero Facts.
func (s *Store) facts(ctx context.Context, where string, args []any) (map[types.ID]ledgerbus.Facts, error) {
	rows, err := s.db.QueryContext(ctx, factsQuery+where+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading what the accountant needs to know: %w", err)
	}
	defer rows.Close()

	out := map[types.ID]ledgerbus.Facts{}

	for rows.Next() {
		var (
			id string
			f  ledgerbus.Facts
		)

		if err := rows.Scan(&id, &f.Reported, &f.Explained, &f.Reference); err != nil {
			return nil, fmt.Errorf("reading what the accountant needs to know: %w", err)
		}

		if f == (ledgerbus.Facts{}) {
			continue
		}

		tx, err := types.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("a transaction is unreadable: %w", err)
		}

		out[tx] = f
	}

	return out, rows.Err()
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
