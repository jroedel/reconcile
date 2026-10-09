// Package tenancybus holds organizations, accounts and projects, and who may
// do what on each (docs/plan.md, "Tenancy and access").
//
// # Nobody sees anything they were not given
//
// There is no administrator who sees every account. Whoever makes an
// organization, a personal account or a project owns it, and everybody else
// reaches it only by a grant: a role, on one scope, given by an owner to an
// email address. The site administrator manages users and nothing else
// (userbus); that person sees an organization's money only if somebody there
// gave them a grant, like anybody else.
//
// So every method that reads or changes a scope takes the actor and answers
// [ErrNotFound] for one they cannot read -- not a refusal, which would say
// the thing exists. A refusal ([ErrForbidden]) is only for somebody who can
// see it and is asking to do more than their role allows.
//
// # Inheritance
//
// A role on an organization holds on every account and project in it. A role
// on an account or a project holds on that one thing. An account or project
// with no organization is personal: its creator owns it, and grants on it
// are the only way in.
//
// # One store, one transaction
//
// Organizations, accounts, projects and grants share a store rather than a
// domain each, because the things that must happen together cross them:
// making an organization and making its creator the owner is one
// transaction, and so is removing a grant and checking that an owner is
// left. Each change writes its line of history in that same transaction
// (eventbus).
package tenancybus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/types"
)

// The errors this package returns.
var (
	// ErrNotFound is a scope that does not exist, or one the actor cannot
	// read: the two are the same answer on purpose.
	ErrNotFound = errors.New("not found")

	// ErrForbidden is a scope the actor can read, and an action their role
	// does not allow.
	ErrForbidden = errors.New("your role does not allow that")

	// ErrLastOwner refuses a change that would leave an organization, or a
	// personal account or project, with nobody who can manage it.
	ErrLastOwner = errors.New("that would leave nobody who can manage it")

	// ErrAlreadyGranted is a second grant to the same person on the same
	// scope. Change the role on the one they have instead.
	ErrAlreadyGranted = errors.New("that person already has a role here")
)

// Invalid is a value somebody typed that the rules refuse. Field names which,
// so a page can say so beside it.
type Invalid struct {
	Field string
	Err   error
}

func (e Invalid) Error() string { return e.Field + ": " + e.Err.Error() }
func (e Invalid) Unwrap() error { return e.Err }

// Limits on what people type.
const (
	MaxName = 100
	MaxNote = 2000
)

// Org is an organization: a parish, a movement, a group of friends running a
// pilgrimage. It is optional -- an account or project may stand alone.
type Org struct {
	ID         types.ID
	Name       string
	CreatedBy  types.ID
	CreatedAt  time.Time
	ArchivedAt time.Time // zero while in use

	// FiscalStart is the month its budget year starts, 1 to 12
	// (docs/budgets.md): January unless an owner says otherwise.
	FiscalStart int
}

// Scope names it.
func (o Org) Scope() types.Scope { return types.OrgScope(o.ID) }

// Archived reports whether it has been put away.
func (o Org) Archived() bool { return !o.ArchivedAt.IsZero() }

// AccountKind is what sort of account it is, which decides how its
// statements read: a card's charges are money out, a checking account's
// deposits money in.
type AccountKind string

const (
	Checking AccountKind = "checking"
	Savings  AccountKind = "savings"
	Card     AccountKind = "card"
	Cash     AccountKind = "cash"
	Other    AccountKind = "other"
)

// AccountKinds is every kind, in the order a form offers them.
var AccountKinds = []AccountKind{Checking, Savings, Card, Cash, Other}

// Account is a bank account, a card, or a cash box: the thing a statement is
// of.
type Account struct {
	ID    types.ID
	OrgID types.ID // zero for a personal account
	Name  string
	Kind  AccountKind

	// Last4 is the last four digits of the number, for telling two accounts
	// at one bank apart. Never the whole number: the app has no use for it,
	// and what is not stored cannot leak.
	Last4 string

	// Currency is the ISO 4217 code its statements are in.
	Currency string
	OpenedOn types.Date

	CreatedBy  types.ID
	CreatedAt  time.Time
	ArchivedAt time.Time
}

