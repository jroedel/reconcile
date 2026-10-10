// Package ledgerdb stores statements, transactions and CSV mappings in
// SQLite.
package ledgerdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/category/categorybus"
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
		"checked", "added", "already", "imported_by", "imported_at", "shape"},
	"transactions": {"id", "account_id", "statement_id", "posted_on", "description", "amount", "balance",
		"external_id", "hash", "occurrence", "holder", "pending", "check_number", "payee"},
	"csv_mappings":        {"account_id", "fingerprint", "mapping", "updated_by", "updated_at"},
	"splits":              {"id", "transaction_id", "position", "amount", "category_id", "project_id", "memo", "rule_id"},
	"transaction_aliases": {"account_id", "hash", "transaction_id", "created_at"},
	"explanations": {"transaction_id", "note", "accepted", "sources", "back_from", "back_to",
		"created_by", "created_at", "updated_by", "updated_at"},
	"explanation_lines": {"transaction_id", "explains", "added_by", "added_at"},
	"holder_accounts":   {"account_id", "set_by", "set_at"},
	"statement_inbox": {"id", "user_id", "file_id", "sha256", "name", "source", "received_at",
		"closed_at", "dismissed_at"},
	"reconciliations": {"statement_id", "account_id", "period_start", "period_end", "note",
		"reconciled_by", "reconciled_at"},
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

	// The signature of a statement's file's layout (docs/shapes.md, 4), a
	// later column; empty for every statement from before it, which then
	// proposes nothing for the next file in its layout.
	if err := sqldb.AddColumn(ctx, db, "statements", "shape", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// The holder a file said made the charge (docs/clearing.md, 3), a
	// later column beside the CREATE; empty for every row from before it
	// and for every file that names nobody.
	if err := sqldb.AddColumn(ctx, db, "transactions", "holder", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// Whether its file listed it as not yet posted (docs/clearing.md, 4),
	// a later column too; 0 for every row from before it.
	if err := sqldb.AddColumn(ctx, db, "transactions", "pending", "INTEGER NOT NULL DEFAULT 0 CHECK (pending IN (0, 1))"); err != nil {
		return err
	}

	// The number of the check it paid, when its file said (docs/shapes.md,
	// 3), and a later column again; empty for every row from before it.
	if err := sqldb.AddColumn(ctx, db, "transactions", "check_number", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// To whom a check was written, as a person read it off its image
	// (ledgerbus/checks.go); empty for every row until somebody says.
	if err := sqldb.AddColumn(ctx, db, "transactions", "payee", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// A check's image is matched by its number within its account. After
	// the AddColumn, since an index on a later column built in the CREATE
	// block fails on every database from before it.
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS transactions_check ON transactions (account_id, check_number) WHERE check_number <> ''`); err != nil {
		return fmt.Errorf("creating the check number index: %w", err)
	}

	if err := initSplits(ctx, db); err != nil {
		return err
	}

	if err := initReconciliations(ctx, db); err != nil {
		return err
	}

	if err := initAliases(ctx, db); err != nil {
		return err
	}

	if err := initExplanations(ctx, db); err != nil {
		return err
	}

	if err := initHolders(ctx, db); err != nil {
		return err
	}

	return initInbox(ctx, db)
}

// initReconciliations creates the table of statements a person has checked
// against the paper (ledgerbus/reconcile.go). A statement has one or none,
// so the statement is the key, and marking is an insert that does nothing
// the second time.
//
// The reference to the statement does not cascade. A reconciled statement
// is reopened before it can be removed, and the foreign key holds that
// even against a mistake in the code above it.
//
// account_id is the statement's, copied, because the question asked of
// this table on every edit is "is this day of this account locked", and
// that is one index rather than a join.
func initReconciliations(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS reconciliations (
    statement_id  TEXT    PRIMARY KEY REFERENCES statements (id),
    account_id    TEXT    NOT NULL REFERENCES accounts (id),
    period_start  TEXT    NOT NULL,
    period_end    TEXT    NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    reconciled_by TEXT    NOT NULL,
    reconciled_at INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS reconciliations_account ON reconciliations (account_id, period_start);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the reconciliations table: %w", err)
	}

	return nil
}

// lockedWhere is the condition a transaction t is in a reconciled period
// of its account under. Dates are ISO text, which compares as dates do.
const lockedWhere = `EXISTS (SELECT 1 FROM reconciliations r
WHERE r.account_id = t.account_id AND t.posted_on BETWEEN r.period_start AND r.period_end)`

// initSplits creates the splits table, which arrived after the first
// statements were imported, and gives every transaction without parts one
// for its whole amount.
//
// The second statement runs on every start and finds nothing to do after
// the first: Import makes a transaction's split with it. It is here, rather
// than once, so that it cannot be forgotten -- a transaction with no parts
// is money in no category and no project, and missing from every total.
// The identifier is sixteen random bytes in lower-case hex, which is what
// types.NewID makes.
//
// A split goes with its transaction (ON DELETE CASCADE): removing a
// statement removes how its transactions were sorted.
func initSplits(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS splits (
    id             TEXT    PRIMARY KEY,
    transaction_id TEXT    NOT NULL REFERENCES transactions (id) ON DELETE CASCADE,
    position       INTEGER NOT NULL,
    amount         INTEGER NOT NULL,
    category_id    TEXT    REFERENCES categories (id),
    project_id     TEXT    REFERENCES projects (id),
    memo           TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS splits_transaction ON splits (transaction_id, position);
CREATE INDEX IF NOT EXISTS splits_project ON splits (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS splits_category ON splits (category_id) WHERE category_id IS NOT NULL;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the splits table: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO splits (id, transaction_id, position, amount)
SELECT lower(hex(randomblob(16))), t.id, 0, t.amount FROM transactions t
WHERE NOT EXISTS (SELECT 1 FROM splits s WHERE s.transaction_id = t.id)`); err != nil {
		return fmt.Errorf("giving every transaction its part: %w", err)
	}

	// The sorting rule that sorted a part (docs/sorting.md), a later
	// column beside the CREATE; NULL for a part a person sorted or nobody
	// has. Not a foreign key: removing a rule leaves what it sorted as it
	// is, still saying a rule did it, until a person saves the transaction.
	return sqldb.AddColumn(ctx, db, "splits", "rule_id", "TEXT")
}

// --- importing ----------------------------------------------------------------

// Import stores a statement and the transactions not already in the
// account, in one transaction -- in the preview exactly as in the import.
//
// Each row is first matched on its own, by the rules from eumaeus:
//
//   - With a bank identifier already in the account, or repeated earlier in
//     the same file: already here.
//   - With a bank identifier, and a row with the same content hash (or the
//     same wording remembered as another name for it) and no identifier:
//     already here, and that row is given the identifier. It came from a
//     CSV of the same period; from now on the match is exact.
//   - With no identifier and the same hash, or a remembered wording, already
//     in the account: already here.
//   - On the same day for the same amount as a row of another statement
//     whose description is this one's cut short, or this one is that one's
//     (ledgerbus.Truncated): already here, and given to it the identifier
//     if it has one. A bank's printed page cuts descriptions short, and the
//     statement that follows it does not.
//
// What is left is new by every exact rule, and the count rule then looks at
// those together (doubts.go): one charge worded two ways in two files is
// set aside rather than stored twice.
//
// With commit false all of it is done and rolled back, and then done a
// second time inside the same transaction, which must add nothing and set
// nothing aside (ErrUnstable). That is the import checking it is
// idempotent on the very file in front of it.
func (s *Store) Import(ctx context.Context, st ledgerbus.Statement, txs []ledgerbus.Transaction, also map[types.ID][]eventbus.Event, mapping *ledgerbus.SavedMapping, ev eventbus.Event, commit bool) (ledgerbus.Statement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("starting the import: %w", err)
	}
	defer tx.Rollback()

	st, err = importRows(ctx, tx, st, txs, also)
	if err != nil {
		return ledgerbus.Statement{}, err
	}

	if !commit {
		st2, txs2 := secondPass(st, txs)

		again, err := importRows(ctx, tx, st2, txs2, nil)
		if err != nil {
			return ledgerbus.Statement{}, err
		}

		if again.Added != 0 || again.SetAside() != 0 {
			return ledgerbus.Statement{}, fmt.Errorf("%w: the second time it added %d and set aside %d", ledgerbus.ErrUnstable, again.Added, again.SetAside())
		}

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

	// The statement's counts, under what the caller said of it: an entry
	// by hand names its description and amount.
	detail := ledgerbus.EventDetail(st)
	for k, v := range ev.Detail {
		detail[k] = v
	}

	ev.Detail = detail
	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return ledgerbus.Statement{}, err
	}

	if err := tx.Commit(); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("saving the import: %w", err)
	}

	return st, nil
}

