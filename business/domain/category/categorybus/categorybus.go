// Package categorybus is the lists of categories a transaction's parts are
// sorted into: offerings, utilities, travel (docs/plan.md, "Ledger").
//
// One list per organization, shared by all its accounts, so that the
// parish's checking account and its card say "Utilities" the same way and
// the accountant's package adds them up. A personal account, which belongs
// to no organization, keeps a list of its own.
//
// A category is never deleted, only archived: a split that names it keeps
// naming it, and last year's package still adds up. An archived category is
// off the choices for new splits and stays on the ones that have it.
//
// # Kinds
//
// Every category is one of four kinds of money (docs/plan.md, "Kinds of
// money"): income, an expense, a transfer between the owner's own accounts,
// or pass-through -- money that went through but was never theirs. Only
// income and expenses are operations; the totals read the kind, never a
// category's name. The kind is on the category rather than on each part so
// that sorting a part stays one choice.
//
// The kind is not the sign. A refund in "Fuel" is money in and still an
// expense, a negative one; that is the whole reason a kind exists apart from
// the direction the bank reports.
package categorybus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// The errors a page tells apart. Not found and not allowed are tenancy's,
// so that every page answers them alike.
var (
	ErrNotFound  = tenancybus.ErrNotFound
	ErrForbidden = tenancybus.ErrForbidden

	// ErrDuplicate is a name the list already has, archived or not. An
	// archived one is brought back rather than made twice.
	ErrDuplicate = errors.New("the list already has a category with that name")
)

// The actions this domain writes into the owner's history.
const (
	Added    eventbus.Action = "category.added"
	Renamed  eventbus.Action = "category.renamed"
	Archived eventbus.Action = "category.archived"
	Restored eventbus.Action = "category.restored"
	KindSet  eventbus.Action = "category.kind"
)

// Kind is what the money in a category was.
type Kind string

const (
	// Unsaid is a category made before kinds, whose owner has not said
	// which it is. Parts in it count as neither income nor expense until
	// they do, and the list asks.
	Unsaid Kind = ""

	Expense     Kind = "expense"
	Income      Kind = "income"
	Transfer    Kind = "transfer"
	PassThrough Kind = "passthrough"
)

// Kinds is every kind a category may be given, in the order the pages
// offer them: expenses first, since most parts are.
var Kinds = []Kind{Expense, Income, Transfer, PassThrough}

// Valid reports whether k is one of Kinds.
func (k Kind) Valid() bool { return slices.Contains(Kinds, k) }

// Operations reports whether money of this kind is the owner's operations:
// income or an expense, which a profit and loss adds up. Transfers and
// pass-through are not, and neither is a category not yet said.
func (k Kind) Operations() bool { return k == Income || k == Expense }

// MaxName is the longest name a category may have.
const MaxName = 60

// Category is one entry in a list.
type Category struct {
	ID types.ID

	// Owner is the organization whose list it is on, or the personal
	// account.
	Owner types.Scope

	Name       string
	Kind       Kind
	CreatedBy  types.ID
	CreatedAt  time.Time
	ArchivedAt time.Time
}

// Archived reports whether it is off the choices.
func (c Category) Archived() bool { return !c.ArchivedAt.IsZero() }

// Access is how this domain asks who may do what (tenancybus).
type Access interface {
	AccessTo(ctx context.Context, actor types.ID, scope types.Scope) (tenancybus.Access, error)
	Account(ctx context.Context, actor, id types.ID) (tenancybus.Account, tenancybus.Access, error)
}

// Storer keeps the lists.
type Storer interface {
	Create(ctx context.Context, c Category, ev eventbus.Event) error
	Update(ctx context.Context, c Category, ev eventbus.Event) error
	ByID(ctx context.Context, id types.ID) (Category, error)
	Of(ctx context.Context, owner types.Scope) ([]Category, error)
}

// Business is the set of operations on category lists.
type Business struct {
	log    *slog.Logger
	store  Storer
	access Access
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer, access Access) *Business {
	return &Business{log: log, store: store, access: access}
}

