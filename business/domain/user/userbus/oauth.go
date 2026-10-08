package userbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/oauth"
)

// OAuth is a second way to hand a program an API key: instead of a person
// copying one off the keys screen into a file, the program sends them here to
// agree, and is given the key itself. It exists for Claude on claude.ai and
// its phone app, which have no file to hold a key in and can only connect to
// a server that signs in this way. The program gets nothing a key made on the
// screen would not give it -- the key is an ordinary one, listed on that
// screen and revoked from it -- so everything apikey.go says about what a key
// can do applies unchanged.
//
// A grant is the authorization code: the person agreed, and this is the
// one-time proof of it that the program trades for the key. It is a sign-in
// link in all but who carries it, and is kept the same way.
//
// # One key per program, and no refresh token
//
// Connecting a program again replaces the key it was given before rather than
// adding one: a person who reconnects Claude a few times should not find the
// five-key limit used up by keys nothing holds any more.
//
// The key lasts as long as one made on the screen, ninety days, and then the
// program must ask the person again. OAuth would have a short-lived token and
// a refresh token that is swapped for a new one each time; that is a second
// credential to store, rotate and revoke, for a program that a person can
// reconnect in two taps four times a year. If the asking ever becomes a
// nuisance, a refresh token is the place to start.

const (
	// GrantLife is how long a program has to trade a code for its key. The
	// program does it the moment it has the code; the RFC asks for ten
	// minutes at most, and nothing needs more than one.
	GrantLife = 5 * time.Minute

	// maxLiveGrants is how many unspent codes one person may have. Pressing
	// "Allow" again on an old tab is the most anybody does by mistake.
	maxLiveGrants = 5
)

// The errors agreeing can give, for a page to word.
var (
	// ErrClient is a program that did not say who it is or where to send
	// the person back.
	ErrClient = errors.New("the program did not say who it is")

	// ErrChallenge is a program that did not protect its sign-in with
	// PKCE and S256.
	ErrChallenge = errors.New("the program did not use PKCE with S256")

	// ErrTooManyGrants is a person who agreed several times in a few
	// minutes.
	ErrTooManyGrants = errors.New("agreed too often; wait five minutes")
)

// Grant is one authorization code, without its secret.
type Grant struct {
	ID     types.ID
	UserID types.ID
	Hash   []byte

	// What the code is bound to: the program it was given to, the name its
	// key will have, where it was sent, and the PKCE challenge the trade
	// must answer.
	ClientID    string
	ClientName  string
	RedirectURI string
	Challenge   string

	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time // zero while unspent
}

// GrantAccess records that a person agreed to let a program act as them,
// and returns the code to send it back with. The caller has read the
// program's metadata document and checked redirect against it; this is the
// rule about what a code is bound to, not about which programs may ask.
func (b *Business) GrantAccess(ctx context.Context, now time.Time, userID types.ID, clientID, name, redirect, challenge string) (string, error) {
	name = keyName(name)

	switch {
	case clientID == "", redirect == "":
		return "", ErrClient
	case !oauth.ValidChallenge(challenge):
		return "", ErrChallenge
	}

	cred := mintCredential()

	made, err := b.store.CreateGrant(ctx, Grant{
		ID: cred.id, UserID: userID, Hash: cred.hash,
		ClientID: clientID, ClientName: name, RedirectURI: redirect, Challenge: challenge,
		CreatedAt: now, ExpiresAt: now.Add(GrantLife),
	}, maxLiveGrants)

	switch {
	case err != nil:
		return "", fmt.Errorf("saving the OAuth grant: %w", err)
	case !made:
		return "", ErrTooManyGrants
	}

	b.log.Info("OAuth access granted", "user_id", userID.String(), "grant_id", cred.id.String(), "client_id", clientID)

	return cred.String(), nil
}

