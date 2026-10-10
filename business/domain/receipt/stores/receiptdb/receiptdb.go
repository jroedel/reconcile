// Package receiptdb stores receipts in SQLite.
package receiptdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/business/types/money"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of receiptbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ receiptbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"receipts":      {"id", "account_id", "project_id", "uploaded_by", "spent_on", "amount", "merchant", "note", "created_at", "removed_at", "check_number", "check_image"},
	"receipt_files": {"receipt_id", "position", "file_id"},
	"receipt_links": {"receipt_id", "transaction_id", "linked_by", "linked_at"},
}

// Init creates the tables. After ledgerdb and filedb, which they reference.
//
// The inbox is two columns, as a category's owner is, so that each is a
// foreign key; exactly one is set. A link goes with its transaction (ON
// DELETE CASCADE): removing a statement puts its receipts back in their
// inboxes, waiting, rather than losing them.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS receipts (
    id          TEXT    PRIMARY KEY,
    account_id  TEXT    REFERENCES accounts (id),
    project_id  TEXT    REFERENCES projects (id),
    uploaded_by TEXT    NOT NULL,
    spent_on    TEXT    NOT NULL DEFAULT '',
    amount      INTEGER,
    merchant    TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    removed_at  INTEGER,
    CHECK ((account_id IS NULL) <> (project_id IS NULL))
) STRICT;

CREATE INDEX IF NOT EXISTS receipts_account ON receipts (account_id, created_at) WHERE account_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS receipts_project ON receipts (project_id, created_at) WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS receipt_files (
    receipt_id TEXT    NOT NULL REFERENCES receipts (id),
    position   INTEGER NOT NULL,
    file_id    TEXT    NOT NULL REFERENCES files (id),
    PRIMARY KEY (receipt_id, position)
) STRICT;

CREATE TABLE IF NOT EXISTS receipt_links (
    receipt_id     TEXT    NOT NULL REFERENCES receipts (id),
    transaction_id TEXT    NOT NULL REFERENCES transactions (id) ON DELETE CASCADE,
    linked_by      TEXT    NOT NULL,
    linked_at      INTEGER NOT NULL,
    PRIMARY KEY (receipt_id, transaction_id)
) STRICT;

CREATE INDEX IF NOT EXISTS receipt_links_transaction ON receipt_links (transaction_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the receipts tables: %w", err)
	}

	// The number of the check a receipt is the image of (receiptbus,
	// AddChecks), a later column beside the CREATE; empty for every other
	// receipt.
	if err := sqldb.AddColumn(ctx, db, "receipts", "check_number", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// Whether a receipt is the image of a check, whose number may not be
	// read yet (docs/phone.md, 1): a later column again. Until it came,
	// a check image was added only once matched, with its number, so
	// every receipt with a number is one. Setting that each time is
	// idempotent and touches nothing once done.
	if err := sqldb.AddColumn(ctx, db, "receipts", "check_image", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `UPDATE receipts SET check_image = 1 WHERE check_number <> '' AND check_image = 0`); err != nil {
		return fmt.Errorf("marking the check images: %w", err)
	}

	return nil
}

func home(s types.Scope) (account, project any) {
	if s.Kind == types.ScopeAccount {
		return s.ID.String(), nil
	}

	return nil, s.ID.String()
}

func amountOf(d receiptbus.Details) any {
	if !d.HasAmount {
		return nil
	}

	return int64(d.Amount)
}

func (s *Store) inTx(ctx context.Context, ev *eventbus.Event, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	if ev != nil {
		if err := eventdb.Insert(ctx, tx, *ev); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving a change: %w", err)
	}

	return nil
}

