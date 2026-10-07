package types

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// ID is an opaque identifier: 16 random bytes, written as 32 lowercase hex
// characters.
//
// Random rather than sequential, taken from mass-intentions. A sequential id
// tells anybody holding one how many exist and lets them ask for the next.
// Here it is also what everything attaches to -- a receipt to a transaction,
// a grant to a user whose email address may change -- so it must never
// change.
//
// Not a UUID. A UUIDv4 carries 122 bits of randomness in 36 characters with
// four hyphens and a version nibble that says nothing useful here, and it
// would be a dependency or a hand-rolled layout. This is 128 bits in 32
// characters with a parser that refuses everything else.
//
// An ID is safe to log and safe to show. It identifies a row; it never
// authorises anything.
type ID struct {
	s string
}

// ErrNotAnID is what every parse failure wraps.
var ErrNotAnID = errors.New("not an identifier")

const idLen = 32

// NewID returns a fresh random identifier.
//
// No error to return: crypto/rand.Read is documented never to return one, and
// to crash the program irrecoverably rather than hand back short or
// predictable bytes. An `(ID, error)` signature here would be a nil check at
// every call site guarding a branch that cannot be taken.
func NewID() ID {
	var b [idLen / 2]byte
	rand.Read(b[:])

	return ID{s: hex.EncodeToString(b[:])}
}

// ParseID reads an identifier that arrived from outside -- a URL path, a form
// field, a database column.
//
// Strict about case as well as alphabet. hex.DecodeString accepts upper case,
// so "AB…" and "ab…" would decode to the same bytes but compare unequal as
// strings, and a value used as a map key and a database key must have exactly
// one spelling.
func ParseID(s string) (ID, error) {
	if len(s) != idLen {
		return ID{}, fmt.Errorf("%w: it is %d characters, not %d", ErrNotAnID, len(s), idLen)
	}

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return ID{}, fmt.Errorf("%w: %q is not one", ErrNotAnID, s)
		}
	}

	return ID{s: s}, nil
}

// String is the identifier as it appears everywhere.
func (i ID) String() string { return i.s }

// Zero reports whether this is the unset ID. An ID from NewID or ParseID never
// is, so this distinguishes "no row" from "some row".
func (i ID) Zero() bool { return i.s == "" }
