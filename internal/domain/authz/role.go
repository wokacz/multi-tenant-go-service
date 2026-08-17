package authz

import "slices"

// RoleKey identifies a role inside its organization, or — for system roles —
// inside the installation. It is stable, lowercase and snake_case; the display
// name is a translation keyed off it, never the identifier itself.
type RoleKey string

const (
	// RolePlatformAdmin is the only system role. It is never a row in the roles
	// table: it belongs to no organization, so forcing it into a table whose
	// every row has an organization_id would put a special case in every query
	// that reads roles.
	RolePlatformAdmin RoleKey = "platform_admin"

	RoleOwner  RoleKey = "owner"
	RoleAdmin  RoleKey = "admin"
	RoleMember RoleKey = "member"
	RoleViewer RoleKey = "viewer"
)

// RoleDefinition is a role the code ships with.
//
// Organization-scoped definitions are materialised as protected rows when an
// organization is created, so an owner can read exactly what "admin" grants and
// clone it into something editable. System-scoped ones stay in code and are
// assigned by key.
//
// Name and Description are English fallbacks. They are what a client sees when
// no catalog entry matches the requested language; they are not the primary
// source of the label.
type RoleDefinition struct {
	Key         RoleKey
	Scope       Scope
	Name        string
	Description string
	Permissions []Permission
}

// ownerPermissions is derived from the catalog rather than listed, because
// "owner" means "everything in this organization" by definition. Deriving makes
// the invariant impossible to break: a permission added to the catalog reaches
// the owner in the same commit, and cannot become a feature that works for
// nobody and reports no error.
//
// The other roles are deliberately *not* derived. A newly added permission
// landing in every existing organization's admin role without anyone deciding
// so would be a silent privilege change shipped with an unrelated feature.
func ownerPermissions() []Permission {
	return InScope(ScopeOrganization)
}

var systemRoles = []RoleDefinition{
	{
		Key:         RolePlatformAdmin,
		Scope:       ScopeSystem,
		Name:        "Platform administrator",
		Description: "Manages organizations and accounts across the whole installation.",
		Permissions: InScope(ScopeSystem),
	},
}

var organizationRoles = []RoleDefinition{
	{
		Key:         RoleOwner,
		Scope:       ScopeOrganization,
		Name:        "Owner",
		Description: "Full control of the organization, including deleting it.",
		Permissions: ownerPermissions(),
	},
	{
		Key:         RoleAdmin,
		Scope:       ScopeOrganization,
		Name:        "Administrator",
		Description: "Manages members and roles, but cannot delete the organization.",
		Permissions: []Permission{
			PermOrganizationRead,
			PermOrganizationUpdate,
			PermMembersRead,
			PermMembersInvite,
			PermMembersRemove,
			PermMembersSuspend,
			PermMembersRolesAssign,
			PermRolesRead,
			PermRolesCreate,
			PermRolesUpdate,
			PermRolesDelete,
			PermAuditRead,
		},
	},
	{
		Key:         RoleMember,
		Scope:       ScopeOrganization,
		Name:        "Member",
		Description: "Sees the organization and who belongs to it.",
		Permissions: []Permission{
			PermOrganizationRead,
			PermMembersRead,
		},
	},
	{
		Key:         RoleViewer,
		Scope:       ScopeOrganization,
		Name:        "Viewer",
		Description: "Read-only access to the organization itself.",
		Permissions: []Permission{
			PermOrganizationRead,
		},
	},
}

// OrganizationRoles are the roles materialised into every new organization.
func OrganizationRoles() []RoleDefinition {
	return cloneRoles(organizationRoles)
}

// SystemRoles are the installation-wide roles, assigned by key rather than
// stored per organization.
func SystemRoles() []RoleDefinition {
	return cloneRoles(systemRoles)
}

// LookupRole finds a shipped role by key, in either scope.
func LookupRole(key RoleKey) (RoleDefinition, bool) {
	for _, def := range organizationRoles {
		if def.Key == key {
			return cloneRole(def), true
		}
	}

	for _, def := range systemRoles {
		if def.Key == key {
			return cloneRole(def), true
		}
	}

	return RoleDefinition{}, false
}

// IsSystemScopeRole reports whether the key names an installation-wide role.
//
// It is separate from IsShippedRole because the two answer different questions: one
// asks "does this exist", the other "is it granted against the installation rather
// than inside an organization". Granting an organization role through the platform
// endpoint would put a key nothing reads into user_system_roles.
func IsSystemScopeRole(key RoleKey) bool {
	for _, def := range systemRoles {
		if def.Key == key {
			return true
		}
	}

	return false
}

// IsShippedRole reports whether the key names a role the code defines. Rows with
// such a key are protected from editing, so this is what the service consults
// before allowing a change.
func IsShippedRole(key RoleKey) bool {
	_, ok := LookupRole(key)

	return ok
}

// cloneRoles copies both the slice and each Permissions slice inside it.
// slices.Clone alone would hand back definitions sharing their permission
// backing arrays with package state, where one caller writing through an index
// changes what every later caller is granted.
func cloneRoles(defs []RoleDefinition) []RoleDefinition {
	out := make([]RoleDefinition, 0, len(defs))
	for _, def := range defs {
		out = append(out, cloneRole(def))
	}

	return out
}

func cloneRole(def RoleDefinition) RoleDefinition {
	def.Permissions = slices.Clone(def.Permissions)

	return def
}