// RedeemGrant trades a code for a key, once. Every refusal is ErrDenied, as
// for a link: the token endpoint's answer to all of them is invalid_grant,
// and the log says which it was.
//
// clientID and redirect must be what the code was given for, and verifier
// must answer its challenge: a code stolen on its way back to the program is
// worth nothing to whoever stole it without the verifier, which never left
// the program.
func (b *Business) RedeemGrant(ctx context.Context, now time.Time, presented, clientID, redirect, verifier string) (APIKey, string, error) {
	id, secret, err := splitCredential(presented)
	if err != nil {
		return APIKey{}, "", ErrDenied
	}

	g, err := b.store.GrantByID(ctx, id)

	switch {
	case errors.Is(err, ErrNotFound):
		return APIKey{}, "", ErrDenied
	case err != nil:
		return APIKey{}, "", fmt.Errorf("reading the OAuth grant: %w", err)
	}

	if !verifySecret(g.Hash, secret) {
		return APIKey{}, "", ErrDenied
	}

	switch {
	case !g.UsedAt.IsZero():
		b.log.Warn("an OAuth code was presented twice", "grant_id", g.ID.String(), "user_id", g.UserID.String())

		return APIKey{}, "", ErrDenied
	case !now.Before(g.ExpiresAt):
		return APIKey{}, "", ErrDenied
	case g.ClientID != clientID, g.RedirectURI != redirect:
		b.log.Warn("an OAuth code was presented by another program, or for another redirect", "grant_id", g.ID.String(), "client_id", clientID)

		return APIKey{}, "", ErrDenied
	case !oauth.VerifyPKCE(g.Challenge, verifier):
		b.log.Warn("an OAuth code was presented with the wrong PKCE verifier", "grant_id", g.ID.String())

		return APIKey{}, "", ErrDenied
	}

	// The claim, after every check, so that a wrong verifier does not spend
	// the code: a program with a bug may try again, and a thief without the
	// verifier gets nowhere however often they try.
	claimed, err := b.store.UseGrant(ctx, g.ID, now)

	switch {
	case err != nil:
		return APIKey{}, "", fmt.Errorf("spending the OAuth grant: %w", err)
	case !claimed:
		return APIKey{}, "", ErrDenied
	}

	u, err := b.store.UserByID(ctx, g.UserID)

	switch {
	case errors.Is(err, ErrNotFound):
		return APIKey{}, "", ErrDenied
	case err != nil:
		return APIKey{}, "", fmt.Errorf("reading the person: %w", err)
	case !u.Enabled:
		return APIKey{}, "", ErrDenied
	}

	cred := mintCredential()
	k := APIKey{
		ID: cred.id, UserID: u.ID, Name: g.ClientName, Hash: cred.hash,
		CreatedAt: now, ExpiresAt: now.Add(APIKeyLife), Client: g.ClientID,
	}

	made, err := b.store.ReplaceAPIKey(ctx, k, MaxAPIKeys)

	switch {
	case err != nil:
		return APIKey{}, "", fmt.Errorf("saving the API key: %w", err)
	case !made:
		// The person has five keys and none of them is this program's.
		// The token endpoint can only say invalid_grant, so the log is
		// where this is found; the person sees the program fail to
		// connect, and the keys screen shows why.
		b.log.Warn("an OAuth code could not become a key: the person has the most keys allowed", "user_id", u.ID.String(), "client_id", g.ClientID)

		return APIKey{}, "", ErrDenied
	}

	b.log.Info("API key given through OAuth", "user_id", u.ID.String(), "key_id", k.ID.String(), "client_id", g.ClientID)

	return k, APIKeyPrefix + cred.String(), nil
}

// keyName fits a program's name to a key's: one line, at most MaxKeyName
// characters, and something rather than nothing.
func keyName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "A program"
	}

	if utf8.RuneCountInString(name) > MaxKeyName {
		name = string([]rune(name)[:MaxKeyName-1]) + "…"
	}

	return name
}