// importRows stores the statement and its new rows inside tx, and counts
// them.
func importRows(ctx context.Context, tx *sql.Tx, st ledgerbus.Statement, txs []ledgerbus.Transaction, also map[types.ID][]eventbus.Event) (ledgerbus.Statement, error) {
	// The rows' identities were made under the account's holder option
	// as it was when the file was read. If it has changed since, rows that
	// name a holder would be matched under the wrong rule; rows that
	// name nobody are the same under either.
	on, err := byHolder(ctx, tx, st.AccountID)
	if err != nil {
		return ledgerbus.Statement{}, err
	}

	if on != st.ByHolder && slices.ContainsFunc(txs, func(t ledgerbus.Transaction) bool { return t.Holder != "" }) {
		return ledgerbus.Statement{}, fmt.Errorf("%w: the account's holder option changed while the file was read", ledgerbus.ErrUnstable)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO statements (id, account_id, file_id, format, period_start, period_end, opening, closing, checked, imported_by, imported_at, shape)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.ID.String(), st.AccountID.String(), st.FileID.String(), string(st.Format), st.Start.String(), st.End.String(),
		nullAmount(st.Opening, st.HasOpening), nullAmount(st.Closing, st.HasClosing), string(st.Checked),
		st.ImportedBy.String(), st.ImportedAt.UnixMilli(), st.Shape); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("storing the statement: %w", err)
	}

	st.Added, st.Already, st.ByRule, st.Doubts = 0, 0, 0, nil

	// Each row on its own, in the file's order. Nothing is inserted yet:
	// the count rule needs the rows of other statements as they were.
	var fresh []int

	seen := map[string]bool{}

	for i, t := range txs {
		known := t.ExternalID != "" && seen[t.ExternalID]
		seen[t.ExternalID] = true

		if !known {
			var err error
			if known, err = already(ctx, tx, t, on); err != nil {
				return ledgerbus.Statement{}, err
			}
		}

		if known {
			st.Already++

			// The posted form of a pending charge worded and priced as
			// it was is the same row, and only stops being pending.
			if !t.Pending {
				if err := settle(ctx, tx, &st, t, on); err != nil {
					return ledgerbus.Statement{}, err
				}
			}

			continue
		}

		fresh = append(fresh, i)
	}

	// A posted charge whose pending form the account already holds takes
	// its place (pending.go), and is new to nothing after.
	if fresh, err = post(ctx, tx, &st, txs, fresh, on); err != nil {
		return ledgerbus.Statement{}, err
	}

	doubts, err := countRule(ctx, tx, st.ID, txs, fresh, on)
	if err != nil {
		return ledgerbus.Statement{}, err
	}

	aside := map[int]bool{}
	for _, d := range doubts {
		aside[d.Index] = !d.Imported
	}

	for _, i := range fresh {
		t := txs[i]

		if aside[i] {
			continue
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO transactions (id, account_id, statement_id, posted_on, description, amount, balance, external_id, hash, occurrence, holder, pending, check_number)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID.String(), t.AccountID.String(), st.ID.String(), t.PostedOn.String(), t.Description, int64(t.Amount),
			nullAmount(t.Balance, t.HasBalance), t.ExternalID, t.Hash, t.Occurrence, t.Holder, t.Pending, t.CheckNumber); err != nil {
			return ledgerbus.Statement{}, fmt.Errorf("storing a transaction: %w", err)
		}

		if err := insertSplits(ctx, tx, t.Splits); err != nil {
			return ledgerbus.Statement{}, err
		}

		// The project history of what a rule put into a project: only for
		// a transaction that was new, so here.
		for _, e := range also[t.ID] {
			if err := eventdb.Insert(ctx, tx, e); err != nil {
				return ledgerbus.Statement{}, err
			}
		}

		if t.ByRule() {
			st.ByRule++
		}

		st.Added++
	}

	// What was set aside is remembered as another name for the row it was
	// taken for, so that the next file worded the same way matches it
	// exactly and is not asked about again.
	for _, d := range doubts {
		if d.Imported {
			continue
		}

		if err := remember(ctx, tx, txs[d.Index], d.Twin, st.ImportedAt); err != nil {
			return ledgerbus.Statement{}, err
		}

		st.Already++
	}

	st.Doubts = doubts

	// What it added inside a reconciled period, counted here because
	// only here is it known which rows were new. The business refuses an
	// import that counts any (ledgerbus.ErrLocked); the preview shows the
	// number.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions t WHERE t.statement_id = ? AND `+lockedWhere,
		st.ID.String()).Scan(&st.Locked); err != nil {
		return ledgerbus.Statement{}, fmt.Errorf("checking the reconciled periods: %w", err)
	}

	return st, nil
}

