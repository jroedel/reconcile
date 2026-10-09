package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
)

// Explaining an amount (docs/clearing.md, 2).

// initExplanations creates the two tables. A line's transaction is the
// primary key of explanation_lines, which is how "a transaction explains
// at most one amount" is kept: by the database, so that two people adding
// the same charge to two payments at once cannot both. Both go with their
// transactions, so removing a statement removes what it brought from every
// explanation, and an explained transaction's explanation with it.
func initExplanations(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS explanations (
    transaction_id TEXT    PRIMARY KEY REFERENCES transactions (id) ON DELETE CASCADE,
    note           TEXT    NOT NULL DEFAULT '',
    accepted       INTEGER NOT NULL DEFAULT 0 CHECK (accepted IN (0, 1)),
    sources        TEXT    NOT NULL DEFAULT '',
    back_from      INTEGER NOT NULL DEFAULT 1,
    back_to        INTEGER NOT NULL DEFAULT 1,
    created_by     TEXT    NOT NULL,
    created_at     INTEGER NOT NULL,
    updated_by     TEXT    NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS explanation_lines (
    transaction_id TEXT    PRIMARY KEY REFERENCES transactions (id) ON DELETE CASCADE,
    explains       TEXT    NOT NULL REFERENCES explanations (transaction_id) ON DELETE CASCADE,
    added_by       TEXT    NOT NULL,
    added_at       INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS explanation_lines_explains ON explanation_lines (explains);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the explanation tables: %w", err)
	}

	return nil
}

// settledWhere is an explained transaction t whose explanation is settled:
// accepted, or its lines come to its amount.
const settledWhere = `EXISTS (SELECT 1 FROM explanations e WHERE e.transaction_id = t.id AND (e.accepted = 1 OR
    (SELECT coalesce(sum(lt.amount), 0) FROM explanation_lines l JOIN transactions lt ON lt.id = l.transaction_id WHERE l.explains = e.transaction_id) = t.amount
    AND EXISTS (SELECT 1 FROM explanation_lines l WHERE l.explains = e.transaction_id)))`

// Explanation is a transaction's explanation, if it has one.
func (s *Store) Explanation(ctx context.Context, transactionID types.ID) (ledgerbus.Stored, bool, error) {
	var (
		x        ledgerbus.Stored
		sources  string
		accepted int
	)

	err := s.db.QueryRowContext(ctx, `
SELECT note, accepted, sources, back_from, back_to FROM explanations WHERE transaction_id = ?`, transactionID.String()).
		Scan(&x.Note, &accepted, &sources, &x.Back[0], &x.Back[1])

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ledgerbus.Stored{}, false, nil
	case err != nil:
		return ledgerbus.Stored{}, false, fmt.Errorf("reading an explanation: %w", err)
	}

	x.TransactionID, x.Accepted = transactionID, accepted == 1

	if x.Sources, err = ids(sources); err != nil {
		return ledgerbus.Stored{}, false, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT transaction_id FROM explanation_lines WHERE explains = ? ORDER BY added_at, rowid`, transactionID.String())
	if err != nil {
		return ledgerbus.Stored{}, false, fmt.Errorf("reading an explanation's lines: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ledgerbus.Stored{}, false, fmt.Errorf("reading an explanation's lines: %w", err)
		}

		line, err := types.ParseID(id)
		if err != nil {
			return ledgerbus.Stored{}, false, err
		}

		x.Lines = append(x.Lines, line)
	}

	return x, true, rows.Err()
}

// Explained is the account's explained transactions, newest first, for
// remembering how the last one like it was gathered.
func (s *Store) Explained(ctx context.Context, account types.ID) ([]ledgerbus.Remembered, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.description, e.sources, e.back_from, e.back_to FROM explanations e JOIN transactions t ON t.id = e.transaction_id
WHERE t.account_id = ? AND e.sources <> '' ORDER BY t.posted_on DESC, e.updated_at DESC LIMIT 200`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the account's explanations: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Remembered

	for rows.Next() {
		var (
			r       ledgerbus.Remembered
			sources string
		)

		if err := rows.Scan(&r.Description, &sources, &r.Back[0], &r.Back[1]); err != nil {
			return nil, fmt.Errorf("reading the account's explanations: %w", err)
		}

		if r.Sources, err = ids(sources); err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	return out, rows.Err()
}

// Clearing says, of some transactions, which explanation each is a line
// of, and which are explained themselves: each of those with whether its
// explanation is settled.
func (s *Store) Clearing(ctx context.Context, txs []types.ID) (map[types.ID]types.ID, map[types.ID]bool, error) {
	lineOf, explained := map[types.ID]types.ID{}, map[types.ID]bool{}

	for chunk := range chunks(txs, 500) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id.String()
		}

		in := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")

		if err := scanPairs(ctx, s.db, `SELECT transaction_id, explains FROM explanation_lines WHERE transaction_id IN (`+in+`)`, args, func(a, b types.ID) {
			lineOf[a] = b
		}); err != nil {
			return nil, nil, err
		}

		rows, err := s.db.QueryContext(ctx, `
SELECT t.id, CASE WHEN `+settledWhere+` THEN 1 ELSE 0 END FROM transactions t
WHERE t.id IN (`+in+`) AND EXISTS (SELECT 1 FROM explanations e WHERE e.transaction_id = t.id)`, args...)
		if err != nil {
			return nil, nil, fmt.Errorf("reading explanations: %w", err)
		}

		for rows.Next() {
			var (
				id      string
				settled int
			)

			if err := rows.Scan(&id, &settled); err != nil {
				rows.Close()

				return nil, nil, fmt.Errorf("reading explanations: %w", err)
			}

			t, err := types.ParseID(id)
			if err != nil {
				rows.Close()

				return nil, nil, err
			}

			explained[t] = settled == 1
		}

		rows.Close()

		if err := rows.Err(); err != nil {
			return nil, nil, fmt.Errorf("reading explanations: %w", err)
		}
	}

	return lineOf, explained, nil
}

