// Package authz holds the authorization rules: what a permission is, which
// permissions exist, what the default roles grant, and how a decision is made.
//
// It knows nothing about HTTP or SQL. The transport lives in internal/api, the
// persistence in internal/store/repositories. Nothing here imports huma or
// gorm, and internal/architecture_test.go fails the build if that changes.
package authz

import (
	"slices"
	"strings"
)

// Permission is a single thing a caller may be allowed to do.
//
// The value is the wire format: it appears in role_permissions, in the
// permission snapshot the frontend consumes, and in the required_permission
// field of a 403. Renaming one is a contract change, not a refactor.
type Permission string

// Scope says what a permission is measured against — a single organization, or
// the installation as a whole. It decides which of the two assignment paths can
// carry a role holding this permission, and whether the operation guarding it
// needs an {orgID} in its path.
type Scope string

const (
	ScopeOrganization Scope = "organization"
	ScopeSystem       Scope = "system"
)

func (s Scope) Valid() bool {
	return s == ScopeOrganization || s == ScopeSystem
}

// Permission keys follow [platform.]<resource>[.<subresource>].<action>.
//
// The action is always last and always comes from the closed set in
// validActions. A new resource is a new prefix; it is never a deeper nesting of
// an existing one, because "members.roles.invitations.create" is a name nobody
// can guess from the outside and nobody can group in a settings screen.
const (
	PermOrganizationRead   Permission = "organization.read"
	PermOrganizationUpdate Permission = "organization.update"
	PermOrganizationDelete Permission = "organization.delete"

	PermMembersRead        Permission = "members.read"
	PermMembersInvite      Permission = "members.invite"
	PermMembersRemove      Permission = "members.remove"
	PermMembersSuspend     Permission = "members.suspend"
	PermMembersRolesAssign Permission = "members.roles.assign"

	PermRolesRead   Permission = "roles.read"
	PermRolesCreate Permission = "roles.create"
	PermRolesUpdate Permission = "roles.update"
	PermRolesDelete Permission = "roles.delete"

	PermAuditRead Permission = "audit.read"
)

const (
	PermPlatformOrganizationsRead   Permission = "platform.organizations.read"
	PermPlatformOrganizationsCreate Permission = "platform.organizations.create"
	PermPlatformOrganizationsDelete Permission = "platform.organizations.delete"

	PermPlatformUsersRead    Permission = "platform.users.read"
	PermPlatformUsersSuspend Permission = "platform.users.suspend"
	PermPlatformUsersDelete  Permission = "platform.users.delete"

	PermPlatformAuditRead Permission = "platform.audit.read"
)

// Definition is a catalog entry. Group is the heading the settings UI files it
// under; it is spelled out rather than derived from the key so that a screen can
// be reorganised without renaming permissions, which would be a contract change.
type Definition struct {
	Key   Permission
	Scope Scope
	Group string
}

// catalog is the source of truth for which permissions exist.
//
// It is code, not a table, because a permission exists precisely when some
// handler enforces it — a row in a database cannot bring one into being, and a
// row left behind after the handler is deleted is worse than nothing. The
// consequence is handled explicitly: Sanitize drops keys that are no longer
// here, so a stale row in role_permissions grants nothing.
//
// The order is the order the settings UI shows, so it is deliberate rather than
// alphabetical.
var catalog = []Definition{
	{PermOrganizationRead, ScopeOrganization, "organization"},
	{PermOrganizationUpdate, ScopeOrganization, "organization"},
	{PermOrganizationDelete, ScopeOrganization, "organization"},

	{PermMembersRead, ScopeOrganization, "members"},
	{PermMembersInvite, ScopeOrganization, "members"},
	{PermMembersRemove, ScopeOrganization, "members"},
	{PermMembersSuspend, ScopeOrganization, "members"},
	{PermMembersRolesAssign, ScopeOrganization, "members"},

	{PermRolesRead, ScopeOrganization, "roles"},
	{PermRolesCreate, ScopeOrganization, "roles"},
	{PermRolesUpdate, ScopeOrganization, "roles"},
	{PermRolesDelete, ScopeOrganization, "roles"},

	{PermAuditRead, ScopeOrganization, "audit"},

	{PermPlatformOrganizationsRead, ScopeSystem, "platform.organizations"},
	{PermPlatformOrganizationsCreate, ScopeSystem, "platform.organizations"},
	{PermPlatformOrganizationsDelete, ScopeSystem, "platform.organizations"},

	{PermPlatformUsersRead, ScopeSystem, "platform.users"},
	{PermPlatformUsersSuspend, ScopeSystem, "platform.users"},
	{PermPlatformUsersDelete, ScopeSystem, "platform.users"},

	{PermPlatformAuditRead, ScopeSystem, "platform.audit"},
}