// already applies the rule in Import's comment to one row.
func already(ctx context.Context, tx *sql.Tx, t ledgerbus.Transaction, byHolder bool) (bool, error) {
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
		if found, err := exists(`SELECT 1 FROM transactions WHERE account_id = ? AND hash = ? LIMIT 1`, account, t.Hash); found || err != nil {
			return found, err
		}

		if found, err := exists(`SELECT 1 FROM transaction_aliases WHERE account_id = ? AND hash = ?`, account, t.Hash); found || err != nil {
			return found, err
		}

		// As many cut-short matches as this row's occurrence: two charges
		// the page printed alike are two, and claim two.
		rows, err := cutShort(ctx, tx, t, false, byHolder)

		return len(rows) >= max(t.Occurrence, 1), err
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

	if n, err := res.RowsAffected(); n == 1 || err != nil {
		return n == 1, err
	}

	// A wording remembered as another name for an unclaimed row: the same.
	res, err = tx.ExecContext(ctx, `
UPDATE transactions SET external_id = ?
WHERE external_id = '' AND id = (SELECT transaction_id FROM transaction_aliases WHERE account_id = ? AND hash = ?)`,
		t.ExternalID, account, t.Hash)
	if err != nil {
		return false, fmt.Errorf("matching a transaction: %w", err)
	}

	if n, err := res.RowsAffected(); n == 1 || err != nil {
		return n == 1, err
	}

	rows, err := cutShort(ctx, tx, t, true, byHolder)
	if err != nil || len(rows) == 0 {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE transactions SET external_id = ? WHERE rowid = ?`, t.ExternalID, rows[0]); err != nil {
		return false, fmt.Errorf("matching a transaction: %w", err)
	}

	return true, nil
}

// cutShort is the rows of other statements on t's day for t's amount whose
// description is t's cut short or the other way round, oldest first; with
// unclaimed, only those no bank identifier has claimed.
//
// On an account split by holder, only rows of t's holder count,
// and rows that name nobody (ledgerbus.SameHolder).
func cutShort(ctx context.Context, tx *sql.Tx, t ledgerbus.Transaction, unclaimed, byHolder bool) ([]int64, error) {
	q := `SELECT rowid, description, holder FROM transactions WHERE account_id = ? AND posted_on = ? AND amount = ? AND statement_id <> ?`
	if unclaimed {
		q += ` AND external_id = ''`
	}

	rows, err := tx.QueryContext(ctx, q+` ORDER BY rowid`, t.AccountID.String(), t.PostedOn.String(), int64(t.Amount), t.StatementID.String())
	if err != nil {
		return nil, fmt.Errorf("looking for a transaction: %w", err)
	}
	defer rows.Close()

	var out []int64

	for rows.Next() {
		var (
			id           int64
			desc, holder string
		)

		if err := rows.Scan(&id, &desc, &holder); err != nil {
			return nil, fmt.Errorf("looking for a transaction: %w", err)
		}

		if byHolder && !ledgerbus.SameHolder(holder, t.Holder) {
			continue
		}

		if ledgerbus.Truncated(desc, t.Description) {
			out = append(out, id)
		}
	}

	return out, rows.Err()
}

// --- statements -------------------------------------------------------------

const statementColumns = `s.id, s.account_id, s.file_id, f.name, s.format, s.period_start, s.period_end, s.opening, s.closing,
s.checked, s.added, s.already, s.imported_by, s.imported_at, s.shape,
r.period_start, r.period_end, r.note, r.reconciled_by, r.reconciled_at`

const statementFrom = ` FROM statements s JOIN files f ON f.id = s.file_id
LEFT JOIN reconciliations r ON r.statement_id = s.id `

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

// Shaped is those of the accounts given that have a statement whose file
// was in the layout with this signature.
func (s *Store) Shaped(ctx context.Context, signature string, accounts []types.ID) ([]types.ID, error) {
	if signature == "" || len(accounts) == 0 {
		return nil, nil
	}

	args := []any{signature}
	for _, a := range accounts {
		args = append(args, a.String())
	}

	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT account_id FROM statements WHERE shape = ? AND account_id IN (`+
		strings.TrimSuffix(strings.Repeat("?, ", len(accounts)), ", ")+`) ORDER BY account_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the accounts of a layout: %w", err)
	}
	defer rows.Close()

	var out []types.ID

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reading the accounts of a layout: %w", err)
		}

		a, err := types.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("reading the accounts of a layout: %w", err)
		}

		out = append(out, a)
	}

	return out, rows.Err()
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

	// Transactions the statement brought into a period another statement
	// was reconciled for are that period's now: removing them would change
	// a month somebody has said agrees with the bank.
	var locked int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions t WHERE t.statement_id = ? AND `+lockedWhere,
		st.ID.String()).Scan(&locked); err != nil {
		return 0, fmt.Errorf("checking the reconciled periods: %w", err)
	}

	if locked > 0 {
		return 0, ledgerbus.ErrLocked
	}

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
		rStart, rEnd, rNote, rBy sql.NullString
		rAt                      sql.NullInt64
	)

	if err := row.Scan(&id, &account, &file, &st.FileName, &format, &start, &end, &opening, &closing,
		&chkd, &st.Added, &st.Already, &by, &at, &st.Shape, &rStart, &rEnd, &rNote, &rBy, &rAt); err != nil {
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

	if rBy.Valid {
		r, err := reconciliation(st.ID.String(), account, rStart.String, rEnd.String, rNote.String, rBy.String, rAt.Int64)
		if err != nil {
			return ledgerbus.Statement{}, err
		}

		st.Reconciliation = r
	}

	return st, nil
}

