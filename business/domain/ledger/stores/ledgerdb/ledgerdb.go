// Package ledgerdb stores statements, transactions and CSV mappings in
// SQLite.
package ledgerdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/importing/sources/csvsource"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of ledgerbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ ledgerbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"statements": {"id", "account_id", "file_id", "format", "period_start", "period_end", "opening", "closing",
		"checked", "added", "already", "imported_by", "imported_at"},
	"transactions": {"id", "account_id", "statement_id", "posted_on", "description", "amount", "balance",
		"external_id", "hash", "occurrence"},
	"csv_mappings": {"account_id", "fingerprint", "mapping", "updated_by", "updated_at"},
}

// Init creates the tables. After tenancydb and filedb, which they reference.
//
// Two indexes carry the deduplication: the bank's identifier is unique
// within an account where there is one, and the content hash is looked up
// where there is not. The hash is not unique, because a row the bank later
// gives an identifier to keeps its hash (Import).
//
// format and checked are free text rather than CHECKed lists: PDF arrives
// in a later version, and a CHECK cannot be widened without rebuilding the
// table (CLAUDE.md).
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS statements (
    id           TEXT    PRIMARY KEY,
    account_id   TEXT    NOT NULL REFERENCES accounts (id),
    file_id      TEXT    NOT NULL REFERENCES files (id),
    format       TEXT    NOT NULL,
    period_start TEXT    NOT NULL,
    period_end   TEXT    NOT NULL,
    opening      INTEGER,
    closing      INTEGER,
    checked      TEXT    NOT NULL,
    added        INTEGER NOT NULL DEFAULT 0,
    already      INTEGER NOT NULL DEFAULT 0,
    imported_by  TEXT    NOT NULL,
    imported_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS statements_account ON statements (account_id, period_end);

CREATE TABLE IF NOT EXISTS transactions (
    id           TEXT    PRIMARY KEY,
    account_id   TEXT    NOT NULL REFERENCES accounts (id),
    statement_id TEXT    NOT NULL REFERENCES statements (id),
    posted_on    TEXT    NOT NULL,
    description  TEXT    NOT NULL,
    amount       INTEGER NOT NULL,
    balance      INTEGER,
    external_id  TEXT    NOT NULL DEFAULT '',
    hash         TEXT    NOT NULL,
    occurrence   INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS transactions_external ON transactions (account_id, external_id) WHERE external_id <> '';
CREATE INDEX IF NOT EXISTS transactions_hash ON transactions (account_id, hash);
CREATE INDEX IF NOT EXISTS transactions_posted ON transactions (account_id, posted_on);
CREATE INDEX IF NOT EXISTS transactions_statement ON transactions (statement_id);

CREATE TABLE IF NOT EXISTS csv_mappings (
    account_id  TEXT    NOT NULL REFERENCES accounts (id),
    fingerprint TEXT    NOT NULL,
    mapping     TEXT    NOT NULL,
    updated_by  TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (account_id, fingerprint)
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the ledger tables: %w", err)
	}

	return nil
}

// --- importing ----------------------------------------------------------------

// Import stores a statement and the transactions not already in the
// account, one at a time inside one transaction, so that two identical rows
// in one file, or a FITID a bank repeated, are seen by the second as
// already here -- in the preview exactly as in the import.
//
// The rule for each row, from eumaeus:
//
//   - With a bank identifier already in the account: already here.
//   - With a bank identifier, and a row with the same content hash and no
//     identifier: already here, and that row is given the identifier. It
//     came from a CSV of the same period; from now on the match is exact.
//   - With no identifier and the same hash already in the account: already
//     here.
//   - Otherwise it is new.
func (s *Store) Import(ctx context.Context, st ledgerbus.Statement, txs []ledgerbus.Transaction, mapping *ledgerbus.SavedMapping, ev eventbus.Event, commit bool) (ledgerbus.Statement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("starting the import: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO statements (id, account_id, file_id, format, period_start, period_end, opening, closing, checked, imported_by, imported_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.ID.String(), st.AccountID.String(), st.FileID.String(), string(st.Format), st.Start.String(), st.End.String(),
		nullAmount(st.Opening, st.HasOpening), nullAmount(st.Closing, st.HasClosing), string(st.Checked),
		st.ImportedBy.String(), st.ImportedAt.UnixMilli()); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("storing the statement: %w", err)
	}

	st.Added, st.Already = 0, 0

	for _, t := range txs {
		known, err := already(ctx, tx, t)
		if err != nil {
			return ledgerbus.Statement{}, err
		}

		if known {
			st.Already++

			continue
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, balance, external_id, hash, occurrence)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID.String(), t.AccountID.String(), st.ID.String(), t.PostedOn.String(), t.Description, int64(t.Amount),
			nullAmount(t.Balance, t.HasBalance), t.ExternalID, t.Hash, t.Occurrence); err != nil {
			return ledgerbus.Statement{}, fmt.Errorf("storing a transaction: %w", err)
		}

		st.Added++
	}

	if !commit {
		return st, nil
	}

	if _, err := tx.ExecContext(ctx, `UPDATE statements SET added = ?, already = ? WHERE id = ?`,
		st.Added, st.Already, st.ID.String()); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("counting the statement: %w", err)
	}

	if mapping != nil {
		data, err := json.Marshal(mapping.Mapping)
		if err != nil {
			return ledgerbus.Statement{}, fmt.Errorf("remembering the columns: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO csv_mappings (account_id, fingerprint, mapping, updated_by, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (account_id, fingerprint) DO UPDATE SET mapping = excluded.mapping, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			st.AccountID.String(), mapping.Fingerprint, string(data), mapping.By.String(), mapping.At.UnixMilli()); err != nil {
			return ledgerbus.Statement{}, fmt.Errorf("remembering the columns: %w", err)
		}
	}

	ev.Detail = ledgerbus.EventDetail(st)
	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return ledgerbus.Statement{}, err
	}

	if err := tx.Commit(); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("saving the import: %w", err)
	}

	return st, nil
}