// Scope names it.
func (a Account) Scope() types.Scope { return types.AccountScope(a.ID) }

// Archived reports whether it has been put away.
func (a Account) Archived() bool { return !a.ArchivedAt.IsZero() }

// Project gathers parts of transactions from any accounts, to see whether
// something -- a pilgrimage, a building fund -- comes out even.
type Project struct {
	ID       types.ID
	OrgID    types.ID // zero for a personal project
	Name     string
	StartsOn types.Date
	EndsOn   types.Date
	Note     string

	CreatedBy  types.ID
	CreatedAt  time.Time
	ArchivedAt time.Time
}

// Scope names it.
func (p Project) Scope() types.Scope { return types.ProjectScope(p.ID) }

// Archived reports whether it has been put away.
func (p Project) Archived() bool { return !p.ArchivedAt.IsZero() }

// Grant is one person's role on one scope.
//
// Exactly one of UserID and Email is set. A grant to an address nobody signs
// in with yet waits under Email, and becomes the user's when they first sign
// in with it, or move their account to it ([Business.Claim]).
type Grant struct {
	ID        types.ID
	Scope     types.Scope
	UserID    types.ID
	Email     types.Email
	Role      Role
	GrantedBy types.ID
	CreatedAt time.Time
}

// Pending reports whether nobody has claimed it yet.
func (g Grant) Pending() bool { return g.UserID.Zero() }

// Users is what this package needs to know about users: whether an address
// already belongs to one, so that a grant to it goes straight to them.
type Users interface {
	UserIDByEmail(ctx context.Context, email types.Email) (types.ID, bool, error)
}

// Storer is what this package needs from storage. Every method that changes
// something takes the event that records it, and writes both in one
// transaction.
type Storer interface {
	CreateOrg(ctx context.Context, o Org, owner Grant, ev eventbus.Event) error
	UpdateOrg(ctx context.Context, o Org, ev eventbus.Event) error
	OrgByID(ctx context.Context, id types.ID) (Org, error)
	OrgsByID(ctx context.Context, ids []types.ID) ([]Org, error)
	AllOrgs(ctx context.Context) ([]Org, error)

	// CreateAccount and CreateProject take an owner grant for a personal
	// one, and nil for one in an organization, whose owners it inherits.
	CreateAccount(ctx context.Context, a Account, owner *Grant, ev eventbus.Event) error
	UpdateAccount(ctx context.Context, a Account, ev eventbus.Event) error
	AccountByID(ctx context.Context, id types.ID) (Account, error)
	AccountsByID(ctx context.Context, ids []types.ID) ([]Account, error)
	AccountsInOrgs(ctx context.Context, orgIDs []types.ID) ([]Account, error)
	AllAccounts(ctx context.Context) ([]Account, error)

	CreateProject(ctx context.Context, p Project, owner *Grant, ev eventbus.Event) error
	UpdateProject(ctx context.Context, p Project, ev eventbus.Event) error
	ProjectByID(ctx context.Context, id types.ID) (Project, error)
	ProjectsByID(ctx context.Context, ids []types.ID) ([]Project, error)
	ProjectsInOrgs(ctx context.Context, orgIDs []types.ID) ([]Project, error)

	// AddGrant returns ErrAlreadyGranted for a second grant to one person
	// on one scope; the unique index decides, not a read beforehand.
	AddGrant(ctx context.Context, g Grant, ev eventbus.Event) error

	// ChangeGrant and RemoveGrant, with keepOwner, refuse with
	// ErrLastOwner when the change would leave the scope no claimed owner
	// -- checked inside the transaction that makes it.
	ChangeGrant(ctx context.Context, g Grant, keepOwner bool, ev eventbus.Event) error
	RemoveGrant(ctx context.Context, g Grant, keepOwner bool, ev eventbus.Event) error
	GrantByID(ctx context.Context, id types.ID) (Grant, error)
	GrantsOn(ctx context.Context, scope types.Scope) ([]Grant, error)
	GrantsHeld(ctx context.Context, userID types.ID, scopes []types.Scope) ([]Grant, error)
	GrantsOfUser(ctx context.Context, userID types.ID) ([]Grant, error)

	// ClaimGrants makes every grant waiting on email the user's, writing a
	// claimed event for each, and returns how many there were.
	ClaimGrants(ctx context.Context, now time.Time, userID types.ID, email types.Email) (int, error)
}