// --- transactions -------------------------------------------------------------

// Months sums an account's transactions by month, newest first.
func (s *Store) Months(ctx context.Context, account types.ID) ([]ledgerbus.Month, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT substr(posted_on, 1, 7), count(*),
       coalesce(sum(CASE WHEN amount > 0 THEN amount END), 0),
       coalesce(sum(CASE WHEN amount < 0 THEN amount END), 0),
       min(posted_on),
       sum(EXISTS (SELECT 1 FROM splits s WHERE s.transaction_id = t.id AND s.category_id IS NULL)),
       sum(EXISTS (SELECT 1 FROM splits s WHERE s.transaction_id = t.id AND s.rule_id IS NOT NULL))
FROM transactions t WHERE account_id = ?
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

		if err := rows.Scan(&m.Month, &m.Count, &in, &outs, &earliest, &m.Unsorted, &m.ByRule); err != nil {
			return nil, fmt.Errorf("reading the months: %w", err)
		}

		m.In, m.Out = money.Amount(in), money.Amount(outs)
		m.Earliest, _ = types.ParseDate(earliest)

		out = append(out, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the months: %w", err)
	}

	return out, s.monthKinds(ctx, account, out)
}

// monthKinds adds each month's parts up by their category's kind. A
// second query rather than more columns on the first: that one counts
// transactions, this one sums parts, and joining parts into it would count
// a transaction once for each of its parts.
func (s *Store) monthKinds(ctx context.Context, account types.ID, months []ledgerbus.Month) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT substr(t.posted_on, 1, 7), coalesce(c.kind, ''), sum(s.amount)
FROM splits s
JOIN transactions t ON t.id = s.transaction_id
LEFT JOIN categories c ON c.id = s.category_id
WHERE t.account_id = ?
GROUP BY 1, 2`, account.String())
	if err != nil {
		return fmt.Errorf("adding the months up by kind: %w", err)
	}
	defer rows.Close()

	at := map[string]int{}
	for i, m := range months {
		at[m.Month] = i
	}

	for rows.Next() {
		var (
			month, kind string
			sum         int64
		)

		if err := rows.Scan(&month, &kind, &sum); err != nil {
			return fmt.Errorf("adding the months up by kind: %w", err)
		}

		if i, ok := at[month]; ok {
			months[i].Operations.Add(categorybus.Kind(kind), money.Amount(sum))
		}
	}

	return rows.Err()
}

// Settling is the sums of an owner's transfer and pass-through categories,
// across every account whose parts use them.
func (s *Store) Settling(ctx context.Context, owner types.Scope) ([]ledgerbus.SettlingRow, error) {
	column := "c.account_id"
	if owner.Kind == types.ScopeOrg {
		column = "c.org_id"
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.name, c.kind, a.currency, substr(t.posted_on, 1, 7), sum(s.amount)
FROM splits s
JOIN categories c ON c.id = s.category_id
JOIN transactions t ON t.id = s.transaction_id
JOIN accounts a ON a.id = t.account_id
WHERE `+column+` = ? AND c.kind IN ('transfer', 'passthrough')
    -- A transfer explained by what it paid for, in the organization's own
    -- accounts, is met: the card it paid has charges, not a payment.
    AND NOT (c.kind = 'transfer' AND `+settledWhere+`)
GROUP BY c.id, a.currency, 5
ORDER BY 5`, owner.ID.String())
	if err != nil {
		return nil, fmt.Errorf("reading what should come back to zero: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.SettlingRow

	for rows.Next() {
		var (
			r        ledgerbus.SettlingRow
			id, kind string
			sum      int64
		)

		if err := rows.Scan(&id, &r.Category.Name, &kind, &r.Currency, &r.Month, &sum); err != nil {
			return nil, fmt.Errorf("reading what should come back to zero: %w", err)
		}

		if r.Category.ID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("a category is unreadable: %w", err)
		}

		r.Category.Kind, r.Category.Owner, r.Sum = categorybus.Kind(kind), owner, money.Amount(sum)
		out = append(out, r)
	}

	return out, rows.Err()
}

