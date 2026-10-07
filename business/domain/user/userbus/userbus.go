// Package userbus holds the people who use the app and how somebody proves
// they are one of them.
//
// Sign-in is by a six-digit code emailed to the address, with backup codes for
// when mail is not working and a one-time bootstrap secret for the very first
// sign-in. There is no password: the mailbox is the factor, and it is one the
// person already keeps. Adapted from mass-intentions, which had every account
// made by an administrator; here sign-up is open (docs/plan.md), and that
// changes three things, each said where it is done:
//
//   - A code is asked for by an address, not by an account. The account is
//     made when a correct code comes back, so an address nobody can read never
//     becomes one.
//   - The page after asking says the same thing for every address, as before,
//     but now every address is sent a code -- there is no "address with no
//     account" to stay silent about. The one that is not sent one is a
//     disabled account.
//   - Anybody can make this service send mail to any address, so there is a
//     ceiling on how many codes go out in an hour, across every address, on top
//     of the five live codes per address. Without it the sign-in form is a way
//     to mail strangers.
//
// # What this package refuses to tell anybody
//
// Every failed attempt returns [ErrDenied] and nothing else: not "that code
// expired", not "wrong code", not "out of tries". The app layer cannot leak
// which half was wrong, because it is never told.
//
// # Who is a user, and what a user may see
//
// A user is a person who can sign in, keyed by an ID that never changes. The
// address can change (email.go), and everything else in the app refers to the
// ID. What a user may see is not here at all: access to organisations,
// accounts and projects is granted per thing, by email, in its own domain
// (docs/plan.md, "Tenancy and access"). SiteAdmin is the one exception, and it
// grants the user list and nothing else -- no organisation's money.
package userbus

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// SessionLife is exported so that a test can assert the boundary without
// writing the number down a second time.
const SessionLife = 90 * 24 * time.Hour

// How long each credential lasts, and how many there may be.
const (
	// Long enough to open the mail and come back, short enough that a code
	// left in an inbox is not a standing key.
	signInLife = 15 * time.Minute

	// codeTries is how many wrong codes one request may have typed against
	// it before it is dead. Five covers somebody who misreads a digit twice
	// and is a one-in-two-hundred-thousand chance for anybody guessing.
	codeTries = 5

	// liveCodes is how many unexpired codes one address may have at once.
	// Every request is another five guesses, so without this the limit
	// above limits nothing; and a person whose inbox fills with codes they
	// did not ask for finds out that way.
	liveCodes = 5

	// CodesPerHour is how many codes the whole service sends in an hour, to
	// every address together. Open sign-up means anybody can type anybody's
	// address, so this is what stops the form being used to mail strangers
	// by the thousand -- and a mail server that sends that much is soon a
	// mail server nobody's inbox accepts, which would end sign-in for
	// everybody. Generous for a service of this size: a busy hour of real
	// sign-ins is a few dozen. When it is reached, the page says the same as
	// ever and the log says why nothing was sent.
	CodesPerHour = 200
)

// The errors this package returns. Everything a stranger can provoke collapses
// into ErrDenied on purpose.
var (
	// ErrDenied is every failed attempt to prove an identity.
	ErrDenied = errors.New("that did not work")

	// ErrNotFound is a lookup by identifier that the caller made, rather than
	// a credential a stranger presented.
	ErrNotFound = errors.New("no such user")
)