// OwnerOf is whose list an account's transactions are sorted with: its
// organization's, or its own when it has none.
func OwnerOf(a tenancybus.Account) types.Scope {
	if a.OrgID.Zero() {
		return a.Scope()
	}

	return types.OrgScope(a.OrgID)
}

// require answers ErrNotFound for an owner the actor cannot read, and
// ErrForbidden for one they cannot keep the books of. Only an organization
// or a personal account has a list: an account in an organization uses the
// organization's, and a list of its own would be one nobody sorts with.
func (b *Business) require(ctx context.Context, actor types.ID, owner types.Scope, p tenancybus.Permission) (tenancybus.Access, error) {
	var (
		access tenancybus.Access
		err    error
	)

	switch owner.Kind {
	case types.ScopeOrg:
		access, err = b.access.AccessTo(ctx, actor, owner)
	case types.ScopeAccount:
		var a tenancybus.Account

		a, access, err = b.access.Account(ctx, actor, owner.ID)
		if err == nil && !a.OrgID.Zero() {
			return tenancybus.Access{}, ErrNotFound
		}
	default:
		return tenancybus.Access{}, ErrNotFound
	}

	if errors.Is(err, ErrNotFound) {
		return tenancybus.Access{}, ErrNotFound
	}

	switch {
	case err != nil:
		return tenancybus.Access{}, err
	case !access.Can(tenancybus.Read):
		return tenancybus.Access{}, ErrNotFound
	case !access.Can(p):
		return access, ErrForbidden
	}

	return access, nil
}

// List is a list, archived categories last, and what the actor holds on its
// owner.
func (b *Business) List(ctx context.Context, actor types.ID, owner types.Scope) ([]Category, tenancybus.Access, error) {
	access, err := b.require(ctx, actor, owner, tenancybus.Read)
	if err != nil {
		return nil, tenancybus.Access{}, err
	}

	list, err := b.store.Of(ctx, owner)

	return list, access, err
}

// ForAccount is the list an account's transactions are sorted with, asking
// nobody's permission: for the ledger, which has already asked whether the
// reader may see the account. A bookkeeper of one account in an
// organization sorts with the organization's list without being able to
// read the organization.
func (b *Business) ForAccount(ctx context.Context, a tenancybus.Account) ([]Category, error) {
	return b.store.Of(ctx, OwnerOf(a))
}

// Of is an owner's list, asking nobody's permission: for a domain that has
// asked already, such as budgets, whose owners may hold a role on a project
// and none on the organization whose list its money is sorted with.
func (b *Business) Of(ctx context.Context, owner types.Scope) ([]Category, error) {
	return b.store.Of(ctx, owner)
}

// Create adds a category of a kind to a list. A new one always has a kind:
// only those made before kinds may lack one.
func (b *Business) Create(ctx context.Context, now time.Time, actor types.ID, owner types.Scope, name string, kind Kind) (Category, error) {
	if _, err := b.require(ctx, actor, owner, tenancybus.Bookkeep); err != nil {
		return Category{}, err
	}

	name, err := cleanName(name)
	if err != nil {
		return Category{}, err
	}

	if !kind.Valid() {
		return Category{}, badKind
	}

	c := Category{ID: types.NewID(), Owner: owner, Name: name, Kind: kind, CreatedBy: actor, CreatedAt: now}

	if err := b.store.Create(ctx, c, eventbus.New(now, actor, owner, Added, map[string]string{"name": name, "kind": string(kind)})); err != nil {
		return Category{}, err
	}

	return c, nil
}

// one is a category and the check that the actor may keep its list.
func (b *Business) one(ctx context.Context, actor, id types.ID) (Category, error) {
	c, err := b.store.ByID(ctx, id)
	if err != nil {
		return Category{}, err
	}

	if _, err := b.require(ctx, actor, c.Owner, tenancybus.Bookkeep); err != nil {
		return Category{}, err
	}

	return c, nil
}