// Business is the set of operations on organizations, accounts, projects and
// grants.
type Business struct {
	log   *slog.Logger
	store Storer
	users Users
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, users Users) *Business {
	return &Business{log: log, store: store, users: users}
}

// --- access -----------------------------------------------------------------

// AccessTo is what the actor holds on a scope, including what it inherits
// from its organization. The zero Access, with no error, for a scope they
// hold nothing on -- including one that does not exist.
func (b *Business) AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (Access, error) {
	if actor.Zero() || scope.Zero() {
		return Access{Scope: scope}, nil
	}

	scopes := []types.Scope{scope}

	if parent, err := b.parent(ctx, scope); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Access{Scope: scope}, nil
		}

		return Access{}, err
	} else if !parent.Zero() {
		scopes = append(scopes, parent)
	}

	grants, err := b.store.GrantsHeld(ctx, actor, scopes)
	if err != nil {
		return Access{}, fmt.Errorf("reading what you have been given: %w", err)
	}

	access := Access{Scope: scope}
	for _, g := range grants {
		access.Roles = append(access.Roles, g.Role)
	}

	return access, nil
}

// parent is the organization a scope belongs to, or the zero Scope for an
// organization or a personal account or project.
func (b *Business) parent(ctx context.Context, scope types.Scope) (types.Scope, error) {
	var org types.ID

	switch scope.Kind {
	case types.ScopeOrg:
		if _, err := b.store.OrgByID(ctx, scope.ID); err != nil {
			return types.Scope{}, err
		}

		return types.Scope{}, nil
	case types.ScopeAccount:
		a, err := b.store.AccountByID(ctx, scope.ID)
		if err != nil {
			return types.Scope{}, err
		}

		org = a.OrgID
	case types.ScopeProject:
		p, err := b.store.ProjectByID(ctx, scope.ID)
		if err != nil {
			return types.Scope{}, err
		}

		org = p.OrgID
	default:
		return types.Scope{}, ErrNotFound
	}

	if org.Zero() {
		return types.Scope{}, nil
	}

	return types.OrgScope(org), nil
}

// require is AccessTo that answers ErrNotFound for a scope the actor cannot
// read and ErrForbidden for one they can read but not do p on.
func (b *Business) require(ctx context.Context, actor types.ID, scope types.Scope, p Permission) (Access, error) {
	access, err := b.AccessTo(ctx, actor, scope)

	switch {
	case err != nil:
		return Access{}, err
	case !access.Can(Read):
		return Access{}, ErrNotFound
	case !access.Can(p):
		return access, ErrForbidden
	}

	return access, nil
}

// --- organizations ----------------------------------------------------------

// CreateOrg makes an organization, owned by whoever made it.
func (b *Business) CreateOrg(ctx context.Context, now time.Time, actor types.ID, name string) (Org, error) {
	name, err := cleanName(name)
	if err != nil {
		return Org{}, err
	}

	o := Org{ID: types.NewID(), Name: name, CreatedBy: actor, CreatedAt: now, FiscalStart: 1}

	owner := Grant{ID: types.NewID(), Scope: o.Scope(), UserID: actor, Role: Owner, GrantedBy: actor, CreatedAt: now}

	if err := b.store.CreateOrg(ctx, o, owner,
		eventbus.New(now, actor, o.Scope(), eventbus.Created, map[string]string{"name": name})); err != nil {
		return Org{}, err
	}

	b.log.Info("an organization was made", "org_id", o.ID.String(), "user_id", actor.String())

	return o, nil
}

// Org is one organization, and what the actor holds on it.
func (b *Business) Org(ctx context.Context, actor, id types.ID) (Org, Access, error) {
	access, err := b.require(ctx, actor, types.OrgScope(id), Read)
	if err != nil {
		return Org{}, Access{}, err
	}

	o, err := b.store.OrgByID(ctx, id)

	return o, access, err
}