// SaveExplanation writes how an explanation was gathered, adds and removes
// lines, and writes ev, in one transaction. A line some explanation has
// already is refused with ErrExplained, by the line table's key, and
// nothing is saved.
func (s *Store) SaveExplanation(ctx context.Context, x ledgerbus.Stored, add, remove []types.ID, by types.ID, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to save an explanation: %w", err)
	}
	defer tx.Rollback()

	at := ev.At.UnixMilli()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO explanations (transaction_id, sources, back_from, back_to, created_by, created_at, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (transaction_id) DO UPDATE SET sources = excluded.sources, back_from = excluded.back_from,
    back_to = excluded.back_to, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		x.TransactionID.String(), joinIDs(x.Sources), x.Back[0], x.Back[1], by.String(), at, by.String(), at); err != nil {
		return fmt.Errorf("saving an explanation: %w", err)
	}

	for _, id := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM explanation_lines WHERE transaction_id = ? AND explains = ?`,
			id.String(), x.TransactionID.String()); err != nil {
			return fmt.Errorf("taking a line out of an explanation: %w", err)
		}
	}

	for _, id := range add {
		res, err := tx.ExecContext(ctx, `
INSERT INTO explanation_lines (transaction_id, explains, added_by, added_at) VALUES (?, ?, ?, ?)
ON CONFLICT (transaction_id) DO NOTHING`, id.String(), x.TransactionID.String(), by.String(), at)
		if err != nil {
			return fmt.Errorf("adding a line to an explanation: %w", err)
		}

		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return cmpErr(err, ledgerbus.ErrExplained)
		}
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving an explanation: %w", err)
	}

	return nil
}

// Settle writes an explanation's note and whether its difference is
// accepted, with ev.
func (s *Store) Settle(ctx context.Context, transactionID types.ID, note string, accepted bool, by types.ID, ev eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to settle an explanation: %w", err)
	}
	defer tx.Rollback()

	a := 0
	if accepted {
		a = 1
	}

	at := ev.At.UnixMilli()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO explanations (transaction_id, note, accepted, created_by, created_at, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (transaction_id) DO UPDATE SET note = excluded.note, accepted = excluded.accepted,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		transactionID.String(), note, a, by.String(), at, by.String(), at); err != nil {
		return fmt.Errorf("settling an explanation: %w", err)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("settling an explanation: %w", err)
	}

	return nil
}

// cmpErr is err if there was one, else want.
func cmpErr(err, want error) error {
	if err != nil {
		return err
	}

	return want
}

// ids reads a comma-separated list of identifiers.
func ids(s string) ([]types.ID, error) {
	var out []types.ID

	for part := range strings.SplitSeq(s, ",") {
		if part == "" {
			continue
		}

		id, err := types.ParseID(part)
		if err != nil {
			return nil, err
		}

		out = append(out, id)
	}

	return out, nil
}

func joinIDs(list []types.ID) string {
	parts := make([]string, len(list))
	for i, id := range list {
		parts[i] = id.String()
	}

	return strings.Join(parts, ",")
}

// chunks cuts a list for IN clauses under SQLite's variable limit.
func chunks(list []types.ID, n int) func(yield func([]types.ID) bool) {
	return func(yield func([]types.ID) bool) {
		for len(list) > 0 {
			k := min(n, len(list))
			if !yield(list[:k]) {
				return
			}

			list = list[k:]
		}
	}
}

// scanPairs runs a query of two identifier columns.
func scanPairs(ctx context.Context, db *sql.DB, q string, args []any, each func(a, b types.ID)) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("reading explanations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return fmt.Errorf("reading explanations: %w", err)
		}

		x, err := types.ParseID(a)
		if err != nil {
			return err
		}

		y, err := types.ParseID(b)
		if err != nil {
			return err
		}

		each(x, y)
	}

	return rows.Err()
}
