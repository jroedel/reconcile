package categorybus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// Heading is a group of a list's categories (docs/reports.md, "Category
// groups"): "Food" over groceries, the market and meals out, so that a
// house's forty lines of a report can be read as ten. On a page it is a
// group; here a heading, because Group is already the categories of one
// kind, the way a choice of category is laid out.
//
// A heading is of one kind, as its categories are: a report's rows sit
// under the kinds, and a heading over income and an expense could sit
// under neither. Headings are for reading. Nobody sorts a charge into one;
// the split editor's choice is still the categories.
type Heading struct {
	ID    types.ID
	Owner types.Scope
	Kind  Kind
	Name  string

	CreatedBy  types.ID
	CreatedAt  time.Time
	ArchivedAt time.Time
}

// Archived reports whether it is off the choices. Its categories stay
// under it, so that last year's report still reads the way it did.
func (h Heading) Archived() bool { return !h.ArchivedAt.IsZero() }

// The actions headings write into the owner's history.
const (
	HeadingAdded    eventbus.Action = "group.added"
	HeadingRenamed  eventbus.Action = "group.renamed"
	HeadingArchived eventbus.Action = "group.archived"
	HeadingRestored eventbus.Action = "group.restored"
	PutUnder        eventbus.Action = "category.grouped"
	TakenOut        eventbus.Action = "category.ungrouped"
)

// The ways a heading can be refused, worded by the pages as
// tenancybus.Invalid is.
var (
	badHeadingKind = tenancybus.Invalid{Field: "group-kind", Err: errors.New("a category goes under a group of its own kind")}
	headingGone    = tenancybus.Invalid{Field: "group-archived", Err: errors.New("that group is archived; bring it back first")}
)

// Headings is a list's headings, archived ones last, for anyone who may
// read its owner.
func (b *Business) Headings(ctx context.Context, actor types.ID, owner types.Scope) ([]Heading, error) {
	if _, err := b.require(ctx, actor, owner, tenancybus.Read); err != nil {
		return nil, err
	}

	return b.store.HeadingsOf(ctx, owner)
}

// HeadingsOf is a list's headings, asking nobody's permission: for a
// domain that has asked already, as Of is.
func (b *Business) HeadingsOf(ctx context.Context, owner types.Scope) ([]Heading, error) {
	return b.store.HeadingsOf(ctx, owner)
}

// AddHeading adds a heading of a kind to a list: whoever may add its
// categories.
func (b *Business) AddHeading(ctx context.Context, now time.Time, actor types.ID, owner types.Scope, name string, kind Kind) (Heading, error) {
	if _, err := b.require(ctx, actor, owner, tenancybus.Bookkeep); err != nil {
		return Heading{}, err
	}

	name, err := cleanHeading(name)
	if err != nil {
		return Heading{}, err
	}

	if !kind.Valid() {
		return Heading{}, badKind
	}

	h := Heading{ID: types.NewID(), Owner: owner, Kind: kind, Name: name, CreatedBy: actor, CreatedAt: now}

	return h, b.store.CreateHeading(ctx, h, eventbus.New(now, actor, owner, HeadingAdded, map[string]string{"name": name, "kind": string(kind)}))
}

// heading is a heading and the check that the actor may keep its list.
func (b *Business) heading(ctx context.Context, actor, id types.ID) (Heading, error) {
	h, err := b.store.HeadingByID(ctx, id)
	if err != nil {
		return Heading{}, err
	}

	if _, err := b.require(ctx, actor, h.Owner, tenancybus.Bookkeep); err != nil {
		return Heading{}, err
	}

	return h, nil
}

// RenameHeading changes a heading's name. A name that will not do comes
// back with the heading as it was, so that a page can return to its list.
func (b *Business) RenameHeading(ctx context.Context, now time.Time, actor, id types.ID, name string) (Heading, error) {
	h, err := b.heading(ctx, actor, id)
	if err != nil {
		return Heading{}, err
	}

	if name, err = cleanHeading(name); err != nil {
		return h, err
	}

	if name == h.Name {
		return h, nil
	}

	from := h.Name
	h.Name = name

	return h, b.store.UpdateHeading(ctx, h, eventbus.New(now, actor, h.Owner, HeadingRenamed, map[string]string{"from": from, "name": name}))
}

// SetHeadingArchived takes a heading off the choices, or puts it back. The
// categories under it stay there.
func (b *Business) SetHeadingArchived(ctx context.Context, now time.Time, actor, id types.ID, archived bool) (Heading, error) {
	h, err := b.heading(ctx, actor, id)
	if err != nil {
		return Heading{}, err
	}

	if h.Archived() == archived {
		return h, nil
	}

	action := HeadingRestored

	h.ArchivedAt = time.Time{}
	if archived {
		h.ArchivedAt, action = now, HeadingArchived
	}

	return h, b.store.UpdateHeading(ctx, h, eventbus.New(now, actor, h.Owner, action, map[string]string{"name": h.Name}))
}

// SetHeading puts a category under a heading of its list and its kind, or
// takes it out of the one it is under when heading is zero: whoever may
// change the list's categories. A refused heading comes back with the
// category as it was.
func (b *Business) SetHeading(ctx context.Context, now time.Time, actor, categoryID, heading types.ID) (Category, error) {
	c, err := b.one(ctx, actor, categoryID)
	if err != nil {
		return Category{}, err
	}

	if heading == c.GroupID {
		return c, nil
	}

	if heading.Zero() {
		was, err := b.store.HeadingByID(ctx, c.GroupID)
		if err != nil {
			return Category{}, err
		}

		c.GroupID = types.ID{}

		return c, b.store.Update(ctx, c, eventbus.New(now, actor, c.Owner, TakenOut, map[string]string{"name": c.Name, "group": was.Name}))
	}

	h, err := b.store.HeadingByID(ctx, heading)

	switch {
	case errors.Is(err, ErrNotFound) || (err == nil && h.Owner != c.Owner):
		return c, ErrNotFound
	case err != nil:
		return Category{}, err
	case h.Kind != c.Kind:
		return c, badHeadingKind
	case h.Archived():
		return c, headingGone
	}

	c.GroupID = h.ID

	return c, b.store.Update(ctx, c, eventbus.New(now, actor, c.Owner, PutUnder, map[string]string{"name": c.Name, "group": h.Name}))
}

// MaxHeading is the longest name a heading may have.
const MaxHeading = 60

// cleanHeading is a heading's name with its spaces tidied.
func cleanHeading(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")

	if name == "" || utf8.RuneCountInString(name) > MaxHeading {
		return "", tenancybus.Invalid{Field: "group-name", Err: fmt.Errorf("give the group a name of at most %d characters", MaxHeading)}
	}

	return name, nil
}
