package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// initPayees creates the lists of payees (ledgerbus/payees.go,
// docs/payees.md), gives transactions their payee, and gives every
// transaction that already says whom it was paid to the payee written so.
//
// A list is owned as a category list is: two owner columns, so that each
// is a foreign key, exactly one set. Names are unique in a list whatever
// their case, archived or not, as categories' are -- except a payee joined
// into another, which keeps its name for when it is split back out and
// must not stop the payee it is in from taking that name: "Hilltop
// Plumbing LLC" joined into "Hilltop Plumbing", which is then renamed
// "Hilltop Plumbing LLC".
//
// payee_names is every way each payee has been written. The owner is
// copied onto it -- the organization's ID, or the account's -- because the
// question asked of it on every check read is "which payee of this list
// is written so", and that is its key rather than a join. NOCASE on the
// name makes the key as blind to case as the lookup is meant to be.
//
// The marks a person sets on a payee -- reported, details_on_file, the
// note -- and via, the key a program last changed it through, are in the
// CREATE rather than later columns, since the table arrived with them
// planned (docs/payees.md, 3 and 5); the pages that set them came after.
//
// payee_not_same is the suggestions a person said are two people: a pair
// of payees, the smaller ID first, so that it is one row whichever way it
// was said.
func initPayees(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS payees (
    id              TEXT    PRIMARY KEY,
    org_id          TEXT    REFERENCES orgs (id),
    account_id      TEXT    REFERENCES accounts (id),
    name            TEXT    NOT NULL,
    reported        INTEGER NOT NULL DEFAULT 0 CHECK (reported IN (0, 1)),
    details_on_file INTEGER NOT NULL DEFAULT 0 CHECK (details_on_file IN (0, 1)),
    note            TEXT    NOT NULL DEFAULT '',
    via             TEXT    NOT NULL DEFAULT '',
    joined_into     TEXT    REFERENCES payees (id),
    created_by      TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    archived_at     INTEGER,
    CHECK ((org_id IS NULL) <> (account_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS payees_name ON payees (coalesce(org_id, account_id), name COLLATE NOCASE) WHERE joined_into IS NULL;

CREATE TABLE IF NOT EXISTS payee_names (
    owner    TEXT NOT NULL,
    name     TEXT NOT NULL COLLATE NOCASE,
    payee_id TEXT NOT NULL REFERENCES payees (id),
    PRIMARY KEY (owner, name)
) STRICT;

CREATE INDEX IF NOT EXISTS payee_names_payee ON payee_names (payee_id);

CREATE TABLE IF NOT EXISTS payee_not_same (
    a       TEXT    NOT NULL REFERENCES payees (id),
    b       TEXT    NOT NULL REFERENCES payees (id),
    said_by TEXT    NOT NULL,
    said_at INTEGER NOT NULL,
    PRIMARY KEY (a, b),
    CHECK (a < b)
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the payees tables: %w", err)
	}

	// The payee a transaction is, a later column on transactions, and its
	// index after it, since an index on a later column built in a CREATE
	// block fails on every database from before the column.
	if err := sqldb.AddColumn(ctx, db, "transactions", "payee_id", "TEXT REFERENCES payees (id)"); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS transactions_payee ON transactions (payee_id) WHERE payee_id IS NOT NULL`); err != nil {
		return fmt.Errorf("creating the payee index: %w", err)
	}

	// The key a program set a transaction's payee through, and the key it
	// brought a spelling into a payee through (docs/payees.md, 5), until a
	// person saves the transaction's payee or the payee on the site: later
	// columns, from the pull request that let Claude join and set payees.
	if err := sqldb.AddColumn(ctx, db, "transactions", "payee_via", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	if err := sqldb.AddColumn(ctx, db, "payee_names", "via", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	return givePayees(ctx, db, time.Now())
}

// givePayees gives every transaction that says whom it was paid to, and has
// no payee yet, the payee of its list written so -- made, named as written,
// when the list has none. It joins no two: "Hilltop Plumbing" and "HILLTOP
// PLUMBING LLC" become two payees, for a person to join. Before payees,
// every transaction with a payee's text was a check whose image was read;
// after, SetPayee gives the payee with the text, so this finds nothing to
// do after the first start, and runs every start so that it cannot be
// forgotten. The person who made the account is who made its payees.
func givePayees(ctx context.Context, db *sql.DB, now time.Time) error {
	// Asked first, alone, so that a start with nothing to do reads one
	// index and nothing else.
	var todo bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM transactions WHERE payee <> '' AND payee_id IS NULL)`).Scan(&todo); err != nil {
		return fmt.Errorf("asking which transactions have no payee: %w", err)
	}

	if !todo {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to give transactions their payees: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT a.org_id, a.id, a.created_by, t.payee
FROM transactions t JOIN accounts a ON a.id = t.account_id
WHERE t.payee <> '' AND t.payee_id IS NULL
ORDER BY t.rowid`)
	if err != nil {
		return fmt.Errorf("reading the payees written so far: %w", err)
	}

	type written struct {
		org            sql.NullString
		account, by, n string
	}

	var pending []written

	for rows.Next() {
		var w written
		if err := rows.Scan(&w.org, &w.account, &w.by, &w.n); err != nil {
			rows.Close()

			return fmt.Errorf("reading the payees written so far: %w", err)
		}

		pending = append(pending, w)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading the payees written so far: %w", err)
	}

	if len(pending) == 0 {
		return nil
	}

	for _, w := range pending {
		owner := types.Scope{Kind: types.ScopeAccount}
		owner.ID, _ = types.ParseID(w.account)

		if w.org.Valid {
			owner.Kind = types.ScopeOrg
			owner.ID, _ = types.ParseID(w.org.String)
		}

		var found string

		err := tx.QueryRowContext(ctx, `SELECT payee_id FROM payee_names WHERE owner = ? AND name = ?`, owner.ID.String(), w.n).Scan(&found)
		if err == nil {
			continue
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("looking up a payee: %w", err)
		}

		by, _ := types.ParseID(w.by)
		if err := insertPayee(ctx, tx, ledgerbus.Payee{ID: types.NewID(), Owner: owner, Name: w.n, CreatedBy: by, CreatedAt: now}); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE transactions SET payee_id = (
    SELECT n.payee_id FROM payee_names n JOIN accounts a ON a.id = transactions.account_id
    WHERE n.owner = coalesce(a.org_id, a.id) AND n.name = transactions.payee)
WHERE payee <> '' AND payee_id IS NULL`); err != nil {
		return fmt.Errorf("giving transactions their payees: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("giving transactions their payees: %w", err)
	}

	return nil
}

func payeeOwner(s types.Scope) (org, account any) {
	if s.Kind == types.ScopeOrg {
		return s.ID.String(), nil
	}

	return nil, s.ID.String()
}

// insertPayee stores a payee, with the key it was made through, and its
// name as its first spelling. The spelling carries no mark: it was not
// brought in from another payee, and the payee's own mark says who made it.
func insertPayee(ctx context.Context, tx *sql.Tx, p ledgerbus.Payee) error {
	org, account := payeeOwner(p.Owner)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO payees (id, org_id, account_id, name, via, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID.String(), org, account, p.Name, p.Via, p.CreatedBy.String(), p.CreatedAt.UnixMilli()); err != nil {
		return payeeErr(err, "storing a payee")
	}

	return addSpelling(ctx, tx, p.Owner, p.ID, p.Name)
}

// markPayees writes the key a change to some payees came through onto
// them, or, for a change a person made on the site (via ""), takes every
// mark off them and off their spellings: a person who changed a payee has
// looked at it.
func markPayees(ctx context.Context, tx *sql.Tx, via string, ids ...types.ID) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE payees SET via = ? WHERE id = ?`, via, id.String()); err != nil {
			return fmt.Errorf("marking a payee: %w", err)
		}

		if via != "" {
			continue
		}

		if _, err := tx.ExecContext(ctx, `UPDATE payee_names SET via = '' WHERE payee_id = ? AND via <> ''`, id.String()); err != nil {
			return fmt.Errorf("unmarking a payee's spellings: %w", err)
		}
	}

	return nil
}

// addSpelling makes a way of writing a payee its, unless it is already.
func addSpelling(ctx context.Context, tx *sql.Tx, owner types.Scope, id types.ID, name string) error {
	var have string

	err := tx.QueryRowContext(ctx, `SELECT payee_id FROM payee_names WHERE owner = ? AND name = ?`, owner.ID.String(), name).Scan(&have)

	switch {
	case err == nil && have == id.String():
		return nil
	case err == nil:
		return ledgerbus.ErrPayeeTaken
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("looking up a payee's spelling: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO payee_names (owner, name, payee_id) VALUES (?, ?, ?)`, owner.ID.String(), name, id.String()); err != nil {
		return payeeErr(err, "storing a payee's spelling")
	}

	return nil
}

// payeeErr is ErrPayeeTaken for a name or spelling another payee has, and
// the error said in words otherwise.
func payeeErr(err error, doing string) error {
	if sqldb.IsUniqueViolation(err) {
		return ledgerbus.ErrPayeeTaken
	}

	return fmt.Errorf("%s: %w", doing, err)
}

// inPayeeTx runs fn and then the events, all or nothing.
func (s *Store) inPayeeTx(ctx context.Context, fn func(tx *sql.Tx) error, events ...eventbus.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change to payees: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	for _, ev := range events {
		if err := eventdb.Insert(ctx, tx, ev); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving a change to payees: %w", err)
	}

	return nil
}

// CreatePayee stores a payee, its name as its spelling, and ev.
func (s *Store) CreatePayee(ctx context.Context, p ledgerbus.Payee, ev eventbus.Event) error {
	return s.inPayeeTx(ctx, func(tx *sql.Tx) error { return insertPayee(ctx, tx, p) }, ev)
}

// UpdatePayee writes a payee's name, archived time and mark (markPayees),
// adds a spelling unless it is "", and ev when there is one: a person who
// saves a payee unchanged takes a program's marks off it and tells the
// history nothing.
func (s *Store) UpdatePayee(ctx context.Context, p ledgerbus.Payee, spelling string, ev *eventbus.Event) error {
	var archived any
	if p.Archived() {
		archived = p.ArchivedAt.UnixMilli()
	}

	var events []eventbus.Event
	if ev != nil {
		events = append(events, *ev)
	}

	return s.inPayeeTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE payees SET name = ?, archived_at = ? WHERE id = ?`, p.Name, archived, p.ID.String()); err != nil {
			return payeeErr(err, "changing a payee")
		}

		if err := markPayees(ctx, tx, p.Via, p.ID); err != nil {
			return err
		}

		if spelling == "" {
			return nil
		}

		return addSpelling(ctx, tx, p.Owner, p.ID, spelling)
	}, events...)
}

// SetPayee writes a transaction's payee text, the payee it is and the key
// it was written through, making the payee first when made says to, with
// the events there are: none when a person saves on the site what a
// program wrote, which only takes its mark off.
func (s *Store) SetPayee(ctx context.Context, id types.ID, payee, via string, p ledgerbus.Payee, made, ev *eventbus.Event) error {
	var events []eventbus.Event

	for _, e := range []*eventbus.Event{made, ev} {
		if e != nil {
			events = append(events, *e)
		}
	}

	return s.inPayeeTx(ctx, func(tx *sql.Tx) error {
		if made != nil {
			if err := insertPayee(ctx, tx, p); err != nil {
				return err
			}
		}

		var payeeID any
		if !p.ID.Zero() {
			payeeID = p.ID.String()
		}

		res, err := tx.ExecContext(ctx, `UPDATE transactions SET payee = ?, payee_id = ?, payee_via = ? WHERE id = ?`, payee, payeeID, via, id.String())
		if err != nil {
			return fmt.Errorf("writing a payee: %w", err)
		}

		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return cmpErr(err, ledgerbus.ErrNotFound)
		}

		return nil
	}, events...)
}

// JoinPayees moves each of from's spellings and transactions to into and
// marks it joined into it, archived, with the events. Through a key (via),
// into and the spellings it is given are marked with it; by a person, into
// and all its spellings are unmarked (markPayees).
func (s *Store) JoinPayees(ctx context.Context, now time.Time, into types.ID, from []types.ID, via string, events []eventbus.Event) error {
	return s.inPayeeTx(ctx, func(tx *sql.Tx) error {
		for _, id := range from {
			for _, q := range []string{
				`UPDATE payee_names SET payee_id = ?1, via = ?4 WHERE payee_id = ?2`,
				`UPDATE transactions SET payee_id = ?1 WHERE payee_id = ?2`,
				`UPDATE payees SET joined_into = ?1, archived_at = coalesce(archived_at, ?3) WHERE id = ?2 AND joined_into IS NULL`,
			} {
				if _, err := tx.ExecContext(ctx, q, into.String(), id.String(), now.UnixMilli(), via); err != nil {
					return fmt.Errorf("joining payees: %w", err)
				}
			}
		}

		return markPayees(ctx, tx, via, into)
	}, events...)
}

// SplitPayee moves a spelling of from, and from's transactions written so,
// to into: stored first if it is new, brought back if it was joined.
// Through a key (into.Via) both payees and the spelling are marked with
// it; by a person both are unmarked (markPayees).
func (s *Store) SplitPayee(ctx context.Context, from types.ID, spelling string, into ledgerbus.Payee, ev eventbus.Event) (int, error) {
	var moved int64

	err := s.inPayeeTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM payees WHERE id = ?`, into.ID.String()).Scan(&exists); err != nil {
			return fmt.Errorf("splitting a payee: %w", err)
		}

		if exists == 0 {
			org, account := payeeOwner(into.Owner)

			if _, err := tx.ExecContext(ctx, `INSERT INTO payees (id, org_id, account_id, name, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				into.ID.String(), org, account, into.Name, into.CreatedBy.String(), into.CreatedAt.UnixMilli()); err != nil {
				return payeeErr(err, "splitting a payee")
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE payees SET joined_into = NULL, archived_at = NULL WHERE id = ? AND joined_into IS NOT NULL`, into.ID.String()); err != nil {
			return payeeErr(err, "splitting a payee")
		}

		res, err := tx.ExecContext(ctx, `UPDATE payee_names SET payee_id = ?, via = ? WHERE owner = ? AND name = ? AND payee_id = ?`,
			into.ID.String(), into.Via, into.Owner.ID.String(), spelling, from.String())
		if err != nil {
			return payeeErr(err, "splitting a payee")
		}

		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return cmpErr(err, ledgerbus.ErrNotFound)
		}

		res, err = tx.ExecContext(ctx, `UPDATE transactions SET payee_id = ? WHERE payee_id = ? AND payee = ? COLLATE NOCASE`, into.ID.String(), from.String(), spelling)
		if err != nil {
			return fmt.Errorf("splitting a payee: %w", err)
		}

		if moved, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("splitting a payee: %w", err)
		}

		return markPayees(ctx, tx, into.Via, from, into.ID)
	}, ev)

	return int(moved), err
}