// already applies the rule in Import's comment to one row.
func already(ctx context.Context, tx *sql.Tx, t ledgerbus.Transaction) (bool, error) {
	exists := func(q string, args ...any) (bool, error) {
		var one int

		err := tx.QueryRowContext(ctx, q, args...).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("looking for a transaction: %w", err)
		}

		return true, nil
	}

	account := t.AccountID.String()

	if t.ExternalID == "" {
		return exists(`SELECT 1 FROM transactions WHERE account_id = ? AND hash = ? LIMIT 1`, account, t.Hash)
	}

	if found, err := exists(`SELECT 1 FROM transactions WHERE account_id = ? AND external_id = ?`, account, t.ExternalID); found || err != nil {
		return found, err
	}

	// Adopt: one row, the oldest, so that two identical CSV rows are
	// claimed by two OFX rows one each.
	res, err := tx.ExecContext(ctx, `
UPDATE transactions SET external_id = ?
WHERE rowid = (SELECT rowid FROM transactions WHERE account_id = ? AND hash = ? AND external_id = '' ORDER BY rowid LIMIT 1)`,
		t.ExternalID, account, t.Hash)
	if err != nil {
		return false, fmt.Errorf("matching a transaction: %w", err)
	}

	n, err := res.RowsAffected()

	return n == 1, err
}

// --- statements -------------------------------------------------------------

const statementColumns = `s.id, s.account_id, s.file_id, f.name, s.format, s.period_start, s.period_end, s.opening, s.closing,
s.checked, s.added, s.already, s.imported_by, s.imported_at`

const statementFrom = ` FROM statements s JOIN files f ON f.id = s.file_id `

