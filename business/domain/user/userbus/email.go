package userbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/reconcile/business/types"
)

// emailChangeLife is how long the code sent to a new address is good for. The
// same fifteen minutes as signing in: the person is on the page that asked,
// waiting for it.
const emailChangeLife = 15 * time.Minute

// EmailChange is an address somebody has asked to move to, and has not yet
// proved they can read.
//
// Proved by a code sent to the new address and typed into the page that
// asked, bound to that browser exactly as a sign-in code is (see [Token]).
// mass-intentions sends a link instead; a code here, for the reason it signs
// in with one -- the link opens in whichever browser the phone prefers.
type EmailChange struct {
	ID       types.ID
	UserID   types.ID
	NewEmail types.Email
	Hash     secretHash

	CreatedAt time.Time
	ExpiresAt time.Time

	UsedAt      time.Time
	CancelledAt time.Time
	Attempts    int
}

// Live reports whether it can still be confirmed.
func (c EmailChange) Live(now time.Time) bool {
	return c.UsedAt.IsZero() && c.CancelledAt.IsZero() && now.Before(c.ExpiresAt)
}

// EmailChangeRequest is what the caller needs to send the code and to ask for
// it back.
type EmailChangeRequest struct {
	User     User
	NewEmail types.Email
	Code     string

	// Pending is the browser's half, kept in a cookie until the code comes
	// back, as for signing in.
	Pending string
}

// ErrSameAddress is a change to the address already in use.
var ErrSameAddress = errors.New("that is already your address")

// ErrAddressTaken is a change to an address another user signs in with.
//
// Said plainly. The fact leaks anyway the moment they try to sign in with it,
// and accepting it only to fail at the code would spend their time for
// nothing.
var ErrAddressTaken = errors.New("another user already signs in with that address")

// RequestEmailChange starts a move to a new address.
//
// Nothing changes yet. With no passwords the address *is* the credential, so
// a session that could rewrite it outright could take the user for good;
// proving the new address can be read closes that, and catches a typo, which
// is the ordinary case.
func (b *Business) RequestEmailChange(ctx context.Context, now time.Time, userID types.ID, addr types.Email) (EmailChangeRequest, error) {
	u, err := b.store.UserByID(ctx, userID)
	if err != nil {
		return EmailChangeRequest{}, fmt.Errorf("that user could not be read: %w", err)
	}

	if u.Email == addr {
		return EmailChangeRequest{}, ErrSameAddress
	}

	switch _, err := b.store.UserByEmail(ctx, addr); {
	case err == nil:
		return EmailChangeRequest{}, ErrAddressTaken
	case !errors.Is(err, ErrNotFound):
		return EmailChangeRequest{}, fmt.Errorf("the users could not be read: %w", err)
	}

	// Anything outstanding is withdrawn first, so that somebody who mistypes
	// and corrects themselves has one live code and not two.
	if err := b.store.CancelEmailChanges(ctx, u.ID, now); err != nil {
		return EmailChangeRequest{}, err
	}

	cred := mintCredential()

	code, err := mintCode()
	if err != nil {
		return EmailChangeRequest{}, fmt.Errorf("a code could not be made: %w", err)
	}

	if err := b.store.CreateEmailChange(ctx, EmailChange{
		ID: cred.id, UserID: u.ID, NewEmail: addr, Hash: hashSecret(cred.secret + code),
		CreatedAt: now, ExpiresAt: now.Add(emailChangeLife),
	}); err != nil {
		return EmailChangeRequest{}, err
	}

	b.log.Info("an address change was requested", "user_id", u.ID.String())

	return EmailChangeRequest{User: u, NewEmail: addr, Code: code, Pending: cred.String()}, nil
}

// PendingEmailChange is the address a user is waiting to move to.
func (b *Business) PendingEmailChange(ctx context.Context, now time.Time, userID types.ID) (types.Email, bool, error) {
	c, err := b.store.PendingEmailChange(ctx, userID, now)

	switch {
	case errors.Is(err, ErrNotFound):
		return types.Email{}, false, nil
	case err != nil:
		return types.Email{}, false, err
	}

	return c.NewEmail, true, nil
}

// CancelEmailChange withdraws whatever the user is waiting on.
func (b *Business) CancelEmailChange(ctx context.Context, now time.Time, userID types.ID) error {
	return b.store.CancelEmailChanges(ctx, userID, now)
}

// ConfirmEmailChange redeems the code sent to the new address and moves the
// user, returning them as they now stand and the address they had.
//
// The change must be the signed-in user's own: a pending cookie carried to
// somebody else's session moves nobody. A try is counted before the code is
// compared, as for signing in.
func (b *Business) ConfirmEmailChange(ctx context.Context, now time.Time, userID types.ID, pending, typed string) (User, types.Email, error) {
	id, secret, err := splitCredential(pending)
	if err != nil {
		return User{}, types.Email{}, ErrDenied
	}

	code, ok := normaliseCode(typed)
	if !ok {
		return User{}, types.Email{}, ErrDenied
	}

	c, err := b.store.EmailChangeByID(ctx, id)
	if err != nil || c.UserID != userID || !c.Live(now) {
		return User{}, types.Email{}, ErrDenied
	}

	tried, err := b.store.TryEmailChange(ctx, c.ID, now, codeTries)
	switch {
	case err != nil:
		return User{}, types.Email{}, fmt.Errorf("the code could not be checked: %w", err)
	case !tried:
		return User{}, types.Email{}, ErrDenied
	}

	if !verifySecret(c.Hash, secret+code) {
		return User{}, types.Email{}, ErrDenied
	}

	u, err := b.store.UserByID(ctx, c.UserID)
	if err != nil {
		return User{}, types.Email{}, fmt.Errorf("that user could not be read: %w", err)
	}

	// Checked again, because fifteen minutes is long enough for somebody else
	// to have signed up with it, and the unique index would otherwise refuse
	// with a storage error where a sentence is wanted.
	switch other, err := b.store.UserByEmail(ctx, c.NewEmail); {
	case err == nil && other.ID != u.ID:
		return User{}, types.Email{}, ErrAddressTaken
	case err != nil && !errors.Is(err, ErrNotFound):
		return User{}, types.Email{}, fmt.Errorf("the users could not be read: %w", err)
	}

	// Spent before the user is written: the other way round, two requests
	// racing the same code would both write.
	spent, err := b.store.UseEmailChange(ctx, c.ID, now)
	if err != nil {
		return User{}, types.Email{}, err
	}

	if !spent {
		return User{}, types.Email{}, ErrDenied
	}

	old := u.Email
	u.Email = c.NewEmail
	u.UpdatedAt = now

	if err := b.store.UpdateUser(ctx, u); err != nil {
		return User{}, types.Email{}, fmt.Errorf("the address could not be changed: %w", err)
	}

	b.log.Info("an address change was confirmed", "user_id", u.ID.String())

	return u, old, nil
}
