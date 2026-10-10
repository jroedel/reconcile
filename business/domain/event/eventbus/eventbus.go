// Package eventbus is the history: who changed what, and when, on an
// organization, an account or a project (docs/plan.md, "Tenancy and access").
//
// Append-only. Nothing here updates or deletes an event, and nothing should:
// an accountant asking "who moved this charge into the pilgrimage, and
// when?" is asking a question whose answer must not depend on whether
// somebody tidied up since.
//
// An event is written by the domain that made the change, in the same
// transaction as the change (eventdb.Insert), so that there is never a change
// without its line here or a line without its change. This package only
// reads them back.
package eventbus

import (
	"context"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/types"
)

// Action is what happened, as a short code a page words in the reader's
// language. A free string rather than a closed set: each domain adds its
// own as it arrives, and a page that meets one it does not know says
// something general rather than failing.
type Action string

// The actions the tenancy domain records.
const (
	Created      Action = "created"
	Renamed      Action = "renamed"
	Edited       Action = "edited"
	Archived     Action = "archived"
	Restored     Action = "restored"
	GrantAdded   Action = "grant.added"
	GrantChanged Action = "grant.changed"
	GrantRemoved Action = "grant.removed"
	GrantClaimed Action = "grant.claimed"
)

// Event is one line of history.
type Event struct {
	ID      types.ID
	ActorID types.ID // zero for the system: an invitation claimed at sign-up has no actor but the person
	Scope   types.Scope
	Action  Action

	// Detail is what a page needs to say it -- a name, a role, an address --
	// as plain strings. Not a struct per action: the history is read far
	// less than it is written, and a page reads the keys it knows.
	Detail map[string]string

	// Via is the name of the API key the change came through ("claude.ai",
	// or what a person called a key), "" for a change made on a page.
	// The actor is still the person: a key acts as them. This says how
	// they reached the change, so that the history can say "through
	// Claude" (docs/books-api.md, "Telling afterwards what the API did").
	Via string

	At time.Time
}

type viaKey struct{}

// WithVia marks a request's context as reaching the business through an
// API key with this name, for every event written while serving it.
//
// A context value rather than an argument to every business method that
// writes history: how the actor arrived is no rule's business, and
// threading it through every signature in every domain to reach one column
// is the wrong trade. The store that writes the event reads it (ViaFrom).
func WithVia(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, viaKey{}, name)
}

// ViaFrom is the key a request came through, or "".
func ViaFrom(ctx context.Context) string {
	name, _ := ctx.Value(viaKey{}).(string)

	return name
}

// New is an event with an identifier and a time.
func New(now time.Time, actor types.ID, scope types.Scope, action Action, detail map[string]string) Event {
	return Event{ID: types.NewID(), ActorID: actor, Scope: scope, Action: action, Detail: detail, At: now}
}

// Storer reads events back.
type Storer interface {
	ForScope(ctx context.Context, scope types.Scope, limit int) ([]Event, error)
}

// Business is the history, for reading.
type Business struct {
	store Storer
}

// NewBusiness constructs one.
func NewBusiness(store Storer) *Business { return &Business{store: store} }

// Recent is the newest events on one scope, newest first. Whether the
// reader may see them is the caller's question, answered before this.
func (b *Business) Recent(ctx context.Context, scope types.Scope, limit int) ([]Event, error) {
	events, err := b.store.ForScope(ctx, scope, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the history: %w", err)
	}

	return events, nil
}
