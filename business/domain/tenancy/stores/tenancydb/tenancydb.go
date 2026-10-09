// Package tenancydb stores organizations, accounts, projects and grants in
// SQLite, and writes each change's history in the same transaction
// (eventdb.Insert).
package tenancydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/event/stores/eventdb"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// Store is the SQLite implementation of tenancybus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ tenancybus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"orgs":     {"id", "name", "created_by", "created_at", "archived_at", "fiscal_start"},
	"accounts": {"id", "org_id", "name", "kind", "last4", "currency", "opened_on", "created_by", "created_at", "archived_at"},
	"projects": {"id", "org_id", "name", "starts_on", "ends_on", "note", "created_by", "created_at", "archived_at"},
	"grants":   {"id", "scope_kind", "scope_id", "user_id", "email", "role", "granted_by", "created_at"},
}

// Init creates the tables. Idempotent, run at every startup, after userdb's:
// a grant references its user.
//
// No CHECK on kind, role or scope_kind. Each is a list that will grow, and a
// CHECK naming today's values would make adding one a table rebuild on every
// database that exists (CLAUDE.md, "When there is a database"); tenancybus
// writes only values it knows and reads back only values it can parse.
//
// The one CHECK is on grants: exactly one of user_id and email. That is not
// a list, and a row with both or neither is a grant nobody can reason about.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS orgs (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;

-- opened_on is a calendar day as YYYY-MM-DD, '' for none (types.Date).
CREATE TABLE IF NOT EXISTS accounts (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    name        TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    last4       TEXT    NOT NULL DEFAULT '',
    currency    TEXT    NOT NULL DEFAULT 'USD',
    opened_on   TEXT    NOT NULL DEFAULT '',
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS accounts_org_id ON accounts (org_id) WHERE org_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS projects (
    id          TEXT    PRIMARY KEY,
    org_id      TEXT    REFERENCES orgs (id),
    name        TEXT    NOT NULL,
    starts_on   TEXT    NOT NULL DEFAULT '',
    ends_on     TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS projects_org_id ON projects (org_id) WHERE org_id IS NOT NULL;

-- scope_id names a row in orgs, accounts or projects, by scope_kind, so it
-- cannot be a foreign key. Nothing is ever deleted from those tables --
-- archiving is a column -- so there is nothing for it to dangle from.
CREATE TABLE IF NOT EXISTS grants (
    id          TEXT    PRIMARY KEY,
    scope_kind  TEXT    NOT NULL,
    scope_id    TEXT    NOT NULL,
    user_id     TEXT    REFERENCES users (id) ON DELETE CASCADE,
    email       TEXT,
    role        TEXT    NOT NULL,
    granted_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    CHECK ((user_id IS NULL) <> (email IS NULL))
) STRICT;

-- One grant per person per scope, claimed or waiting. Change the role on
-- the one there is.
CREATE UNIQUE INDEX IF NOT EXISTS grants_user ON grants (scope_kind, scope_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS grants_email ON grants (scope_kind, scope_id, email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS grants_held ON grants (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS grants_waiting ON grants (email) WHERE email IS NOT NULL;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the organization tables: %w", err)
	}

	// The month an organization's budget year starts (docs/budgets.md), a
	// later column beside the CREATE: January for every organization made
	// before it, which is what they had.
	return sqldb.AddColumn(ctx, db, "orgs", "fiscal_start", "INTEGER NOT NULL DEFAULT 1")
}

// --- transactions -----------------------------------------------------------

// inTx runs fn in a transaction and writes ev in it, committing only if both
// succeed.
func (s *Store) inTx(ctx context.Context, ev eventbus.Event, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting a change: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	if err := eventdb.Insert(ctx, tx, ev); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving a change: %w", err)
	}

	return nil
}

// --- organizations ----------------------------------------------------------

const orgColumns = `id, name, created_by, created_at, archived_at, fiscal_start`

// CreateOrg inserts an organization and its owner's grant.
func (s *Store) CreateOrg(ctx context.Context, o tenancybus.Org, owner tenancybus.Grant, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO orgs (`+orgColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
			o.ID.String(), o.Name, o.CreatedBy.String(), ms(o.CreatedAt), nullMS(o.ArchivedAt), o.FiscalStart); err != nil {
			return fmt.Errorf("inserting the organization: %w", err)
		}

		return insertGrant(ctx, tx, owner)
	})
}

// UpdateOrg writes an organization's name, archived state and budget year.
func (s *Store) UpdateOrg(ctx context.Context, o tenancybus.Org, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE orgs SET name = ?, archived_at = ?, fiscal_start = ? WHERE id = ?`,
			o.Name, nullMS(o.ArchivedAt), o.FiscalStart, o.ID.String())
		if err != nil {
			return fmt.Errorf("updating the organization: %w", err)
		}

		return oneRow(res)
	})
}

