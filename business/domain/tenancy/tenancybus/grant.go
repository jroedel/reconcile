package tenancybus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/domain/event/eventbus"
	"github.com/jroedel/reconcile/business/types"
)

// People is who holds a role on a scope: the grants on it, and for an
// account or project in an organization, the organization's grants, which
// hold here too. Anybody who can read the scope may see who else can.
func (b *Business) People(ctx context.Context, actor types.ID, scope types.Scope) (direct, inherited []Grant, err error) {
	if _, err := b.require(ctx, actor, scope, Read); err != nil {
		return nil, nil, err
	}

	direct, err = b.store.GrantsOn(ctx, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("reading who has a role here: %w", err)
	}

	parent, err := b.parent(ctx, scope)
	if err != nil || parent.Zero() {
		return direct, nil, err
	}

	inherited, err = b.store.GrantsOn(ctx, parent)
	if err != nil {
		return nil, nil, fmt.Errorf("reading who has a role in the organization: %w", err)
	}

	return direct, inherited, nil
}

// Grant gives a role on a scope to an email address. If somebody already
// signs in with the address the grant is theirs at once; otherwise it waits
// for them to sign up. The bool reports which.
//
// The caller sends the invitation. Whether to say "sign in" or "make an
// account" in it is the bool's job, and nothing about the answer is new to
// the granter: they will see it on the people list the moment they look.
func (b *Business) Grant(ctx context.Context, now time.Time, actor types.ID, scope types.Scope, email types.Email, role Role) (Grant, bool, error) {
	if _, err := b.require(ctx, actor, scope, Manage); err != nil {
		return Grant{}, false, err
	}

	if _, err := ParseRole(string(role)); err != nil {
		return Grant{}, false, err
	}

	if email.Zero() {
		return Grant{}, false, Invalid{Field: "email", Err: errors.New("an email address")}
	}

	userID, known, err := b.users.UserIDByEmail(ctx, email)
	if err != nil {
		return Grant{}, false, fmt.Errorf("looking the address up: %w", err)
	}

	g := Grant{ID: types.NewID(), Scope: scope, Role: role, GrantedBy: actor, CreatedAt: now}
	if known {
		g.UserID = userID
	} else {
		g.Email = email
	}

	detail := map[string]string{"role": string(role), "email": email.String()}

	if err := b.store.AddGrant(ctx, g, eventbus.New(now, actor, scope, eventbus.GrantAdded, detail)); err != nil {
		return Grant{}, false, err
	}

	b.log.Info("a role was granted", "scope", scope.String(), "grant_id", g.ID.String(), "role", role, "by", actor.String())

	return g, known, nil
}

// grantOn is one grant, checked to be on the scope the caller says, so that
// a form posted to one organization's page cannot change a grant on
// another's.
func (b *Business) grantOn(ctx context.Context, scope types.Scope, id types.ID) (Grant, error) {
	g, err := b.store.GrantByID(ctx, id)
	if err != nil {
		return Grant{}, err
	}

	if g.Scope != scope {
		return Grant{}, ErrNotFound
	}

	return g, nil
}

// needsOwner reports whether a scope must always keep an owner of its own:
// an organization, or a personal account or project. One inside an
// organization has the organization's owners, and its own may all go.
func (b *Business) needsOwner(ctx context.Context, scope types.Scope) (bool, error) {
	parent, err := b.parent(ctx, scope)

	return parent.Zero(), err
}

// ChangeRole changes the role on a grant. Taking the last owner's ownership
// away is refused with ErrLastOwner, including the owner's own: hand it to
// somebody else first.
func (b *Business) ChangeRole(ctx context.Context, now time.Time, actor types.ID, scope types.Scope, grantID types.ID, role Role) (Grant, error) {
	if _, err := b.require(ctx, actor, scope, Manage); err != nil {
		return Grant{}, err
	}

	if _, err := ParseRole(string(role)); err != nil {
		return Grant{}, err
	}

	g, err := b.grantOn(ctx, scope, grantID)
	if err != nil {
		return Grant{}, err
	}

	if g.Role == role {
		return g, nil
	}

	keep, err := b.needsOwner(ctx, scope)
	if err != nil {
		return Grant{}, err
	}

	detail := map[string]string{"from": string(g.Role), "role": string(role), "who": who(g)}
	g.Role = role

	if err := b.store.ChangeGrant(ctx, g, keep && role != Owner, eventbus.New(now, actor, scope, eventbus.GrantChanged, detail)); err != nil {
		return Grant{}, err
	}

	return g, nil
}

// RemoveGrant takes a grant away, or withdraws an invitation nobody has
// claimed. Removing the last owner is refused with ErrLastOwner.
func (b *Business) RemoveGrant(ctx context.Context, now time.Time, actor types.ID, scope types.Scope, grantID types.ID) error {
	if _, err := b.require(ctx, actor, scope, Manage); err != nil {
		return err
	}

	g, err := b.grantOn(ctx, scope, grantID)
	if err != nil {
		return err
	}

	keep, err := b.needsOwner(ctx, scope)
	if err != nil {
		return err
	}

	detail := map[string]string{"role": string(g.Role), "who": who(g)}

	return b.store.RemoveGrant(ctx, g, keep, eventbus.New(now, actor, scope, eventbus.GrantRemoved, detail))
}

// Claim makes every grant waiting on an address its user's. Called whenever
// somebody signs in, and when they move their account to a new address --
// every time, not only the first, because an invitation can arrive between
// a person signing up and the page that would have claimed it.
//
// A grant waiting on a scope where the user already holds one is dropped,
// and the role they have is kept: the one they hold was given knowing who
// they are, and the invitation was not.
func (b *Business) Claim(ctx context.Context, now time.Time, userID types.ID, email types.Email) (int, error) {
	n, err := b.store.ClaimGrants(ctx, now, userID, email)
	if err != nil {
		return 0, fmt.Errorf("claiming the roles waiting for that address: %w", err)
	}

	if n > 0 {
		b.log.Info("waiting roles were claimed", "user_id", userID.String(), "count", n)
	}

	return n, nil
}

// who is what the history says about whose grant it was: their user ID for
// a claimed grant, their address for one still waiting.
func who(g Grant) string {
	if g.Pending() {
		return g.Email.String()
	}

	return g.UserID.String()
}
