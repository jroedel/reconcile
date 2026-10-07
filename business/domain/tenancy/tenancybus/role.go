package tenancybus

import (
	"fmt"
	"slices"

	"github.com/jroedel/reconcile/business/types"
)

// Role is what somebody may do on an organization, an account or a project.
//
// Not a ladder. An accountant exports and a contributor uploads receipts, and
// neither can do the other's job, so "highest wins" (docs/plan.md) is read as
// "holding two roles is holding everything either allows". Each role is a set
// of permissions, below, and every check asks for a permission rather than a
// role, so that adding a role is adding a line there and never editing a
// comparison somewhere else.
type Role string

const (
	Owner       Role = "owner"
	Bookkeeper  Role = "bookkeeper"
	Contributor Role = "contributor"
	Accountant  Role = "accountant"
	Viewer      Role = "viewer"
)

// Roles is every role, the most powerful first, for a page offering a choice.
var Roles = []Role{Owner, Bookkeeper, Contributor, Accountant, Viewer}

// ParseRole reads a role from a form or a stored row. A role this binary does
// not know is refused rather than read as some other: a row written by a
// newer binary must not become more authority than this one understands.
func ParseRole(s string) (Role, error) {
	if r := Role(s); slices.Contains(Roles, r) {
		return r, nil
	}

	return "", Invalid{Field: "role", Err: fmt.Errorf("%q is not a role", s)}
}

// Permission is one thing a page or an action needs.
type Permission string

const (
	// Read is seeing the scope and everything in it.
	Read Permission = "read"

	// Note is adding a note to a transaction or a receipt.
	Note Permission = "note"

	// Receipts is uploading a receipt and attaching it to a charge.
	Receipts Permission = "receipts"

	// Export is downloading the accountant's package.
	Export Permission = "export"

	// Bookkeep is importing statements, splitting and categorising
	// transactions, keeping the category list, making projects in an
	// organization, and reconciling a month.
	Bookkeep Permission = "bookkeep"

	// Manage is renaming and archiving, and deciding who else may do any of
	// this.
	Manage Permission = "manage"
)

var allows = map[Role][]Permission{
	Owner:       {Read, Note, Receipts, Export, Bookkeep, Manage},
	Bookkeeper:  {Read, Note, Receipts, Export, Bookkeep},
	Contributor: {Read, Note, Receipts},
	Accountant:  {Read, Note, Export},
	Viewer:      {Read},
}

// Allows reports whether the role carries the permission. An unknown role
// carries nothing.
func (r Role) Allows(p Permission) bool { return slices.Contains(allows[r], p) }

// Access is what one user holds on one scope: the roles granted on it
// directly, and those inherited from the organization it belongs to.
//
// The zero Access allows nothing, which is what every lookup that fails
// returns alongside its error.
type Access struct {
	Scope types.Scope
	Roles []Role
}

// Can reports whether any role held allows p.
func (a Access) Can(p Permission) bool {
	return slices.ContainsFunc(a.Roles, func(r Role) bool { return r.Allows(p) })
}