// byKey indexes the catalog once. Lookup happens on every authorization
// decision, so a linear scan over a list that only grows is the wrong shape.
var byKey = func() map[Permission]Definition {
	index := make(map[Permission]Definition, len(catalog))
	for _, def := range catalog {
		index[def.Key] = def
	}

	return index
}()

// validActions is the closed set of verbs a key may end with. Keeping it closed
// is what stops "roles.edit" and "roles.update" from both existing and meaning
// the same thing to two different handlers.
var validActions = []string{
	"read", "create", "update", "delete",
	"assign", "invite", "remove", "suspend",
}

// Catalog returns every defined permission in display order.
//
// It returns a copy: the catalog is package state read by every request, and a
// caller that sorted or filtered the slice in place would change what every
// other caller sees.
func Catalog() []Definition {
	return slices.Clone(catalog)
}

// Lookup finds a permission's definition.
func Lookup(key Permission) (Definition, bool) {
	def, ok := byKey[key]

	return def, ok
}

// Known reports whether the key is in the catalog.
func Known(key Permission) bool {
	_, ok := byKey[key]

	return ok
}

// InScope lists the permissions measured against the given scope, in catalog
// order.
func InScope(scope Scope) []Permission {
	out := make([]Permission, 0, len(catalog))
	for _, def := range catalog {
		if def.Scope == scope {
			out = append(out, def.Key)
		}
	}

	return out
}

// Sanitize turns rows read from storage into permissions this build recognises,
// dropping anything unknown and de-duplicating the rest.
//
// Dropping is the safe direction. A key that was removed from the catalog can
// still sit in role_permissions long after the handler that honoured it is
// gone; treating it as a grant would mean a role keeps conferring a power the
// code no longer defines. The dropped keys are worth reporting — see
// UnknownKeys — but never worth honouring.
func Sanitize(keys []string) []Permission {
	out := make([]Permission, 0, len(keys))
	for _, key := range keys {
		perm := Permission(key)
		if !Known(perm) || slices.Contains(out, perm) {
			continue
		}

		out = append(out, perm)
	}

	return out
}

// UnknownKeys is the other half of Sanitize: the keys it would drop. A non-empty
// result means the catalog and the database have drifted apart, which is a
// deployment problem rather than a request problem, so it is reported rather
// than returned as an error to whoever happened to make the call.
func UnknownKeys(keys []string) []string {
	var out []string

	for _, key := range keys {
		if !Known(Permission(key)) {
			out = append(out, key)
		}
	}

	return out
}

// ValidKey reports whether a key follows the naming rules. Nothing calls it at
// runtime — the catalog is a constant list — but a test walks the catalog with
// it, so a key that does not fit the scheme cannot be merged.
func ValidKey(key Permission) bool {
	parts := strings.Split(string(key), ".")

	// [platform.]<resource>[.<subresource>].<action>: two segments minimum,
	// four at most.
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}

	for _, part := range parts {
		if part == "" || strings.ToLower(part) != part {
			return false
		}
	}

	return slices.Contains(validActions, parts[len(parts)-1])
}