// RenameOrg changes an organization's name.
func (b *Business) RenameOrg(ctx context.Context, now time.Time, actor, id types.ID, name string) (Org, error) {
	if _, err := b.require(ctx, actor, types.OrgScope(id), Manage); err != nil {
		return Org{}, err
	}

	name, err := cleanName(name)
	if err != nil {
		return Org{}, err
	}

	o, err := b.store.OrgByID(ctx, id)
	if err != nil {
		return Org{}, err
	}

	if o.Name == name {
		return o, nil
	}

	detail := map[string]string{"from": o.Name, "name": name}
	o.Name = name

	return o, b.store.UpdateOrg(ctx, o, eventbus.New(now, actor, o.Scope(), eventbus.Renamed, detail))
}

// FiscalSet is the line in an organization's history when its budget year
// is moved.
const FiscalSet eventbus.Action = "org.fiscal"

// SetFiscalStart says which month an organization's budget year starts in.
// It changes which months every budget year of it covers, so it is an
// owner's, and the history says from what to what.
func (b *Business) SetFiscalStart(ctx context.Context, now time.Time, actor, id types.ID, month int) (Org, error) {
	if _, err := b.require(ctx, actor, types.OrgScope(id), Manage); err != nil {
		return Org{}, err
	}

	if month < 1 || month > 12 {
		return Org{}, Invalid{Field: "fiscal-start", Err: errors.New("choose a month")}
	}

	o, err := b.store.OrgByID(ctx, id)
	if err != nil {
		return Org{}, err
	}

	if o.FiscalStart == month {
		return o, nil
	}

	detail := map[string]string{"from": strconv.Itoa(o.FiscalStart), "month": strconv.Itoa(month)}
	o.FiscalStart = month

	return o, b.store.UpdateOrg(ctx, o, eventbus.New(now, actor, o.Scope(), FiscalSet, detail))
}

// SetOrgArchived puts an organization away, or brings it back. Nothing in it
// is deleted: archiving takes it off the lists people work from, and that is
// all.
func (b *Business) SetOrgArchived(ctx context.Context, now time.Time, actor, id types.ID, archived bool) (Org, error) {
	if _, err := b.require(ctx, actor, types.OrgScope(id), Manage); err != nil {
		return Org{}, err
	}

	o, err := b.store.OrgByID(ctx, id)
	if err != nil {
		return Org{}, err
	}

	if o.Archived() == archived {
		return o, nil
	}

	o.ArchivedAt = time.Time{}
	if archived {
		o.ArchivedAt = now
	}

	return o, b.store.UpdateOrg(ctx, o, eventbus.New(now, actor, o.Scope(), archiveAction(archived), nil))
}

// --- accounts ---------------------------------------------------------------

// AccountFields is what a person types about an account.
type AccountFields struct {
	Name     string
	Kind     string
	Last4    string
	Currency string
	OpenedOn string
}

var (
	fourDigits = regexp.MustCompile(`^[0-9]{4}$`)
	currency   = regexp.MustCompile(`^[A-Z]{3}$`)
)

// DefaultCurrency is what an account is in when nobody says otherwise.
const DefaultCurrency = "USD"

func (f AccountFields) apply(a *Account) error {
	name, err := cleanName(f.Name)
	if err != nil {
		return err
	}

	kind := AccountKind(f.Kind)
	if !slices.Contains(AccountKinds, kind) {
		return Invalid{Field: "kind", Err: fmt.Errorf("%q is not a kind of account", f.Kind)}
	}

	last4 := strings.TrimSpace(f.Last4)
	if last4 != "" && !fourDigits.MatchString(last4) {
		return Invalid{Field: "last4", Err: errors.New("the last four digits, or nothing")}
	}

	cur := strings.ToUpper(strings.TrimSpace(f.Currency))
	if cur == "" {
		cur = DefaultCurrency
	}

	if !currency.MatchString(cur) {
		return Invalid{Field: "currency", Err: errors.New("a three-letter currency code, such as USD")}
	}

	opened, err := types.ParseDate(f.OpenedOn)
	if err != nil {
		return Invalid{Field: "opened", Err: err}
	}

	a.Name, a.Kind, a.Last4, a.Currency, a.OpenedOn = name, kind, last4, cur, opened

	return nil
}