// OrgByID finds one.
func (s *Store) OrgByID(ctx context.Context, id types.ID) (tenancybus.Org, error) {
	return scanOrg(s.db.QueryRowContext(ctx, `SELECT `+orgColumns+` FROM orgs WHERE id = ?`, id.String()))
}

// OrgsByID finds several, by name.
func (s *Store) OrgsByID(ctx context.Context, ids []types.ID) ([]tenancybus.Org, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	in, args := inList(ids)

	return queryAll(ctx, s.db, scanOrg, `SELECT `+orgColumns+` FROM orgs WHERE id IN (`+in+`) ORDER BY name COLLATE NOCASE, id`, args...)
}

// AllOrgs is every organization, by name.
func (s *Store) AllOrgs(ctx context.Context) ([]tenancybus.Org, error) {
	return queryAll(ctx, s.db, scanOrg, `SELECT `+orgColumns+` FROM orgs ORDER BY name COLLATE NOCASE, id`)
}

func scanOrg(row scanner) (tenancybus.Org, error) {
	var (
		o        tenancybus.Org
		id, by   string
		made     int64
		archived sql.NullInt64
	)

	if err := row.Scan(&id, &o.Name, &by, &made, &archived, &o.FiscalStart); err != nil {
		return tenancybus.Org{}, notFound(err, "the organization")
	}

	var e1, e2 error
	o.ID, e1 = types.ParseID(id)
	o.CreatedBy, e2 = types.ParseID(by)

	if err := errors.Join(e1, e2); err != nil {
		return tenancybus.Org{}, fmt.Errorf("a stored organization is unreadable: %w", err)
	}

	o.CreatedAt, o.ArchivedAt = timeOf(made), timeOfNull(archived)

	return o, nil
}

// --- accounts ---------------------------------------------------------------

const accountColumns = `id, org_id, name, kind, last4, currency, opened_on, created_by, created_at, archived_at`

// CreateAccount inserts an account, and its owner's grant if it has one.
func (s *Store) CreateAccount(ctx context.Context, a tenancybus.Account, owner *tenancybus.Grant, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts (`+accountColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID.String(), nullID(a.OrgID), a.Name, string(a.Kind), a.Last4, a.Currency, a.OpenedOn.String(),
			a.CreatedBy.String(), ms(a.CreatedAt), nullMS(a.ArchivedAt)); err != nil {
			return fmt.Errorf("inserting the account: %w", err)
		}

		if owner == nil {
			return nil
		}

		return insertGrant(ctx, tx, *owner)
	})
}

// UpdateAccount writes what can change about an account. Not its
// organization: an account does not move between them.
func (s *Store) UpdateAccount(ctx context.Context, a tenancybus.Account, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
UPDATE accounts SET name = ?, kind = ?, last4 = ?, currency = ?, opened_on = ?, archived_at = ?
WHERE id = ?`,
			a.Name, string(a.Kind), a.Last4, a.Currency, a.OpenedOn.String(), nullMS(a.ArchivedAt), a.ID.String())
		if err != nil {
			return fmt.Errorf("updating the account: %w", err)
		}

		return oneRow(res)
	})
}

// AccountByID finds one.
func (s *Store) AccountByID(ctx context.Context, id types.ID) (tenancybus.Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id.String()))
}

// AccountsByID finds several, by name.
func (s *Store) AccountsByID(ctx context.Context, ids []types.ID) ([]tenancybus.Account, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	in, args := inList(ids)

	return queryAll(ctx, s.db, scanAccount, `SELECT `+accountColumns+` FROM accounts WHERE id IN (`+in+`) ORDER BY name COLLATE NOCASE, id`, args...)
}

// AccountsInOrgs is every account in any of the organizations, by name.
func (s *Store) AccountsInOrgs(ctx context.Context, orgIDs []types.ID) ([]tenancybus.Account, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}

	in, args := inList(orgIDs)

	return queryAll(ctx, s.db, scanAccount, `SELECT `+accountColumns+` FROM accounts WHERE org_id IN (`+in+`) ORDER BY name COLLATE NOCASE, id`, args...)
}