// NotSame keeps a pair of payees said to be two, once, with ev.
func (s *Store) NotSame(ctx context.Context, pair [2]types.ID, by types.ID, at time.Time, ev eventbus.Event) error {
	return s.inPayeeTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO payee_not_same (a, b, said_by, said_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			pair[0].String(), pair[1].String(), by.String(), at.UnixMilli()); err != nil {
			return fmt.Errorf("keeping that two payees are not the same: %w", err)
		}

		return nil
	}, ev)
}

// PayeesNotSame is the pairs of an owner's list said to be two payees.
func (s *Store) PayeesNotSame(ctx context.Context, owner types.Scope) (map[[2]types.ID]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT n.a, n.b FROM payee_not_same n JOIN payees p ON p.id = n.a
WHERE coalesce(p.org_id, p.account_id) = ?`, owner.ID.String())
	if err != nil {
		return nil, fmt.Errorf("reading the payees said to be different: %w", err)
	}
	defer rows.Close()

	out := map[[2]types.ID]bool{}

	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, fmt.Errorf("reading the payees said to be different: %w", err)
		}

		var pair [2]types.ID

		var e [2]error
		pair[0], e[0] = types.ParseID(a)
		pair[1], e[1] = types.ParseID(b)

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a stored pair of payees is unreadable: %w", err)
		}

		out[pair] = true
	}

	return out, rows.Err()
}

const payeeColumns = `p.id, p.org_id, p.account_id, p.name, p.via, p.reported, p.details_on_file, p.note, coalesce(p.joined_into, ''), p.created_by, p.created_at, p.archived_at,
    (SELECT count(*) FROM transactions t WHERE t.payee_id = p.id)`

// Payees is an owner's list, not joined ones, by name, archived ones last.
func (s *Store) Payees(ctx context.Context, owner types.Scope) ([]ledgerbus.Payee, error) {
	column := "p.account_id"
	if owner.Kind == types.ScopeOrg {
		column = "p.org_id"
	}

	return s.payees(ctx, `SELECT `+payeeColumns+` FROM payees p WHERE `+column+` = ? AND p.joined_into IS NULL
ORDER BY p.archived_at IS NOT NULL, p.name COLLATE NOCASE`, owner.ID.String())
}

// PayeeByID is one payee, joined or not.
func (s *Store) PayeeByID(ctx context.Context, id types.ID) (ledgerbus.Payee, error) {
	return s.onePayee(ctx, `SELECT `+payeeColumns+` FROM payees p WHERE p.id = ?`, id.String())
}

// PayeeNamed is the payee of an owner's list named so: the one on the
// list, or else the latest joined into another.
func (s *Store) PayeeNamed(ctx context.Context, owner types.Scope, name string) (ledgerbus.Payee, error) {
	return s.onePayee(ctx, `SELECT `+payeeColumns+` FROM payees p WHERE coalesce(p.org_id, p.account_id) = ? AND p.name = ? COLLATE NOCASE
ORDER BY p.joined_into IS NOT NULL, p.created_at DESC LIMIT 1`, owner.ID.String(), name)
}

// PayeeSpelled is the payee of an owner's list written so.
func (s *Store) PayeeSpelled(ctx context.Context, owner types.Scope, spelling string) (ledgerbus.Payee, error) {
	return s.onePayee(ctx, `SELECT `+payeeColumns+` FROM payee_names n JOIN payees p ON p.id = n.payee_id WHERE n.owner = ? AND n.name = ?`, owner.ID.String(), spelling)
}

// PayeeTransactions is a payee's transactions, newest first, each with its
// account's name and currency.
func (s *Store) PayeeTransactions(ctx context.Context, id types.ID, limit int) ([]ledgerbus.PayeePayment, error) {
	txs, err := s.transactions(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE payee_id = ? ORDER BY posted_on DESC, rowid DESC LIMIT ?`, id.String(), limit)
	if err != nil || len(txs) == 0 {
		return nil, err
	}

	return s.withAccounts(ctx, txs)
}