// CreateAccount makes an account. In an organization it needs Manage there,
// and its owners are the organization's; on its own it is personal, and its
// creator owns it.
func (b *Business) CreateAccount(ctx context.Context, now time.Time, actor, orgID types.ID, f AccountFields) (Account, error) {
	if !orgID.Zero() {
		if _, err := b.require(ctx, actor, types.OrgScope(orgID), Manage); err != nil {
			return Account{}, err
		}
	}

	a := Account{ID: types.NewID(), OrgID: orgID, CreatedBy: actor, CreatedAt: now}
	if err := f.apply(&a); err != nil {
		return Account{}, err
	}

	var owner *Grant
	if orgID.Zero() {
		owner = &Grant{ID: types.NewID(), Scope: a.Scope(), UserID: actor, Role: Owner, GrantedBy: actor, CreatedAt: now}
	}

	if err := b.store.CreateAccount(ctx, a, owner,
		eventbus.New(now, actor, a.Scope(), eventbus.Created, map[string]string{"name": a.Name})); err != nil {
		return Account{}, err
	}

	return a, nil
}

// Account is one account, and what the actor holds on it.
func (b *Business) Account(ctx context.Context, actor, id types.ID) (Account, Access, error) {
	access, err := b.require(ctx, actor, types.AccountScope(id), Read)
	if err != nil {
		return Account{}, Access{}, err
	}

	a, err := b.store.AccountByID(ctx, id)

	return a, access, err
}

// EditAccount changes what was typed about an account.
func (b *Business) EditAccount(ctx context.Context, now time.Time, actor, id types.ID, f AccountFields) (Account, error) {
	if _, err := b.require(ctx, actor, types.AccountScope(id), Manage); err != nil {
		return Account{}, err
	}

	a, err := b.store.AccountByID(ctx, id)
	if err != nil {
		return Account{}, err
	}

	if err := f.apply(&a); err != nil {
		return Account{}, err
	}

	return a, b.store.UpdateAccount(ctx, a, eventbus.New(now, actor, a.Scope(), eventbus.Edited, map[string]string{"name": a.Name}))
}

// SetAccountArchived puts an account away, or brings it back.
func (b *Business) SetAccountArchived(ctx context.Context, now time.Time, actor, id types.ID, archived bool) (Account, error) {
	if _, err := b.require(ctx, actor, types.AccountScope(id), Manage); err != nil {
		return Account{}, err
	}

	a, err := b.store.AccountByID(ctx, id)
	if err != nil {
		return Account{}, err
	}

	if a.Archived() == archived {
		return a, nil
	}

	a.ArchivedAt = time.Time{}
	if archived {
		a.ArchivedAt = now
	}

	return a, b.store.UpdateAccount(ctx, a, eventbus.New(now, actor, a.Scope(), archiveAction(archived), nil))
}

// --- projects ---------------------------------------------------------------

// ProjectFields is what a person types about a project.
type ProjectFields struct {
	Name     string
	StartsOn string
	EndsOn   string
	Note     string
}

func (f ProjectFields) apply(p *Project) error {
	name, err := cleanName(f.Name)
	if err != nil {
		return err
	}

	starts, err := types.ParseDate(f.StartsOn)
	if err != nil {
		return Invalid{Field: "starts", Err: err}
	}

	ends, err := types.ParseDate(f.EndsOn)
	if err != nil {
		return Invalid{Field: "ends", Err: err}
	}

	if !starts.Zero() && !ends.Zero() && ends.Before(starts) {
		return Invalid{Field: "ends", Err: errors.New("an end on or after the start")}
	}

	note := strings.TrimSpace(f.Note)
	if utf8.RuneCountInString(note) > MaxNote {
		return Invalid{Field: "note", Err: fmt.Errorf("a note of at most %d characters", MaxNote)}
	}

	p.Name, p.StartsOn, p.EndsOn, p.Note = name, starts, ends, note

	return nil
}