// Create stores receipts, their files and links, and the history.
func (s *Store) Create(ctx context.Context, receipts []receiptbus.Receipt, ev eventbus.Event) error {
	return s.inTx(ctx, &ev, func(tx *sql.Tx) error {
		for _, r := range receipts {
			account, project := home(r.Home)

			if _, err := tx.ExecContext(ctx, `
INSERT INTO receipts (id, account_id, project_id, uploaded_by, spent_on, amount, merchant, note, created_at, check_number, check_image)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.ID.String(), account, project, r.UploadedBy.String(), r.SpentOn.String(), amountOf(r.Details),
				r.Merchant, r.Note, r.CreatedAt.UnixMilli(), r.Check, r.CheckImage); err != nil {
				return fmt.Errorf("storing a receipt: %w", err)
			}

			for i, f := range r.Files {
				if _, err := tx.ExecContext(ctx, `INSERT INTO receipt_files (receipt_id, position, file_id) VALUES (?, ?, ?)`,
					r.ID.String(), i, f.ID.String()); err != nil {
					return fmt.Errorf("storing a receipt's file: %w", err)
				}
			}

			for _, l := range r.Links {
				if err := link(ctx, tx, r.ID, l); err != nil {
					return err
				}
			}
		}

		return nil
	})
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// link is one statement that is its own claim: a second attach of the same
// pair changes nothing.
func link(ctx context.Context, db execer, receiptID types.ID, l receiptbus.Link) error {
	if _, err := db.ExecContext(ctx, `
INSERT INTO receipt_links (receipt_id, transaction_id, linked_by, linked_at) VALUES (?, ?, ?, ?)
ON CONFLICT (receipt_id, transaction_id) DO NOTHING`,
		receiptID.String(), l.TransactionID.String(), l.LinkedBy.String(), l.LinkedAt.UnixMilli()); err != nil {
		if sqldb.IsForeignKeyViolation(err) {
			return receiptbus.ErrNotFound
		}

		return fmt.Errorf("attaching a receipt: %w", err)
	}

	return nil
}

// Link attaches a receipt to a transaction.
func (s *Store) Link(ctx context.Context, receiptID types.ID, l receiptbus.Link) error {
	return link(ctx, s.db, receiptID, l)
}

// Unlink takes it off.
func (s *Store) Unlink(ctx context.Context, receiptID, transactionID types.ID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM receipt_links WHERE receipt_id = ? AND transaction_id = ?`,
		receiptID.String(), transactionID.String()); err != nil {
		return fmt.Errorf("detaching a receipt: %w", err)
	}

	return nil
}

// Update writes the details and the removed time.
func (s *Store) Update(ctx context.Context, r receiptbus.Receipt, ev *eventbus.Event) error {
	var removed any
	if r.Removed() {
		removed = r.RemovedAt.UnixMilli()
	}

	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE receipts SET spent_on = ?, amount = ?, merchant = ?, note = ?, removed_at = ? WHERE id = ?`,
			r.SpentOn.String(), amountOf(r.Details), r.Merchant, r.Note, removed, r.ID.String()); err != nil {
			return fmt.Errorf("changing a receipt: %w", err)
		}

		return nil
	})
}

// SaveCheck writes a check image's number and details, and its links.
func (s *Store) SaveCheck(ctx context.Context, r receiptbus.Receipt) error {
	return s.inTx(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE receipts SET check_number = ?, spent_on = ?, amount = ?, merchant = ?, note = ? WHERE id = ? AND check_image = 1`,
			r.Check, r.SpentOn.String(), amountOf(r.Details), r.Merchant, r.Note, r.ID.String()); err != nil {
			return fmt.Errorf("numbering a check's image: %w", err)
		}

		for _, l := range r.Links {
			if err := link(ctx, tx, r.ID, l); err != nil {
				return err
			}
		}

		return nil
	})
}

// --- reading ------------------------------------------------------------------

// ByID finds one.
func (s *Store) ByID(ctx context.Context, id types.ID) (receiptbus.Receipt, error) {
	rs, err := s.load(ctx, `r.id = ?`, id.String())
	if err != nil {
		return receiptbus.Receipt{}, err
	}

	if len(rs) == 0 {
		return receiptbus.Receipt{}, receiptbus.ErrNotFound
	}

	return rs[0], nil
}

// InHomes is the receipts in any of the inboxes, newest first.
func (s *Store) InHomes(ctx context.Context, homes []types.Scope, waitingOnly bool) ([]receiptbus.Receipt, error) {
	var accounts, projects []string

	for _, h := range homes {
		switch h.Kind {
		case types.ScopeAccount:
			accounts = append(accounts, h.ID.String())
		case types.ScopeProject:
			projects = append(projects, h.ID.String())
		}
	}

	if len(accounts)+len(projects) == 0 {
		return nil, nil
	}

	where := `(r.account_id IN (` + marks(len(accounts)) + `) OR r.project_id IN (` + marks(len(projects)) + `))`
	if waitingOnly {
		where += ` AND r.removed_at IS NULL AND NOT EXISTS (SELECT 1 FROM receipt_links l WHERE l.receipt_id = r.id)`
	}

	args := make([]any, 0, len(accounts)+len(projects))
	for _, id := range append(accounts, projects...) {
		args = append(args, id)
	}

	return s.load(ctx, where, args...)
}

// OnTransactions is the receipts attached to any of the transactions.
func (s *Store) OnTransactions(ctx context.Context, ids []types.ID) ([]receiptbus.Receipt, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}

	return s.load(ctx, `r.id IN (SELECT receipt_id FROM receipt_links WHERE transaction_id IN (`+marks(len(ids))+`))`, args...)
}