// YearPayments is the transactions of a list's payees, or of one payee
// when payee is not zero, whose day -- the day written on a check where it
// is known, the day posted otherwise -- falls between from and to, both
// included: with their parts, oldest first, each with its account's name
// and currency.
func (s *Store) YearPayments(ctx context.Context, owner types.Scope, payee types.ID, from, to types.Date) ([]ledgerbus.PayeePayment, error) {
	where := `payee_id IN (SELECT id FROM payees WHERE coalesce(org_id, account_id) = ?)`
	args := []any{owner.ID.String()}

	if !payee.Zero() {
		where, args = `payee_id = ?`, []any{payee.String()}
	}

	// An account for reference's payments are not the organization's to
	// report, whoever they were paid to (docs/plan.md, "Whose books").
	where += ` AND account_id NOT IN (SELECT id FROM accounts WHERE reference = 1)`

	txs, err := s.withSplits(ctx, where+` AND coalesce(nullif(check_written_on, ''), posted_on) BETWEEN ? AND ?`,
		`coalesce(nullif(check_written_on, ''), posted_on), rowid`, append(args, from.String(), to.String())...)
	if err != nil || len(txs) == 0 {
		return nil, err
	}

	return s.withAccounts(ctx, txs)
}

// PayeesByID is the payees with these ids, joined or not, in no order.
func (s *Store) PayeesByID(ctx context.Context, ids []types.ID) ([]ledgerbus.Payee, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}

	return s.payees(ctx, `SELECT `+payeeColumns+` FROM payees p WHERE p.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
}

// SetPayeeMarks writes the marks a person sets on a payee and its key's
// mark (markPayees), with the events.
func (s *Store) SetPayeeMarks(ctx context.Context, p ledgerbus.Payee, events []eventbus.Event) error {
	return s.inPayeeTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE payees SET reported = ?, details_on_file = ?, note = ? WHERE id = ?`,
			p.Reported, p.DetailsOnFile, p.Note, p.ID.String()); err != nil {
			return fmt.Errorf("marking a payee: %w", err)
		}

		return markPayees(ctx, tx, p.Via, p.ID)
	}, events...)
}

