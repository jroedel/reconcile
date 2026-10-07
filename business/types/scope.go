package types

import "fmt"

// ScopeKind is the kind of thing access is granted on and history is kept
// for: an organization, an account, or a project (docs/plan.md, "Tenancy and
// access").
type ScopeKind string

const (
	ScopeOrg     ScopeKind = "org"
	ScopeAccount ScopeKind = "account"
	ScopeProject ScopeKind = "project"
)

// ParseScopeKind reads a kind from outside -- a path segment, a stored row --
// and refuses anything it does not know. A kind written by a newer binary is
// not a kind this one may grant anything on.
func ParseScopeKind(s string) (ScopeKind, error) {
	switch k := ScopeKind(s); k {
	case ScopeOrg, ScopeAccount, ScopeProject:
		return k, nil
	}

	return "", fmt.Errorf("%q is not a kind of scope", s)
}

// Scope names one organization, account or project.
type Scope struct {
	Kind ScopeKind
	ID   ID
}

// Zero reports whether this names nothing.
func (s Scope) Zero() bool { return s.Kind == "" || s.ID.Zero() }

func (s Scope) String() string { return string(s.Kind) + ":" + s.ID.String() }

// OrgScope, AccountScope and ProjectScope name one of each.
func OrgScope(id ID) Scope     { return Scope{Kind: ScopeOrg, ID: id} }
func AccountScope(id ID) Scope { return Scope{Kind: ScopeAccount, ID: id} }
func ProjectScope(id ID) Scope { return Scope{Kind: ScopeProject, ID: id} }