// User is somebody who can sign in.
//
// Enabled is a positive field rather than a Disabled one, so that the zero
// User -- the thing returned alongside every error in this package -- is
// somebody who cannot sign in and cannot do anything.
type User struct {
	ID    types.ID
	Email types.Email
	Name  string

	// Lang is the language they chose on their profile, or "" for none: the
	// page then follows the browser.
	Lang types.Lang

	// SiteAdmin may see the list of users and disable one. It grants no
	// organisation's data; see the package comment.
	SiteAdmin bool

	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Named is what to call somebody: their name, or their address before they
// have given one.
func (u User) Named() string {
	if strings.TrimSpace(u.Name) != "" {
		return u.Name
	}

	return u.Email.String()
}

// Token is one request to sign in: a six-digit code sent by mail, bound to the
// browser that asked for it.
//
// # Why a code and not a link
//
// A link opens wherever the mail program decides, and on an iPhone that is
// Safari, while somebody who added the app to the Home Screen is signing in
// from there -- so a link signs in the wrong one, for ever. A code is typed
// into the page that asked for it, whatever that page is running in.
//
// # Why the code is bound to the browser
//
// Six digits are a million possibilities, and a stolen hash of one is reversed
// in well under a second. So the code is never hashed on its own: the browser
// that asked holds a credential of the ordinary kind, an identifier and a
// 128-bit secret, in a cookie, and Hash is over that secret and the code
// together. A copy of this table is then as useless as the sessions table.
//
// # Why it names an address
//
// Sign-up is open, so a code is often for an address nobody has signed in
// with yet. Email is the address it was sent to; UserID is the user who held
// it then, or zero when nobody did, and the user is made when the code comes
// back right. When UserID is set it wins over Email: a user who changed their
// address in the fifteen minutes since still signs in as themselves.
type Token struct {
	ID        types.ID
	Email     types.Email
	UserID    types.ID
	Hash      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time // zero means unused

	// Attempts is how many codes have been typed against it, right or wrong.
	// At [codeTries] it is dead, however much time it has left.
	Attempts int
}

// Session is a signed-in browser.
type Session struct {
	ID        types.ID
	UserID    types.ID
	Hash      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// BackupCode is one of the codes issued for when mail is not working.
type BackupCode struct {
	ID        types.ID
	UserID    types.ID
	Hash      []byte
	CreatedAt time.Time
	UsedAt    time.Time // zero means unused
}

// Storer is what this package needs from storage.
//
// Several of these return a bool for "somebody else got there first", and
// that is the important part of the interface. UseToken, TryToken,
// ClaimCode, UseBackupCode, ClaimBootstrap and their email-change twins must
// each be one statement -- an UPDATE or INSERT whose WHERE is the check --
// because a read followed by a write lets two requests both see an unused
// credential and both succeed.
type Storer interface {
	CreateUser(ctx context.Context, u User) error
	UpdateUser(ctx context.Context, u User) error
	UserByID(ctx context.Context, id types.ID) (User, error)
	UserByEmail(ctx context.Context, email types.Email) (User, error)
	Users(ctx context.Context) ([]User, error)

	// ClaimCode stores a sign-in token if, and only if, the address has fewer
	// than perAddress live tokens and the service has sent fewer than perHour
	// since hourAgo. One INSERT ... SELECT ... WHERE, so a burst cannot all
	// pass a count that only some of them should.
	ClaimCode(ctx context.Context, t Token, now time.Time, perAddress int, hourAgo time.Time, perHour int) (bool, error)
	TokenByID(ctx context.Context, id types.ID) (Token, error)
	TryToken(ctx context.Context, id types.ID, now time.Time, limit int) (bool, error)
	UseToken(ctx context.Context, id types.ID, at time.Time) (bool, error)

	ReplaceBackupCodes(ctx context.Context, userID types.ID, codes []BackupCode) error
	BackupCodes(ctx context.Context, userID types.ID) ([]BackupCode, error)
	UseBackupCode(ctx context.Context, id types.ID, at time.Time) (bool, error)

	CreateSession(ctx context.Context, s Session) error
	SessionByID(ctx context.Context, id types.ID) (Session, error)
	DeleteSession(ctx context.Context, id types.ID) error
	DeleteUserSessions(ctx context.Context, userID types.ID) error

	ClaimBootstrap(ctx context.Context, at time.Time) (bool, error)
	BootstrapSpent(ctx context.Context) (bool, error)

	CreateEmailChange(ctx context.Context, c EmailChange) error
	EmailChangeByID(ctx context.Context, id types.ID) (EmailChange, error)
	PendingEmailChange(ctx context.Context, userID types.ID, now time.Time) (EmailChange, error)
	TryEmailChange(ctx context.Context, id types.ID, now time.Time, limit int) (bool, error)
	UseEmailChange(ctx context.Context, id types.ID, at time.Time) (bool, error)
	CancelEmailChanges(ctx context.Context, userID types.ID, at time.Time) error

	PruneExpired(ctx context.Context, before time.Time) error
}

// Business is the set of operations on users.
type Business struct {
	log   *slog.Logger
	store Storer
}

// NewBusiness constructs one.
func NewBusiness(log *slog.Logger, store Storer) *Business {
	return &Business{log: log, store: store}
}

// ByID returns a user.
func (b *Business) ByID(ctx context.Context, id types.ID) (User, error) {
	return b.store.UserByID(ctx, id)
}

// UserIDByEmail is who signs in with an address, if anybody does: what a
// grant to that address needs to know, to go straight to them rather than
// wait.
func (b *Business) UserIDByEmail(ctx context.Context, email types.Email) (types.ID, bool, error) {
	u, err := b.store.UserByEmail(ctx, email)

	switch {
	case errors.Is(err, ErrNotFound):
		return types.ID{}, false, nil
	case err != nil:
		return types.ID{}, false, err
	}

	return u.ID, true, nil
}

// All returns every user, for the site administrator's list.
func (b *Business) All(ctx context.Context) ([]User, error) {
	return b.store.Users(ctx)
}

// SignInRequest is the result of asking for a sign-in code.
//
// Pending is always set, and Code is empty when nothing is to be sent. The
// caller does the same thing either way -- set the cookie, render the page
// with a box for the code -- which is why this is a struct with an empty
// field rather than an error: an error would tempt a handler into saying
// something different. A Pending that names no token is refused when the code
// comes back, exactly as a wrong code is.
type SignInRequest struct {
	Email types.Email

	// Code is the six digits to mail. Empty means: send nothing, say the
	// same thing.
	Code string

	// Pending is what the asking browser keeps, in a cookie, until the code
	// comes back. It is half of the credential and the code is the other.
	Pending string
}

// Sendable reports whether there is a code to mail.
func (r SignInRequest) Sendable() bool { return r.Code != "" }

// RequestSignIn mints a sign-in code for an address.
//
// No error for a disabled user, an address at its five live codes, or a
// service at its hourly ceiling: the caller sends mail only when Sendable, and
// renders the same page whatever happens.
func (b *Business) RequestSignIn(ctx context.Context, now time.Time, email types.Email) (SignInRequest, error) {
	cred := mintCredential()
	nothing := SignInRequest{Email: email, Pending: cred.String()}

	u, err := b.store.UserByEmail(ctx, email)

	switch {
	case errors.Is(err, ErrNotFound):
		// Somebody new, which is ordinary here. The token names the address
		// and no user; the user is made when the code comes back.
	case err != nil:
		return SignInRequest{}, fmt.Errorf("the users could not be read: %w", err)
	case !u.Enabled:
		b.log.Info("sign-in requested for a disabled user", "user_id", u.ID.String())

		return nothing, nil
	}

	code, err := mintCode()
	if err != nil {
		return SignInRequest{}, fmt.Errorf("a sign-in code could not be made: %w", err)
	}

	t := Token{
		ID:        cred.id,
		Email:     email,
		UserID:    u.ID,
		Hash:      hashSecret(cred.secret + code),
		CreatedAt: now,
		ExpiresAt: now.Add(signInLife),
	}

	claimed, err := b.store.ClaimCode(ctx, t, now, liveCodes, now.Add(-time.Hour), CodesPerHour)
	switch {
	case err != nil:
		return SignInRequest{}, fmt.Errorf("the sign-in code could not be saved: %w", err)
	case !claimed:
		// Warn: an address at five live codes is somebody pressing the
		// button over and over, somebody guessing, or the hourly ceiling --
		// and the third means somebody is using the form to mail strangers.
		b.log.Warn("a sign-in code was not sent: the address has five outstanding, or the service has sent its hourly ceiling",
			"email", email.String(), "per_hour", CodesPerHour)

		return nothing, nil
	}

	return SignInRequest{Email: email, Code: code, Pending: cred.String()}, nil
}

// SignIn redeems a sign-in code and returns a session credential, making the
// user first if this is the first time the address has signed in. The bool
// reports whether it was.
//
// A try is counted before the code is compared, and in one statement, so that
// a burst of guesses sent at once still costs one try each.
func (b *Business) SignIn(ctx context.Context, now time.Time, pending, typed string) (User, string, bool, error) {
	id, secret, err := splitCredential(pending)
	if err != nil {
		return User{}, "", false, ErrDenied
	}

	code, ok := normaliseCode(typed)
	if !ok {
		return User{}, "", false, ErrDenied
	}

	t, err := b.store.TokenByID(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, "", false, ErrDenied
	case err != nil:
		return User{}, "", false, fmt.Errorf("the sign-in code could not be read: %w", err)
	}

	tried, err := b.store.TryToken(ctx, t.ID, now, codeTries)
	switch {
	case err != nil:
		return User{}, "", false, fmt.Errorf("the sign-in code could not be checked: %w", err)
	case !tried:
		return User{}, "", false, ErrDenied
	}

	if !verifySecret(t.Hash, secret+code) {
		if t.Attempts+1 >= codeTries {
			b.log.Warn("a sign-in code ran out of tries", "token_id", t.ID.String())
		}

		return User{}, "", false, ErrDenied
	}

	claimed, err := b.store.UseToken(ctx, t.ID, now)
	switch {
	case err != nil:
		return User{}, "", false, fmt.Errorf("the sign-in code could not be spent: %w", err)
	case !claimed:
		b.log.Warn("two requests raced for one sign-in code", "token_id", t.ID.String())

		return User{}, "", false, ErrDenied
	}

	u, created, err := b.holder(ctx, now, t)
	if err != nil {
		return User{}, "", false, err
	}

	user, session, err := b.start(ctx, now, u)

	return user, session, created, err
}

// holder is the user a spent token signs in, made now if the address has
// never signed in before.
func (b *Business) holder(ctx context.Context, now time.Time, t Token) (User, bool, error) {
	if !t.UserID.Zero() {
		u, err := b.store.UserByID(ctx, t.UserID)
		if errors.Is(err, ErrNotFound) {
			return User{}, false, ErrDenied
		}

		return u, false, err
	}

	// Somebody may have made it in the fifteen minutes since -- a second
	// browser, or a code asked for twice and both typed. Then it is theirs.
	u, err := b.store.UserByEmail(ctx, t.Email)

	switch {
	case err == nil:
		return u, false, nil
	case !errors.Is(err, ErrNotFound):
		return User{}, false, fmt.Errorf("the users could not be read: %w", err)
	}

	u = User{ID: types.NewID(), Email: t.Email, Enabled: true, CreatedAt: now, UpdatedAt: now}

	if err := b.store.CreateUser(ctx, u); err != nil {
		// Two first sign-ins for one address at the same moment: the unique
		// index settles it, and the loser signs in as the winner.
		if sqldb.IsUniqueViolation(err) {
			u, err := b.store.UserByEmail(ctx, t.Email)

			return u, false, err
		}

		return User{}, false, fmt.Errorf("the user could not be made: %w", err)
	}

	b.log.Info("a new user signed up", "user_id", u.ID.String())

	return u, true, nil
}

// start opens a session for somebody who has just proved who they are.
func (b *Business) start(ctx context.Context, now time.Time, u User) (User, string, error) {
	if !u.Enabled {
		return User{}, "", ErrDenied
	}

	cred := mintCredential()

	if err := b.store.CreateSession(ctx, Session{
		ID: cred.id, UserID: u.ID, Hash: cred.hash, CreatedAt: now, ExpiresAt: now.Add(SessionLife),
	}); err != nil {
		return User{}, "", fmt.Errorf("the session could not be saved: %w", err)
	}

	return u, cred.String(), nil
}

// SignInWithBackupCode is the way in when mail is not working.
//
// The address is asked for as well as the code, because a code has no
// identifier in it (credential.go), so there is nothing to look it up by.
func (b *Business) SignInWithBackupCode(ctx context.Context, now time.Time, email types.Email, typed string) (User, string, error) {
	normalised := normaliseBackupCode(typed)
	if len(normalised) != backupCodeLen {
		return User{}, "", ErrDenied
	}

	u, err := b.store.UserByEmail(ctx, email)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, "", ErrDenied
	case err != nil:
		return User{}, "", fmt.Errorf("the users could not be read: %w", err)
	case !u.Enabled:
		return User{}, "", ErrDenied
	}

	codes, err := b.store.BackupCodes(ctx, u.ID)
	if err != nil {
		return User{}, "", fmt.Errorf("the backup codes could not be read: %w", err)
	}

	// Every code is checked even after a match, so that the time taken does
	// not reveal which position matched or how many remain.
	var match BackupCode

	for _, c := range codes {
		if !c.UsedAt.IsZero() {
			continue
		}

		if verifySecret(c.Hash, normalised) && match.ID.Zero() {
			match = c
		}
	}

	if match.ID.Zero() {
		b.log.Warn("a backup code did not match", "user_id", u.ID.String())

		return User{}, "", ErrDenied
	}

	claimed, err := b.store.UseBackupCode(ctx, match.ID, now)
	switch {
	case err != nil:
		return User{}, "", fmt.Errorf("the backup code could not be spent: %w", err)
	case !claimed:
		return User{}, "", ErrDenied
	}

	b.log.Warn("signed in with a backup code", "user_id", u.ID.String())

	return b.start(ctx, now, u)
}

// Bootstrap trades the one-time secret from the config file for a session as
// the site administrator, making the user if the address has none.
//
// It is how the first person gets in before mail works, and how the site gets
// an administrator at all. It works once: the claim is recorded in storage,
// so a restart does not restore it and neither does a new secret in the
// config.
func (b *Business) Bootstrap(ctx context.Context, now time.Time, configured, presented string, email types.Email) (User, string, error) {
	// Constant time, and length-checked first: ConstantTimeCompare returns 0
	// on a length mismatch, which would otherwise be a free length oracle.
	switch {
	case configured == "":
		return User{}, "", ErrDenied
	case len(presented) != len(configured):
		return User{}, "", ErrDenied
	case subtle.ConstantTimeCompare([]byte(presented), []byte(configured)) != 1:
		b.log.Warn("the bootstrap secret was presented incorrectly")

		return User{}, "", ErrDenied
	}

	claimed, err := b.store.ClaimBootstrap(ctx, now)
	switch {
	case err != nil:
		return User{}, "", fmt.Errorf("the bootstrap could not be recorded: %w", err)
	case !claimed:
		b.log.Warn("the bootstrap secret was presented after it had been spent")

		return User{}, "", ErrDenied
	}

	u, err := b.store.UserByEmail(ctx, email)

	switch {
	case errors.Is(err, ErrNotFound):
		u = User{ID: types.NewID(), Email: email, Enabled: true, CreatedAt: now, UpdatedAt: now}
		if err := b.store.CreateUser(ctx, u); err != nil {
			return User{}, "", fmt.Errorf("the first user could not be made: %w", err)
		}
	case err != nil:
		return User{}, "", fmt.Errorf("the users could not be read: %w", err)
	case !u.Enabled:
		// Spent all the same: a secret presented for a disabled user is a
		// secret that has been used.
		return User{}, "", ErrDenied
	}

	u.SiteAdmin = true
	u.UpdatedAt = now

	if err := b.store.UpdateUser(ctx, u); err != nil {
		return User{}, "", fmt.Errorf("the first user could not be made the administrator: %w", err)
	}

	b.log.Warn("the bootstrap secret made a site administrator", "user_id", u.ID.String())

	return b.start(ctx, now, u)
}

// BootstrapSpent reports whether the one-time secret has been used, so that a
// page which can no longer work says so instead of collecting a secret and
// refusing it.
func (b *Business) BootstrapSpent(ctx context.Context) (bool, error) {
	return b.store.BootstrapSpent(ctx)
}

// IssueBackupCodes replaces every code a user has and returns the new ones,
// which are shown exactly once.
//
// Replaces rather than adds: somebody who has lost track of which codes they
// have needs one honest list, and any old code stops working, which is also
// what somebody who suspects a leak wants from the button.
func (b *Business) IssueBackupCodes(ctx context.Context, now time.Time, userID types.ID) ([]string, error) {
	shown := make([]string, 0, backupCodeCount)
	stored := make([]BackupCode, 0, backupCodeCount)

	for range backupCodeCount {
		code, hash := mintBackupCode()

		shown = append(shown, code)
		stored = append(stored, BackupCode{ID: types.NewID(), UserID: userID, Hash: hash, CreatedAt: now})
	}

	if err := b.store.ReplaceBackupCodes(ctx, userID, stored); err != nil {
		return nil, fmt.Errorf("the backup codes could not be saved: %w", err)
	}

	b.log.Info("issued backup codes", "user_id", userID.String())

	return shown, nil
}

// BackupCodesLeft is how many unused backup codes a user has.
func (b *Business) BackupCodesLeft(ctx context.Context, userID types.ID) (int, error) {
	codes, err := b.store.BackupCodes(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("the backup codes could not be read: %w", err)
	}

	left := 0

	for _, c := range codes {
		if c.UsedAt.IsZero() {
			left++
		}
	}

	return left, nil
}

// Authenticate turns a session cookie into the user who holds it.
//
// Called on every request, so it is one indexed read, one constant-time
// comparison and one read of the user, and it never writes: a page read that
// wrote would make every listing a writer on a single-writer database.
func (b *Business) Authenticate(ctx context.Context, now time.Time, presented string) (User, error) {
	id, secret, err := splitCredential(presented)
	if err != nil {
		return User{}, ErrDenied
	}

	s, err := b.store.SessionByID(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("the session could not be read: %w", err)
	}

	if !verifySecret(s.Hash, secret) || !now.Before(s.ExpiresAt) {
		return User{}, ErrDenied
	}

	u, err := b.store.UserByID(ctx, s.UserID)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("the user could not be read: %w", err)
	case !u.Enabled:
		// Here as well as at sign-in, so that disabling somebody ends their
		// sessions on the next request rather than in ninety days.
		return User{}, ErrDenied
	}

	return u, nil
}