// CreateProject makes a project. In an organization it needs Bookkeep
// there -- a project is bookkeeping, not administration -- and inherits the
// organization's people; on its own its creator owns it.
func (b *Business) CreateProject(ctx context.Context, now time.Time, actor, orgID types.ID, f ProjectFields) (Project, error) {
	if !orgID.Zero() {
		if _, err := b.require(ctx, actor, types.OrgScope(orgID), Bookkeep); err != nil {
			return Project{}, err
		}
	}

	p := Project{ID: types.NewID(), OrgID: orgID, CreatedBy: actor, CreatedAt: now}
	if err := f.apply(&p); err != nil {
		return Project{}, err
	}

	var owner *Grant
	if orgID.Zero() {
		owner = &Grant{ID: types.NewID(), Scope: p.Scope(), UserID: actor, Role: Owner, GrantedBy: actor, CreatedAt: now}
	}

	if err := b.store.CreateProject(ctx, p, owner,
		eventbus.New(now, actor, p.Scope(), eventbus.Created, map[string]string{"name": p.Name})); err != nil {
		return Project{}, err
	}

	return p, nil
}

// Project is one project, and what the actor holds on it.
func (b *Business) Project(ctx context.Context, actor, id types.ID) (Project, Access, error) {
	access, err := b.require(ctx, actor, types.ProjectScope(id), Read)
	if err != nil {
		return Project{}, Access{}, err
	}

	p, err := b.store.ProjectByID(ctx, id)

	return p, access, err
}

// EditProject changes what was typed about a project.
func (b *Business) EditProject(ctx context.Context, now time.Time, actor, id types.ID, f ProjectFields) (Project, error) {
	if _, err := b.require(ctx, actor, types.ProjectScope(id), Manage); err != nil {
		return Project{}, err
	}

	p, err := b.store.ProjectByID(ctx, id)
	if err != nil {
		return Project{}, err
	}

	if err := f.apply(&p); err != nil {
		return Project{}, err
	}

	return p, b.store.UpdateProject(ctx, p, eventbus.New(now, actor, p.Scope(), eventbus.Edited, map[string]string{"name": p.Name}))
}

// SetProjectArchived puts a project away, or brings it back.
func (b *Business) SetProjectArchived(ctx context.Context, now time.Time, actor, id types.ID, archived bool) (Project, error) {
	if _, err := b.require(ctx, actor, types.ProjectScope(id), Manage); err != nil {
		return Project{}, err
	}

	p, err := b.store.ProjectByID(ctx, id)
	if err != nil {
		return Project{}, err
	}

	if p.Archived() == archived {
		return p, nil
	}

	p.ArchivedAt = time.Time{}
	if archived {
		p.ArchivedAt = now
	}

	return p, b.store.UpdateProject(ctx, p, eventbus.New(now, actor, p.Scope(), archiveAction(archived), nil))
}

// --- what is inside an organization -----------------------------------------

// Contents is an organization's accounts and projects, archived ones last.
func (b *Business) Contents(ctx context.Context, actor, orgID types.ID) ([]Account, []Project, error) {
	if _, err := b.require(ctx, actor, types.OrgScope(orgID), Read); err != nil {
		return nil, nil, err
	}

	accounts, err := b.store.AccountsInOrgs(ctx, []types.ID{orgID})
	if err != nil {
		return nil, nil, err
	}

	projects, err := b.store.ProjectsInOrgs(ctx, []types.ID{orgID})
	if err != nil {
		return nil, nil, err
	}

	return accounts, projects, nil
}

// Overview is everything a user can reach, for the page they land on: the
// organizations they hold a role in with what is inside each, and the
// accounts and projects they were given on their own.
type Overview struct {
	Orgs     []OrgOverview
	Accounts []Account
	Projects []Project
}

// OrgOverview is one organization on the overview.
type OrgOverview struct {
	Org      Org
	Accounts []Account
	Projects []Project
}

// Empty reports whether the user has nothing yet.
func (o Overview) Empty() bool {
	return len(o.Orgs) == 0 && len(o.Accounts) == 0 && len(o.Projects) == 0
}