// Transactions is an account's transactions posted from from up to but not
// including to, in the order they were posted and then imported, with their
// parts.
func (s *Store) Transactions(ctx context.Context, account types.ID, from, to types.Date) ([]ledgerbus.Transaction, error) {
	const where = `account_id = ? AND posted_on >= ? AND posted_on < ?`

	args := []any{account.String(), from.String(), to.String()}

	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE `+where+` ORDER BY posted_on, rowid`, args...)
	if err != nil {
		return nil, err
	}

	splits, err := s.splits(ctx, `transaction_id IN (SELECT id FROM transactions WHERE `+where+`)`, args...)
	if err != nil {
		return nil, err
	}

	for i := range txs {
		txs[i].Splits = splits[txs[i].ID]
	}

	return txs, nil
}

// TransactionByID is one transaction with its parts.
func (s *Store) TransactionByID(ctx context.Context, id types.ID) (ledgerbus.Transaction, error) {
	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE id = ?`, id.String())
	if err != nil {
		return ledgerbus.Transaction{}, err
	}

	if len(txs) == 0 {
		return ledgerbus.Transaction{}, ledgerbus.ErrNotFound
	}

	splits, err := s.splits(ctx, `transaction_id = ?`, id.String())
	if err != nil {
		return ledgerbus.Transaction{}, err
	}

	txs[0].Splits = splits[id]

	return txs[0], nil
}