// AllAccounts is every account, by name.
func (s *Store) AllAccounts(ctx context.Context) ([]tenancybus.Account, error) {
	return queryAll(ctx, s.db, scanAccount, `SELECT `+accountColumns+` FROM accounts ORDER BY name COLLATE NOCASE, id`)
}

func scanAccount(row scanner) (tenancybus.Account, error) {
	var (
		a                  tenancybus.Account
		id, by, kind, open string
		org                sql.NullString
		made               int64
		archived           sql.NullInt64
	)

	if err := row.Scan(&id, &org, &a.Name, &kind, &a.Last4, &a.Currency, &open, &by, &made, &archived); err != nil {
		return tenancybus.Account{}, notFound(err, "the account")
	}

	var e1, e2, e3, e4 error
	a.ID, e1 = types.ParseID(id)
	a.CreatedBy, e2 = types.ParseID(by)
	a.OpenedOn, e3 = types.ParseDate(open)

	if org.Valid {
		a.OrgID, e4 = types.ParseID(org.String)
	}

	if err := errors.Join(e1, e2, e3, e4); err != nil {
		return tenancybus.Account{}, fmt.Errorf("a stored account is unreadable: %w", err)
	}

	a.Kind = tenancybus.AccountKind(kind)
	a.CreatedAt, a.ArchivedAt = timeOf(made), timeOfNull(archived)

	return a, nil
}

// --- projects ---------------------------------------------------------------

const projectColumns = `id, org_id, name, starts_on, ends_on, note, created_by, created_at, archived_at`

// CreateProject inserts a project, and its owner's grant if it has one.
func (s *Store) CreateProject(ctx context.Context, p tenancybus.Project, owner *tenancybus.Grant, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID.String(), nullID(p.OrgID), p.Name, p.StartsOn.String(), p.EndsOn.String(), p.Note,
			p.CreatedBy.String(), ms(p.CreatedAt), nullMS(p.ArchivedAt)); err != nil {
			return fmt.Errorf("inserting the project: %w", err)
		}

		if owner == nil {
			return nil
		}

		return insertGrant(ctx, tx, *owner)
	})
}

// UpdateProject writes what can change about a project.
func (s *Store) UpdateProject(ctx context.Context, p tenancybus.Project, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
UPDATE projects SET name = ?, starts_on = ?, ends_on = ?, note = ?, archived_at = ?
WHERE id = ?`,
			p.Name, p.StartsOn.String(), p.EndsOn.String(), p.Note, nullMS(p.ArchivedAt), p.ID.String())
		if err != nil {
			return fmt.Errorf("updating the project: %w", err)
		}

		return oneRow(res)
	})
}

// ProjectByID finds one.
func (s *Store) ProjectByID(ctx context.Context, id types.ID) (tenancybus.Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id.String()))
}

// ProjectsByID finds several, by name.
func (s *Store) ProjectsByID(ctx context.Context, ids []types.ID) ([]tenancybus.Project, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	in, args := inList(ids)

	return queryAll(ctx, s.db, scanProject, `SELECT `+projectColumns+` FROM projects WHERE id IN (`+in+`) ORDER BY name COLLATE NOCASE, id`, args...)
}

// ProjectsInOrgs is every project in any of the organizations, by name.
func (s *Store) ProjectsInOrgs(ctx context.Context, orgIDs []types.ID) ([]tenancybus.Project, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}

	in, args := inList(orgIDs)

	return queryAll(ctx, s.db, scanProject, `SELECT `+projectColumns+` FROM projects WHERE org_id IN (`+in+`) ORDER BY name COLLATE NOCASE, id`, args...)
}

func scanProject(row scanner) (tenancybus.Project, error) {
	var (
		p                  tenancybus.Project
		id, by, start, end string
		org                sql.NullString
		made               int64
		archived           sql.NullInt64
	)

	if err := row.Scan(&id, &org, &p.Name, &start, &end, &p.Note, &by, &made, &archived); err != nil {
		return tenancybus.Project{}, notFound(err, "the project")
	}

	var e1, e2, e3, e4, e5 error
	p.ID, e1 = types.ParseID(id)
	p.CreatedBy, e2 = types.ParseID(by)
	p.StartsOn, e3 = types.ParseDate(start)
	p.EndsOn, e4 = types.ParseDate(end)

	if org.Valid {
		p.OrgID, e5 = types.ParseID(org.String)
	}

	if err := errors.Join(e1, e2, e3, e4, e5); err != nil {
		return tenancybus.Project{}, fmt.Errorf("a stored project is unreadable: %w", err)
	}

	p.CreatedAt, p.ArchivedAt = timeOf(made), timeOfNull(archived)

	return p, nil
}

// --- grants -----------------------------------------------------------------

const grantColumns = `id, scope_kind, scope_id, user_id, email, role, granted_by, created_at`

func insertGrant(ctx context.Context, tx *sql.Tx, g tenancybus.Grant) error {
	var email any
	if !g.Email.Zero() {
		email = g.Email.String()
	}

	_, err := tx.ExecContext(ctx, `INSERT INTO grants (`+grantColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID.String(), string(g.Scope.Kind), g.Scope.ID.String(), nullID(g.UserID), email, string(g.Role),
		g.GrantedBy.String(), ms(g.CreatedAt))

	switch {
	case sqldb.IsUniqueViolation(err):
		return tenancybus.ErrAlreadyGranted
	case err != nil:
		return fmt.Errorf("inserting the grant: %w", err)
	}

	return nil
}

// AddGrant inserts a grant.
func (s *Store) AddGrant(ctx context.Context, g tenancybus.Grant, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error { return insertGrant(ctx, tx, g) })
}

// ChangeGrant writes a grant's new role.
func (s *Store) ChangeGrant(ctx context.Context, g tenancybus.Grant, keepOwner bool, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE grants SET role = ? WHERE id = ?`, string(g.Role), g.ID.String())
		if err != nil {
			return fmt.Errorf("changing the role: %w", err)
		}

		if err := oneRow(res); err != nil {
			return err
		}

		return ownerLeft(ctx, tx, g.Scope, keepOwner)
	})
}