// SignOut ends one session. A malformed or unknown cookie is not an error:
// the caller wanted no session, and has none.
func (b *Business) SignOut(ctx context.Context, presented string) error {
	id, _, err := splitCredential(presented)
	if err != nil {
		return nil
	}

	if err := b.store.DeleteSession(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("the session could not be ended: %w", err)
	}

	return nil
}

// SignOutEverywhere ends every session a user has, for somebody who has lost
// a phone.
func (b *Business) SignOutEverywhere(ctx context.Context, userID types.ID) error {
	if err := b.store.DeleteUserSessions(ctx, userID); err != nil {
		return fmt.Errorf("the sessions could not be ended: %w", err)
	}

	return nil
}

// MaxName is the longest name a user may give.
const MaxName = 100

// SetProfile changes a user's name and language. An empty language means
// "follow the browser".
func (b *Business) SetProfile(ctx context.Context, now time.Time, userID types.ID, name string, lang types.Lang) (User, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > MaxName {
		return User{}, fmt.Errorf("%w: a name of at most %d characters", ErrInvalid, MaxName)
	}

	if lang != "" {
		if _, err := types.ParseLang(string(lang)); err != nil {
			return User{}, fmt.Errorf("%w: a language this app speaks", ErrInvalid)
		}
	}

	u, err := b.store.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}

	u.Name, u.Lang, u.UpdatedAt = name, lang, now

	if err := b.store.UpdateUser(ctx, u); err != nil {
		return User{}, fmt.Errorf("the profile could not be saved: %w", err)
	}

	return u, nil
}