// TransactionsByID is several transactions with their parts.
func (s *Store) TransactionsByID(ctx context.Context, ids []types.ID) ([]ledgerbus.Transaction, error) {
	in, args := inList(ids)

	return s.withSplits(ctx, `id IN (`+in+`)`, `posted_on, rowid`, args...)
}

// Matching is the transactions in the accounts for the amount either way
// round, posted from from to to inclusive, in date order.
func (s *Store) Matching(ctx context.Context, accounts []types.ID, amount money.Amount, from, to types.Date) ([]ledgerbus.Transaction, error) {
	in, args := inList(accounts)
	args = append(args, int64(amount), -int64(amount), from.String(), to.String())

	return s.withSplits(ctx, `account_id IN (`+in+`) AND amount IN (?, ?) AND posted_on >= ? AND posted_on <= ?`, `posted_on, rowid`, args...)
}

// withSplits reads the transactions matching a condition, and their parts.
func (s *Store) withSplits(ctx context.Context, where, order string, args ...any) ([]ledgerbus.Transaction, error) {
	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE `+where+` ORDER BY `+order, args...)
	if err != nil || len(txs) == 0 {
		return txs, err
	}

	splits, err := s.splits(ctx, `transaction_id IN (SELECT id FROM transactions WHERE `+where+`)`, args...)
	if err != nil {
		return nil, err
	}

	for i := range txs {
		txs[i].Splits = splits[txs[i].ID]
	}

	return txs, nil
}

func inList(ids []types.ID) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))

	for i, id := range ids {
		marks[i], args[i] = "?", id.String()
	}

	return strings.Join(marks, ", "), args
}

const transactionColumns = `id, account_id, statement_id, posted_on, description, amount, balance, external_id, hash, occurrence, holder, pending, check_number, payee`

func (s *Store) transactions(ctx context.Context, q string, args ...any) ([]ledgerbus.Transaction, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
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

		if err := rows.Scan(&id, &acct, &stmt, &posted, &t.Description, &amount, &balance, &t.ExternalID, &t.Hash, &t.Occurrence, &t.Holder, &t.Pending, &t.CheckNumber, &t.Payee); err != nil {
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

// --- splits -----------------------------------------------------------------

const splitColumns = `id, transaction_id, position, amount, category_id, project_id, memo, rule_id`

// splits reads the parts matching a condition, by transaction, in order.
func (s *Store) splits(ctx context.Context, where string, args ...any) (map[types.ID][]ledgerbus.Split, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+splitColumns+` FROM splits WHERE `+where+` ORDER BY transaction_id, position`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the parts: %w", err)
	}
	defer rows.Close()

	out := map[types.ID][]ledgerbus.Split{}

	for rows.Next() {
		sp, err := scanSplit(rows)
		if err != nil {
			return nil, err
		}

		out[sp.TransactionID] = append(out[sp.TransactionID], sp)
	}

	return out, rows.Err()
}

func scanSplit(row scanner, extra ...any) (ledgerbus.Split, error) {
	var (
		sp                ledgerbus.Split
		id, tx            string
		amount            int64
		category, project sql.NullString
		rule              sql.NullString
	)

	if err := row.Scan(append([]any{&id, &tx, &sp.Position, &amount, &category, &project, &sp.Memo, &rule}, extra...)...); err != nil {
		return sp, fmt.Errorf("reading a part: %w", err)
	}

	var e [5]error
	sp.ID, e[0] = types.ParseID(id)
	sp.TransactionID, e[1] = types.ParseID(tx)

	if category.Valid {
		sp.CategoryID, e[2] = types.ParseID(category.String)
	}

	if project.Valid {
		sp.ProjectID, e[3] = types.ParseID(project.String)
	}

	if rule.Valid {
		sp.RuleID, e[4] = types.ParseID(rule.String)
	}

	if err := errors.Join(e[:]...); err != nil {
		return sp, fmt.Errorf("a stored part is unreadable: %w", err)
	}

	sp.Amount = money.Amount(amount)

	return sp, nil
}

func insertSplits(ctx context.Context, tx *sql.Tx, splits []ledgerbus.Split) error {
	for _, sp := range splits {
		if _, err := tx.ExecContext(ctx, `INSERT INTO splits (`+splitColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sp.ID.String(), sp.TransactionID.String(), sp.Position, int64(sp.Amount),
			nullID(sp.CategoryID), nullID(sp.ProjectID), sp.Memo, nullID(sp.RuleID)); err != nil {
			return fmt.Errorf("storing a part: %w", err)
		}
	}

	return nil
}

// ReplaceSplits writes a transaction's new parts in place of the old, and
// the history, in one transaction.
func (s *Store) ReplaceSplits(ctx context.Context, transactionID types.ID, splits []ledgerbus.Split, events []eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM splits WHERE transaction_id = ?`, transactionID.String()); err != nil {
		return fmt.Errorf("replacing the parts: %w", err)
	}

	if err := insertSplits(ctx, tx, splits); err != nil {
		return err
	}

	for _, ev := range events {
		if err := eventdb.Insert(ctx, tx, ev); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving a change: %w", err)
	}

	return nil
}