// RemoveGrant deletes a grant.
func (s *Store) RemoveGrant(ctx context.Context, g tenancybus.Grant, keepOwner bool, ev eventbus.Event) error {
	return s.inTx(ctx, ev, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM grants WHERE id = ?`, g.ID.String())
		if err != nil {
			return fmt.Errorf("removing the grant: %w", err)
		}

		if err := oneRow(res); err != nil {
			return err
		}

		return ownerLeft(ctx, tx, g.Scope, keepOwner)
	})
}

// ownerLeft refuses, inside the transaction that made the change, a scope
// left with no claimed owner. Counted after the change rather than before,
// so that two owners removing each other at once cannot both pass a count
// that saw the other still there. A waiting invitation is not an owner: it
// may never be claimed.
func ownerLeft(ctx context.Context, tx *sql.Tx, scope types.Scope, keepOwner bool) error {
	if !keepOwner {
		return nil
	}

	var owners int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM grants
WHERE scope_kind = ? AND scope_id = ? AND role = ? AND user_id IS NOT NULL`,
		string(scope.Kind), scope.ID.String(), string(tenancybus.Owner)).Scan(&owners); err != nil {
		return fmt.Errorf("counting the owners: %w", err)
	}

	if owners == 0 {
		return tenancybus.ErrLastOwner
	}

	return nil
}

// GrantByID finds one.
func (s *Store) GrantByID(ctx context.Context, id types.ID) (tenancybus.Grant, error) {
	return scanGrant(s.db.QueryRowContext(ctx, `SELECT `+grantColumns+` FROM grants WHERE id = ?`, id.String()))
}

// GrantsOn is every grant on a scope, claimed ones first, oldest first.
func (s *Store) GrantsOn(ctx context.Context, scope types.Scope) ([]tenancybus.Grant, error) {
	return queryAll(ctx, s.db, scanGrant, `
SELECT `+grantColumns+` FROM grants WHERE scope_kind = ? AND scope_id = ?
ORDER BY user_id IS NULL, created_at, id`, string(scope.Kind), scope.ID.String())
}