// Overview gathers what a user can reach. Archived things are left out: the
// organization's own page lists them.
func (b *Business) Overview(ctx context.Context, actor types.ID) (Overview, error) {
	grants, err := b.store.GrantsOfUser(ctx, actor)
	if err != nil {
		return Overview{}, fmt.Errorf("reading what you have been given: %w", err)
	}

	var orgIDs, accountIDs, projectIDs []types.ID

	for _, g := range grants {
		switch g.Scope.Kind {
		case types.ScopeOrg:
			orgIDs = append(orgIDs, g.Scope.ID)
		case types.ScopeAccount:
			accountIDs = append(accountIDs, g.Scope.ID)
		case types.ScopeProject:
			projectIDs = append(projectIDs, g.Scope.ID)
		}
	}

	orgs, err := b.store.OrgsByID(ctx, orgIDs)
	if err != nil {
		return Overview{}, err
	}

	inOrg, err := b.store.AccountsInOrgs(ctx, orgIDs)
	if err != nil {
		return Overview{}, err
	}

	projectsInOrg, err := b.store.ProjectsInOrgs(ctx, orgIDs)
	if err != nil {
		return Overview{}, err
	}

	var out Overview

	covered := map[types.ID]bool{}

	for _, o := range orgs {
		if o.Archived() {
			continue
		}

		covered[o.ID] = true
		ov := OrgOverview{Org: o}

		for _, a := range inOrg {
			if a.OrgID == o.ID && !a.Archived() {
				ov.Accounts = append(ov.Accounts, a)
			}
		}

		for _, p := range projectsInOrg {
			if p.OrgID == o.ID && !p.Archived() {
				ov.Projects = append(ov.Projects, p)
			}
		}

		out.Orgs = append(out.Orgs, ov)
	}

	accounts, err := b.store.AccountsByID(ctx, accountIDs)
	if err != nil {
		return Overview{}, err
	}

	for _, a := range accounts {
		// Already listed under its organization, which the user can read.
		if !a.Archived() && !covered[a.OrgID] {
			out.Accounts = append(out.Accounts, a)
		}
	}

	projects, err := b.store.ProjectsByID(ctx, projectIDs)
	if err != nil {
		return Overview{}, err
	}

	for _, p := range projects {
		if !p.Archived() && !covered[p.OrgID] {
			out.Projects = append(out.Projects, p)
		}
	}

	return out, nil
}

// ProjectsFor is the projects, not archived, on which the actor may do p:
// the choices when they put a transaction's money into a project.
func (b *Business) ProjectsFor(ctx context.Context, actor types.ID, p Permission) ([]Project, error) {
	ov, err := b.Overview(ctx, actor)
	if err != nil {
		return nil, err
	}

	all := ov.Projects
	for _, o := range ov.Orgs {
		all = append(all, o.Projects...)
	}

	var out []Project

	for _, pr := range all {
		access, err := b.AccessTo(ctx, actor, pr.Scope())
		if err != nil {
			return nil, err
		}

		if access.Can(p) {
			out = append(out, pr)
		}
	}

	slices.SortFunc(out, func(a, b Project) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })

	return out, nil
}

// ProjectNames is the names of projects by ID, asking nobody's permission.
//
// For the ledger, which has already asked whether the reader may see an
// account and is naming the projects that account's own splits point at.
// What a split of my account's money is spent on is part of my account,
// even when the project's other books are not mine to read.
func (b *Business) ProjectNames(ctx context.Context, ids []types.ID) (map[types.ID]string, error) {
	projects, err := b.store.ProjectsByID(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make(map[types.ID]string, len(projects))
	for _, p := range projects {
		out[p.ID] = p.Name
	}

	return out, nil
}

// --- the site administrator's list ------------------------------------------

// Names is every organization and account by name, for the site
// administrator: enough to answer "is there already one for our parish?"
// and nothing about the money in them. The caller checks that the reader is
// the site administrator.
func (b *Business) Names(ctx context.Context) ([]Org, []Account, error) {
	orgs, err := b.store.AllOrgs(ctx)
	if err != nil {
		return nil, nil, err
	}

	accounts, err := b.store.AllAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}

	return orgs, accounts, nil
}

// --- small things -----------------------------------------------------------

func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")

	switch {
	case name == "":
		return "", Invalid{Field: "name", Err: errors.New("a name")}
	case utf8.RuneCountInString(name) > MaxName:
		return "", Invalid{Field: "name", Err: fmt.Errorf("a name of at most %d characters", MaxName)}
	}

	return name, nil
}

func archiveAction(archived bool) eventbus.Action {
	if archived {
		return eventbus.Archived
	}

	return eventbus.Restored
}