// ProjectLines is every part in a project, with what a page shows about
// its transaction, account and category, oldest first.
//
// Joined across the tenancy and category tables rather than asked of their
// domains line by line: a project's book is read whole, and a line knows
// nothing more about its account than the name and currency.
func (s *Store) ProjectLines(ctx context.Context, projectID types.ID) ([]ledgerbus.ProjectLine, error) {
	return s.lines(ctx, `s.project_id = ?`, projectID.String())
}

// OrgLines is every part of an organization's accounts posted from from to
// before to, with the same about each as a project's line: an
// organization's budget year (budgetbus).
func (s *Store) OrgLines(ctx context.Context, orgID types.ID, from, to types.Date) ([]ledgerbus.ProjectLine, error) {
	return s.lines(ctx, `a.org_id = ? AND t.posted_on >= ? AND t.posted_on < ?`, orgID.String(), from.String(), to.String())
}

// lines is the parts matching a condition, with their transaction, account
// and category.
func (s *Store) lines(ctx context.Context, where string, args ...any) ([]ledgerbus.ProjectLine, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, s.transaction_id, s.position, s.amount, s.category_id, s.project_id, s.memo, s.rule_id,
       t.posted_on, t.description, a.id, a.name, a.currency, coalesce(c.name, ''), coalesce(c.kind, '')
FROM splits s
JOIN transactions t ON t.id = s.transaction_id
JOIN accounts a ON a.id = t.account_id
LEFT JOIN categories c ON c.id = s.category_id
WHERE `+where+`
ORDER BY t.posted_on, t.rowid, s.position`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the parts: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.ProjectLine

	for rows.Next() {
		var (
			l               ledgerbus.ProjectLine
			posted, account string
			kind            string
		)

		l.Split, err = scanSplit(rows, &posted, &l.Description, &account, &l.AccountName, &l.Currency, &l.CategoryName, &kind)
		if err != nil {
			return nil, err
		}

		var e1, e2 error
		l.CategoryKind = categorybus.Kind(kind)
		l.PostedOn, e1 = types.ParseDate(posted)
		l.AccountID, e2 = types.ParseID(account)

		if err := errors.Join(e1, e2); err != nil {
			return nil, fmt.Errorf("a part's line is unreadable: %w", err)
		}

		out = append(out, l)
	}

	return out, rows.Err()
}

func nullID(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
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

// --- reconciliations ----------------------------------------------------------

// Reconcile stores a reconciliation unless the statement has one.
func (s *Store) Reconcile(ctx context.Context, r ledgerbus.Reconciliation, ev eventbus.Event) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT INTO reconciliations (statement_id, account_id, period_start, period_end, note, reconciled_by, reconciled_at)
VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (statement_id) DO NOTHING`,
		r.StatementID.String(), r.AccountID.String(), r.Start.String(), r.End.String(), r.Note, r.By.String(), r.At.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("reconciling: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("saving a change: %w", err)
	}

	return true, nil
}