// GrantsHeld is the user's grants on any of the scopes.
func (s *Store) GrantsHeld(ctx context.Context, userID types.ID, scopes []types.Scope) ([]tenancybus.Grant, error) {
	if len(scopes) == 0 {
		return nil, nil
	}

	clauses := make([]string, len(scopes))
	args := []any{userID.String()}

	for i, sc := range scopes {
		clauses[i] = "(scope_kind = ? AND scope_id = ?)"
		args = append(args, string(sc.Kind), sc.ID.String())
	}

	return queryAll(ctx, s.db, scanGrant, `
SELECT `+grantColumns+` FROM grants WHERE user_id = ? AND (`+strings.Join(clauses, " OR ")+`)`, args...)
}

// GrantsOfUser is everything a user has been given.
func (s *Store) GrantsOfUser(ctx context.Context, userID types.ID) ([]tenancybus.Grant, error) {
	return queryAll(ctx, s.db, scanGrant, `SELECT `+grantColumns+` FROM grants WHERE user_id = ? ORDER BY created_at, id`, userID.String())
}

// ClaimGrants makes the grants waiting on an address the user's, in one
// transaction with a line of history for each. One waiting where the user
// already holds a grant is dropped (tenancybus.Business.Claim).
func (s *Store) ClaimGrants(ctx context.Context, now time.Time, userID types.ID, email types.Email) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting to claim: %w", err)
	}
	defer tx.Rollback()

	waiting, err := queryAll(ctx, tx, scanGrant, `SELECT `+grantColumns+` FROM grants WHERE email = ?`, email.String())
	if err != nil {
		return 0, err
	}

	if len(waiting) == 0 {
		return 0, nil
	}

	if _, err := tx.ExecContext(ctx, `
DELETE FROM grants
WHERE email = ? AND EXISTS (
    SELECT 1 FROM grants AS held
    WHERE held.scope_kind = grants.scope_kind AND held.scope_id = grants.scope_id AND held.user_id = ?
)`, email.String(), userID.String()); err != nil {
		return 0, fmt.Errorf("dropping invitations already covered: %w", err)
	}

	claimed, err := queryAll(ctx, tx, scanGrant, `SELECT `+grantColumns+` FROM grants WHERE email = ?`, email.String())
	if err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE grants SET user_id = ?, email = NULL WHERE email = ?`,
		userID.String(), email.String()); err != nil {
		return 0, fmt.Errorf("claiming the invitations: %w", err)
	}

	for _, g := range claimed {
		ev := eventbus.New(now, userID, g.Scope, eventbus.GrantClaimed, map[string]string{"role": string(g.Role), "email": email.String()})
		if err := eventdb.Insert(ctx, tx, ev); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("claiming the invitations: %w", err)
	}

	return len(claimed), nil
}

func scanGrant(row scanner) (tenancybus.Grant, error) {
	var (
		g                   tenancybus.Grant
		id, kind, sid, role string
		by                  string
		user, email         sql.NullString
		made                int64
	)

	if err := row.Scan(&id, &kind, &sid, &user, &email, &role, &by, &made); err != nil {
		return tenancybus.Grant{}, notFound(err, "the grant")
	}

	var e1, e2, e3, e4, e5, e6 error
	g.ID, e1 = types.ParseID(id)
	g.Scope.Kind, e2 = types.ParseScopeKind(kind)
	g.Scope.ID, e3 = types.ParseID(sid)
	g.Role, e4 = tenancybus.ParseRole(role)
	g.GrantedBy, e5 = types.ParseID(by)

	switch {
	case user.Valid:
		g.UserID, e6 = types.ParseID(user.String)
	case email.Valid:
		g.Email, e6 = types.ParseEmail(email.String)
	}

	if err := errors.Join(e1, e2, e3, e4, e5, e6); err != nil {
		return tenancybus.Grant{}, fmt.Errorf("a stored grant is unreadable: %w", err)
	}

	g.CreatedAt = timeOf(made)

	return g, nil
}

// --- small things -----------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryAll[T any](ctx context.Context, db querier, scan func(scanner) (T, error), q string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	defer rows.Close()

	var out []T

	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}

	return out, nil
}

func inList(ids []types.ID) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}

	return strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", "), args
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return tenancybus.ErrNotFound
	}

	return fmt.Errorf("reading %s: %w", what, err)
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("checking what changed: %w", err)
	case n == 0:
		return tenancybus.ErrNotFound
	}

	return nil
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}

	return t.UnixMilli()
}

func timeOf(v int64) time.Time { return time.UnixMilli(v).UTC() }

func timeOfNull(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}

	return timeOf(n.Int64)
}

func nullID(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}