// SetEnabled lets a user sign in, or stops them. The site administrator's
// one power over somebody else's account (see the package comment).
//
// Disabling takes effect on the next request: Authenticate reads Enabled every
// time, so their open sessions stop working without being deleted, and come
// back if they are enabled again.
func (b *Business) SetEnabled(ctx context.Context, now time.Time, userID types.ID, enabled bool) (User, error) {
	u, err := b.store.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}

	u.Enabled, u.UpdatedAt = enabled, now

	if err := b.store.UpdateUser(ctx, u); err != nil {
		return User{}, fmt.Errorf("the user could not be saved: %w", err)
	}

	b.log.Warn("a user was enabled or disabled", "user_id", u.ID.String(), "enabled", enabled)

	return u, nil
}

// ErrInvalid is a value a person typed that the rules refuse. Its text, after
// the colon, says what would be accepted.
var ErrInvalid = errors.New("that needs")

// Prune deletes spent and expired credentials.
//
// Housekeeping only: every check reads the expiry, so nothing depends on this
// for correctness. A token is kept an hour past its expiry so that the
// hourly ceiling still counts it.
func (b *Business) Prune(ctx context.Context, now time.Time) error {
	if err := b.store.PruneExpired(ctx, now.Add(-time.Hour)); err != nil {
		return fmt.Errorf("pruning expired credentials: %w", err)
	}

	return nil
}
