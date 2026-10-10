package ledgerdb

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/types"
)

// blankWhere is a transaction a sorting rule may still sort: one part,
// with no category, no project and no memo, outside every reconciled
// period. Written over the alias t, as lockedWhere is.
const blankWhere = `(SELECT count(*) FROM splits s WHERE s.transaction_id = t.id) = 1
AND EXISTS (SELECT 1 FROM splits s WHERE s.transaction_id = t.id
            AND s.category_id IS NULL AND s.project_id IS NULL AND s.memo = '')
AND NOT ` + lockedWhere

// Blank is the account's transactions a sorting rule may still sort, in
// date order, with their one part.
func (s *Store) Blank(ctx context.Context, account types.ID) ([]ledgerbus.Transaction, error) {
	return s.withSplits(ctx, `id IN (SELECT t.id FROM transactions t WHERE t.account_id = ? AND `+blankWhere+`)`,
		`posted_on, rowid`, account.String())
}

// SortByRule gives each part its rule's choice, with the lines of project
// history that go with it, and writes ev with how many it sorted if that
// is any.
//
// Each part is updated only if it is still blank: a person who saved the
// transaction, split it, or reconciled its month between the read and this
// write has decided, and the rule loses. Saving a transaction replaces its
// parts with new ones, so a part that was replaced is simply not found.
func (s *Store) SortByRule(ctx context.Context, sorted []ledgerbus.SortedPart, ev eventbus.Event) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	n := 0

	for _, p := range sorted {
		res, err := tx.ExecContext(ctx, `
UPDATE splits SET category_id = ?, project_id = ?, rule_id = ?
WHERE id = ? AND transaction_id IN (SELECT t.id FROM transactions t WHERE `+blankWhere+`)`,
			nullID(p.Split.CategoryID), nullID(p.Split.ProjectID), nullID(p.Split.RuleID), p.Split.ID.String())
		if err != nil {
			return 0, fmt.Errorf("sorting by rule: %w", err)
		}

		if hit, err := res.RowsAffected(); err != nil || hit == 0 {
			continue
		}

		n++

		for _, e := range p.Events {
			if err := eventdb.Insert(ctx, tx, e); err != nil {
				return 0, err
			}
		}
	}

	if n == 0 {
		return 0, nil
	}

	ev.Detail = map[string]string{"count": strconv.Itoa(n)}
	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("saving a change: %w", err)
	}

	return n, nil
}

// RuleCounts is how many parts of the account each rule sorted and nobody
// has changed since.
func (s *Store) RuleCounts(ctx context.Context, account types.ID) (map[types.ID]int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.rule_id, count(*)
FROM splits s JOIN transactions t ON t.id = s.transaction_id
WHERE t.account_id = ? AND s.rule_id IS NOT NULL
GROUP BY 1`, account.String())
	if err != nil {
		return nil, fmt.Errorf("counting what the rules sorted: %w", err)
	}
	defer rows.Close()

	out := map[types.ID]int{}

	for rows.Next() {
		var (
			id string
			n  int
		)

		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("counting what the rules sorted: %w", err)
		}

		rid, err := types.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("a stored rule is unreadable: %w", err)
		}

		out[rid] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting what the rules sorted: %w", err)
	}

	return out, nil
}

// maxExamples is the most transactions suggestions learn from, newest
// first: years of one account, and a bound on a page's work.
const maxExamples = 2000

// Examples is the account's transactions a person sorted into a category,
// newest first: one part, with a category, that no rule chose. A split
// transaction is left out, because its description says nothing about
// which of its parts is which.
func (s *Store) Examples(ctx context.Context, account types.ID) ([]rulebus.Example, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT CASE WHEN t.payee <> '' THEN t.payee || ' ' || t.description ELSE t.description END, s.category_id
FROM splits s JOIN transactions t ON t.id = s.transaction_id
WHERE t.account_id = ? AND s.category_id IS NOT NULL AND s.rule_id IS NULL
  AND (SELECT count(*) FROM splits x WHERE x.transaction_id = t.id) = 1
ORDER BY t.posted_on DESC, t.rowid DESC
LIMIT ?`, account.String(), maxExamples)
	if err != nil {
		return nil, fmt.Errorf("reading what was sorted: %w", err)
	}
	defer rows.Close()

	var out []rulebus.Example

	for rows.Next() {
		var (
			e  rulebus.Example
			id string
		)

		if err := rows.Scan(&e.Description, &id); err != nil {
			return nil, fmt.Errorf("reading what was sorted: %w", err)
		}

		if e.CategoryID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("a stored part is unreadable: %w", err)
		}

		out = append(out, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading what was sorted: %w", err)
	}

	return out, nil
}