// Reopen deletes a statement's reconciliation.
func (s *Store) Reopen(ctx context.Context, statementID types.ID, ev eventbus.Event) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM reconciliations WHERE statement_id = ?`, statementID.String())
	if err != nil {
		return false, fmt.Errorf("reopening: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("saving a change: %w", err)
	}

	return true, nil
}

// Reconciliations is an account's, by the start of their periods.
func (s *Store) Reconciliations(ctx context.Context, account types.ID) ([]ledgerbus.Reconciliation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT statement_id, account_id, period_start, period_end, note, reconciled_by, reconciled_at
FROM reconciliations WHERE account_id = ? ORDER BY period_start, statement_id`, account.String())
	if err != nil {
		return nil, fmt.Errorf("reading the reconciliations: %w", err)
	}
	defer rows.Close()

	var out []ledgerbus.Reconciliation

	for rows.Next() {
		var (
			st, acc, start, end, note, by string
			at                            int64
		)

		if err := rows.Scan(&st, &acc, &start, &end, &note, &by, &at); err != nil {
			return nil, fmt.Errorf("reading a reconciliation: %w", err)
		}

		r, err := reconciliation(st, acc, start, end, note, by, at)
		if err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	return out, rows.Err()
}

func reconciliation(statement, account, start, end, note, by string, at int64) (ledgerbus.Reconciliation, error) {
	r := ledgerbus.Reconciliation{Note: note, At: time.UnixMilli(at).UTC()}

	var e [5]error
	r.StatementID, e[0] = types.ParseID(statement)
	r.AccountID, e[1] = types.ParseID(account)
	r.By, e[2] = types.ParseID(by)
	r.Start, e[3] = types.ParseDate(start)
	r.End, e[4] = types.ParseDate(end)

	if err := errors.Join(e[:]...); err != nil {
		return ledgerbus.Reconciliation{}, fmt.Errorf("a stored reconciliation is unreadable: %w", err)
	}

	return r, nil
}