// withAccounts is payments with the name and currency of the account each
// was made from.
func (s *Store) withAccounts(ctx context.Context, txs []ledgerbus.Transaction) ([]ledgerbus.PayeePayment, error) {
	var ids []any

	for _, t := range txs {
		if !slices.Contains(ids, any(t.AccountID.String())) {
			ids = append(ids, t.AccountID.String())
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, name, currency FROM accounts WHERE id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...)
	if err != nil {
		return nil, fmt.Errorf("reading the accounts of a payee's payments: %w", err)
	}
	defer rows.Close()

	type named struct{ name, currency string }

	accounts := map[string]named{}

	for rows.Next() {
		var id string

		var n named
		if err := rows.Scan(&id, &n.name, &n.currency); err != nil {
			return nil, fmt.Errorf("reading the accounts of a payee's payments: %w", err)
		}

		accounts[id] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the accounts of a payee's payments: %w", err)
	}

	out := make([]ledgerbus.PayeePayment, 0, len(txs))
	for _, t := range txs {
		n := accounts[t.AccountID.String()]
		out = append(out, ledgerbus.PayeePayment{Transaction: t, AccountName: n.name, Currency: n.currency})
	}

	return out, nil
}

func (s *Store) onePayee(ctx context.Context, q string, args ...any) (ledgerbus.Payee, error) {
	list, err := s.payees(ctx, q, args...)
	if err != nil {
		return ledgerbus.Payee{}, err
	}

	if len(list) == 0 {
		return ledgerbus.Payee{}, ledgerbus.ErrNotFound
	}

	return list[0], nil
}

// payees reads payees and then their spellings.
func (s *Store) payees(ctx context.Context, q string, args ...any) ([]ledgerbus.Payee, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("reading payees: %w", err)
	}
	defer rows.Close()

	var (
		out []ledgerbus.Payee
		ids []any
	)

	for rows.Next() {
		var (
			p              ledgerbus.Payee
			id, joined, by string
			org, account   sql.NullString
			at             int64
			archived       sql.NullInt64
		)

		if err := rows.Scan(&id, &org, &account, &p.Name, &p.Via, &p.Reported, &p.DetailsOnFile, &p.Note, &joined, &by, &at, &archived, &p.Payments); err != nil {
			return nil, fmt.Errorf("reading a payee: %w", err)
		}

		var e [4]error
		p.ID, e[0] = types.ParseID(id)
		p.CreatedBy, e[1] = types.ParseID(by)

		if org.Valid {
			p.Owner.Kind = types.ScopeOrg
			p.Owner.ID, e[2] = types.ParseID(org.String)
		} else {
			p.Owner.Kind = types.ScopeAccount
			p.Owner.ID, e[2] = types.ParseID(account.String)
		}

		if joined != "" {
			p.JoinedInto, e[3] = types.ParseID(joined)
		}

		if err := errors.Join(e[:]...); err != nil {
			return nil, fmt.Errorf("a stored payee is unreadable: %w", err)
		}

		p.CreatedAt = time.UnixMilli(at).UTC()
		if archived.Valid {
			p.ArchivedAt = time.UnixMilli(archived.Int64).UTC()
		}

		out = append(out, p)
		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading payees: %w", err)
	}

	if len(out) == 0 {
		return nil, nil
	}

	spellings, err := s.spellings(ctx, ids)
	if err != nil {
		return nil, err
	}

	for i := range out {
		got := spellings[out[i].ID]
		out[i].Spellings, out[i].BroughtVia = got.names, got.brought
	}

	return out, nil
}

// spelled is a payee's spellings, and those a program brought in.
type spelled struct{ names, brought []string }

// spellings is the ways each of some payees has been written, in the
// order of the alphabet, with those a key brought in apart.
func (s *Store) spellings(ctx context.Context, ids []any) (map[types.ID]spelled, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payee_id, name, via FROM payee_names WHERE payee_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY name`, ids...)
	if err != nil {
		return nil, fmt.Errorf("reading payees' spellings: %w", err)
	}
	defer rows.Close()

	out := map[types.ID]spelled{}

	for rows.Next() {
		var id, name, via string
		if err := rows.Scan(&id, &name, &via); err != nil {
			return nil, fmt.Errorf("reading payees' spellings: %w", err)
		}

		pid, err := types.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("a stored spelling is unreadable: %w", err)
		}

		got := out[pid]
		got.names = append(got.names, name)

		if via != "" {
			got.brought = append(got.brought, name)
		}

		out[pid] = got
	}

	return out, rows.Err()
}