// StatementByID finds one.
func (s *Store) StatementByID(ctx context.Context, id types.ID) (ledgerbus.Statement, error) {
	st, err := scanStatement(s.db.QueryRowContext(ctx, `SELECT `+statementColumns+statementFrom+`WHERE s.id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return ledgerbus.Statement{}, ledgerbus.ErrNotFound
	}

	return st, err
}

// StatementWithFile finds a statement in an account read from the same
// bytes, whoever uploaded them.
func (s *Store) StatementWithFile(ctx context.Context, account types.ID, sha string) (ledgerbus.Statement, bool, error) {
	st, err := scanStatement(s.db.QueryRowContext(ctx, `SELECT `+statementColumns+statementFrom+`
WHERE s.account_id = ? AND f.sha256 = ? ORDER BY s.imported_at LIMIT 1`, account.String(), sha))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ledgerbus.Statement{}, false, nil
	case err != nil:
		return ledgerbus.Statement{}, false, err
	}

	return st, true, nil
}

// Statements is an account's, newest period first.
func (s *Store) Statements(ctx context.Context, account types.ID) ([]ledgerbus.Statement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statementColumns+statementFrom+`
WHERE s.account_id = ? ORDER BY s.period_end DESC, s.imported_at DESC`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the statements: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Statement

	for rows.Next() {
		st, err := scanStatement(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, st)
	}

	return out, rows.Err()
}

// RemoveStatement deletes a statement and the transactions it brought in,
// and writes the history, in one transaction.
func (s *Store) RemoveStatement(ctx context.Context, st ledgerbus.Statement, ev eventbus.Event) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE statement_id = ?`, st.ID.String())
	if err != nil {
		return 0, fmt.Errorf("removing the statement's transactions: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM statements WHERE id = ?`, st.ID.String()); err != nil {
		return 0, fmt.Errorf("removing the statement: %w", err)
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("saving a change: %w", err)
	}

	return int(n), nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanStatement(row scanner) (ledgerbus.Statement, error) {
	var (
		st                       ledgerbus.Statement
		id, account, file, by    string
		format, start, end, chkd string
		opening, closing         sql.NullInt64
		at                       int64
	)

	if err := row.Scan(&id, &account, &file, &st.FileName, &format, &start, &end, &opening, &closing,
		&chkd, &st.Added, &st.Already, &by, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ledgerbus.Statement{}, err
		}

		return ledgerbus.Statement{}, fmt.Errorf("reading a statement: %w", err)
	}

	var e [6]error
	st.ID, e[0] = types.ParseID(id)
	st.AccountID, e[1] = types.ParseID(account)
	st.FileID, e[2] = types.ParseID(file)
	st.ImportedBy, e[3] = types.ParseID(by)
	st.Start, e[4] = types.ParseDate(start)
	st.End, e[5] = types.ParseDate(end)

	if err := errors.Join(e[:]...); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("a stored statement is unreadable: %w", err)
	}

	st.Format = ledgerbus.Format(format)
	st.Checked = ledgerbus.Method(chkd)
	st.Opening, st.HasOpening = money.Amount(opening.Int64), opening.Valid
	st.Closing, st.HasClosing = money.Amount(closing.Int64), closing.Valid
	st.ImportedAt = time.UnixMilli(at).UTC()

	return st, nil
}

// --- transactions -------------------------------------------------------------

// Months sums an account's transactions by month, newest first.
func (s *Store) Months(ctx context.Context, account types.ID) ([]ledgerbus.Month, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT substr(posted_on, 1, 7), count(*),
       coalesce(sum(CASE WHEN amount > 0 THEN amount END), 0),
       coalesce(sum(CASE WHEN amount < 0 THEN amount END), 0),
       min(posted_on)
FROM transactions WHERE account_id = ?
GROUP BY 1 ORDER BY 1 DESC`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the months: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Month

	for rows.Next() {
		var (
			m        ledgerbus.Month
			in, outs int64
			earliest string
		)

		if err := rows.Scan(&m.Month, &m.Count, &in, &outs, &earliest); err != nil {
			return nil, fmt.Errorf("reading the months: %w", err)
		}

		m.In, m.Out = money.Amount(in), money.Amount(outs)
		m.Earliest, _ = types.ParseDate(earliest)

		out = append(out, m)
	}

	return out, rows.Err()
}

// Transactions is an account's transactions posted from from up to but not
// including to, in the order they were posted and then imported.
func (s *Store) Transactions(ctx context.Context, account types.ID, from, to types.Date) ([]ledgerbus.Transaction, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, account_id, statement_id, posted_on, description, amount, balance, external_id, hash, occurrence
FROM transactions WHERE account_id = ? AND posted_on >= ? AND posted_on < ?
ORDER BY posted_on, rowid`, account.String(), from.String(), to.String())
	if err != nil {
		return nil, fmt.Errorf("reading the transactions: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Transaction

	for rows.Next() {
		var (
			t                      ledgerbus.Transaction
			id, acct, stmt, posted string
			amount                 int64
			balance                sql.NullInt64
		)

		if err := rows.Scan(&id, &acct, &stmt, &posted, &t.Description, &amount, &balance, &t.ExternalID, &t.Hash, &t.Occurrence); err != nil {
			return nil, fmt.Errorf("reading the transactions: %w", err)
		}

		var e [4]error
		t.ID, e[0] = types.ParseID(id)
		t.AccountID, e[1] = types.ParseID(acct)
		t.StatementID, e[2] = types.ParseID(stmt)
		t.PostedOn, e[3] = types.ParseDate(posted)

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a stored transaction is unreadable: %w", err)
		}

		t.Amount = money.Amount(amount)
		t.Balance, t.HasBalance = money.Amount(balance.Int64), balance.Valid

		out = append(out, t)
	}

	return out, rows.Err()
}

// --- mappings -----------------------------------------------------------------

// Mappings is every CSV mapping remembered for an account, by header
// fingerprint.
func (s *Store) Mappings(ctx context.Context, account types.ID) (map[string]csvsource.Mapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint, mapping FROM csv_mappings WHERE account_id = ? ORDER BY updated_at DESC`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the remembered columns: %w", err)
	}
	defer rows.Close()

	out := map[string]csvsource.Mapping{}

	for rows.Next() {
		var (
			fingerprint, data string
			m                 csvsource.Mapping
		)

		if err := rows.Scan(&fingerprint, &data); err != nil {
			return nil, fmt.Errorf("reading the remembered columns: %w", err)
		}

		// One that no longer reads is forgotten rather than fatal: the
		// person is asked for the columns again.
		if json.Unmarshal([]byte(data), &m) == nil {
			out[fingerprint] = m
		}
	}

	return out, rows.Err()
}

func nullAmount(a money.Amount, ok bool) any {
	if !ok {
		return nil
	}

	return int64(a)
}