// HoldsContent reports whether a receipt in the inbox, not removed, has a
// file with these bytes.
func (s *Store) HoldsContent(ctx context.Context, inbox types.Scope, sha256 string) (bool, error) {
	account, project := home(inbox)

	var one int

	err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM receipts r
JOIN receipt_files rf ON rf.receipt_id = r.id
JOIN files f ON f.id = rf.file_id
WHERE (r.account_id = ? OR r.project_id = ?) AND r.removed_at IS NULL AND f.sha256 = ?
LIMIT 1`, account, project, sha256).Scan(&one)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("looking for a receipt's bytes: %w", err)
	}

	return true, nil
}

// marks is n placeholders; none is a list that matches nothing.
func marks(n int) string {
	if n == 0 {
		return "NULL"
	}

	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// load reads the receipts matching a condition, newest first, with their
// files and links: three queries whatever the count.
func (s *Store) load(ctx context.Context, where string, args ...any) ([]receiptbus.Receipt, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id, r.account_id, r.project_id, r.uploaded_by, r.spent_on, r.amount, r.merchant, r.note, r.created_at, r.removed_at, r.check_number, r.check_image
FROM receipts r WHERE `+where+` ORDER BY r.created_at DESC, r.rowid DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading receipts: %w", err)
	}

	var (
		out   []receiptbus.Receipt
		index = map[types.ID]int{}
	)

	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			rows.Close()

			return nil, err
		}

		index[r.ID] = len(out)
		out = append(out, r)
	}

	rows.Close()

	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}

	sub := `SELECT r.id FROM receipts r WHERE ` + where

	if err := s.loadFiles(ctx, out, index, sub, args); err != nil {
		return nil, err
	}

	return out, s.loadLinks(ctx, out, index, sub, args)
}

func (s *Store) loadFiles(ctx context.Context, out []receiptbus.Receipt, index map[types.ID]int, sub string, args []any) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT rf.receipt_id, f.id, f.sha256, f.size, f.content_type, f.name, f.uploaded_by, f.uploaded_at
FROM receipt_files rf JOIN files f ON f.id = rf.file_id
WHERE rf.receipt_id IN (`+sub+`) ORDER BY rf.receipt_id, rf.position`, args...)
	if err != nil {
		return fmt.Errorf("reading receipts' files: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			f               filebus.File
			receipt, id, by string
			at              int64
		)

		if err := rows.Scan(&receipt, &id, &f.SHA256, &f.Size, &f.ContentType, &f.Name, &by, &at); err != nil {
			return fmt.Errorf("reading receipts' files: %w", err)
		}

		var e [3]error
		var rid types.ID
		rid, e[0] = types.ParseID(receipt)
		f.ID, e[1] = types.ParseID(id)
		f.UploadedBy, e[2] = types.ParseID(by)

		if err := errors.Join(e[:]...); err != nil {
			return fmt.Errorf("a receipt's file is unreadable: %w", err)
		}

		f.UploadedAt = time.UnixMilli(at).UTC()

		if i, ok := index[rid]; ok {
			out[i].Files = append(out[i].Files, f)
		}
	}

	return rows.Err()
}

func (s *Store) loadLinks(ctx context.Context, out []receiptbus.Receipt, index map[types.ID]int, sub string, args []any) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT receipt_id, transaction_id, linked_by, linked_at FROM receipt_links
WHERE receipt_id IN (`+sub+`) ORDER BY linked_at, rowid`, args...)
	if err != nil {
		return fmt.Errorf("reading receipts' links: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			l              receiptbus.Link
			receipt, t, by string
			at             int64
		)

		if err := rows.Scan(&receipt, &t, &by, &at); err != nil {
			return fmt.Errorf("reading receipts' links: %w", err)
		}

		var e [3]error
		var rid types.ID
		rid, e[0] = types.ParseID(receipt)
		l.TransactionID, e[1] = types.ParseID(t)
		l.LinkedBy, e[2] = types.ParseID(by)

		if err := errors.Join(e[:]...); err != nil {
			return fmt.Errorf("a receipt's link is unreadable: %w", err)
		}

		l.LinkedAt = time.UnixMilli(at).UTC()

		if i, ok := index[rid]; ok {
			out[i].Links = append(out[i].Links, l)
		}
	}

	return rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (receiptbus.Receipt, error) {
	var (
		r                receiptbus.Receipt
		id, by, spent    string
		account, project sql.NullString
		amount, removed  sql.NullInt64
		at               int64
	)

	if err := row.Scan(&id, &account, &project, &by, &spent, &amount, &r.Merchant, &r.Note, &at, &removed, &r.Check, &r.CheckImage); err != nil {
		return r, fmt.Errorf("reading a receipt: %w", err)
	}

	var e [4]error
	r.ID, e[0] = types.ParseID(id)
	r.UploadedBy, e[1] = types.ParseID(by)

	if account.Valid {
		r.Home.Kind = types.ScopeAccount
		r.Home.ID, e[2] = types.ParseID(account.String)
	} else {
		r.Home.Kind = types.ScopeProject
		r.Home.ID, e[2] = types.ParseID(project.String)
	}

	if spent != "" {
		r.SpentOn, e[3] = types.ParseDate(spent)
	}

	if err := errors.Join(e[:]...); err != nil {
		return r, fmt.Errorf("a stored receipt is unreadable: %w", err)
	}

	r.Amount, r.HasAmount = money.Amount(amount.Int64), amount.Valid
	r.CreatedAt = time.UnixMilli(at).UTC()

	if removed.Valid {
		r.RemovedAt = time.UnixMilli(removed.Int64).UTC()
	}

	return r, nil
}