// Rename changes a category's name, everywhere it is used: a split names
// the category, not the words. A name that will not do comes back with the
// category as it was, so that a page can return to its list.
func (b *Business) Rename(ctx context.Context, now time.Time, actor, id types.ID, name string) (Category, error) {
	c, err := b.one(ctx, actor, id)
	if err != nil {
		return Category{}, err
	}

	name, err = cleanName(name)
	if err != nil {
		return c, err
	}

	if name == c.Name {
		return c, nil
	}

	from := c.Name
	c.Name = name

	return c, b.store.Update(ctx, c, eventbus.New(now, actor, c.Owner, Renamed, map[string]string{"from": from, "name": name}))
}

// SetArchived takes a category off the choices, or puts it back.
func (b *Business) SetArchived(ctx context.Context, now time.Time, actor, id types.ID, archived bool) (Category, error) {
	c, err := b.one(ctx, actor, id)
	if err != nil {
		return Category{}, err
	}

	if c.Archived() == archived {
		return c, nil
	}

	action := Restored

	c.ArchivedAt = time.Time{}
	if archived {
		c.ArchivedAt, action = now, Archived
	}

	return c, b.store.Update(ctx, c, eventbus.New(now, actor, c.Owner, action, map[string]string{"name": c.Name}))
}

// badKind is a kind that is not one of Kinds, worded by the pages as
// tenancybus.Invalid is.
var badKind = tenancybus.Invalid{Field: "category-kind", Err: errors.New("choose income, an expense, a transfer or pass-through")}

// SetKind says what kind of money a category is. The owner's to do, not a
// bookkeeper's: it moves every part ever sorted into the category between
// income, expenses and the lines outside them, last year's included, which
// is a decision about the books rather than about one charge.
func (b *Business) SetKind(ctx context.Context, now time.Time, actor, id types.ID, kind Kind) (Category, error) {
	c, err := b.store.ByID(ctx, id)
	if err != nil {
		return Category{}, err
	}

	if _, err := b.require(ctx, actor, c.Owner, tenancybus.Manage); err != nil {
		return Category{}, err
	}

	if !kind.Valid() {
		return c, badKind
	}

	if kind == c.Kind {
		return c, nil
	}

	from := c.Kind
	c.Kind = kind

	return c, b.store.Update(ctx, c, eventbus.New(now, actor, c.Owner, KindSet, map[string]string{"name": c.Name, "from": string(from), "kind": string(kind)}))
}

// Group is the categories of one kind.
type Group struct {
	Kind       Kind
	Categories []Category
}

// Grouped is a list a kind at a time, in the order of Kinds, with any whose
// kind is not said yet last; within a kind, as the list had them. For a
// choice of category, where the kind is the first thing a person decides.
func Grouped(list []Category) []Group {
	var out []Group

	for _, k := range append(slices.Clone(Kinds), Unsaid) {
		var g Group

		for _, c := range list {
			if c.Kind == k {
				g.Categories = append(g.Categories, c)
			}
		}

		if len(g.Categories) > 0 {
			g.Kind = k
			out = append(out, g)
		}
	}

	return out
}

// Starter is a category every new list begins with.
type Starter struct {
	Name string
	Kind Kind
}

// Start gives a new list the categories it begins with: one for transfers
// between the owner's accounts and one for pass-through, because the
// monthly card payment and the personal charge that is repaid arrive with
// the first statement, and a list without them invites sorting both as
// expenses. The names are the caller's, in the creator's language; one the
// list already has is left as it is.
func (b *Business) Start(ctx context.Context, now time.Time, actor types.ID, owner types.Scope, starters []Starter) error {
	for _, s := range starters {
		if _, err := b.Create(ctx, now, actor, owner, s.Name, s.Kind); err != nil && !errors.Is(err, ErrDuplicate) {
			return err
		}
	}

	return nil
}

// cleanName is a name with its spaces tidied, or tenancybus.Invalid, which
// the pages already word.
func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")

	if name == "" || utf8.RuneCountInString(name) > MaxName {
		return "", tenancybus.Invalid{Field: "category-name", Err: fmt.Errorf("give it a name of at most %d characters", MaxName)}
	}

	return name, nil
}
