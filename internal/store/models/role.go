package models

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	// ErrRoleIsSystem guards the roles the code ships. They are visible and
	// clonable, but not editable: an owner who could rewrite "admin" could
	// rewrite it into something the anti-escalation rule was meant to prevent,
	// and every organization's copy would drift from every other.
	ErrRoleIsSystem = errors.New("models: system roles cannot be modified or deleted")

	// ErrRoleBatchDeleteUnsupported mirrors ErrBatchDeleteUnsupported on User: a
	// batch delete hands the hook a zero-valued receiver, so IsSystem reads
	// false and the protection above would silently not apply.
	ErrRoleBatchDeleteUnsupported = errors.New("models: deleting a role requires a primary key so system roles stay protected")
)

// MaxRoleKeyLength matches the column.
const MaxRoleKeyLength = 64

// Role is a named bundle of permissions inside one organization.
//
// It is *not* soft deleted, unlike User and Organization. The unique index is
// (organization_id, key), and a soft-deleted row keeps occupying its key — so
// deleting "editor" and creating "editor" again would fail on a duplicate key
// for a role the user cannot see. Deletion is therefore a real delete, and the
// service refuses it while the role is still assigned, which is a better
// guarantee than an undo nobody can reach.
type Role struct {
	Model

	OrganizationID uuid.UUID
	Organization   *Organization `json:"-"`

	Key string

	// Name and Description are what a role created here is called. For a shipped
	// role they are the English strings from the Go definition and act as a
	// fallback: the API renders those from the message catalog instead, keyed by
	// Key, so they read in the caller's language.
	Name        string
	Description string

	// IsSystem marks a row materialised from the shipped catalog when the
	// organization was created.
	IsSystem bool

	Permissions []RolePermission `json:"-"`
}

func (r *Role) Validate() error {
	if !validRoleKey(r.Key) {
		return fmt.Errorf("models: invalid role key %q", r.Key)
	}

	if r.Name == "" || utf8.RuneCountInString(r.Name) > 100 {
		return fmt.Errorf("models: invalid role name %q", r.Name)
	}

	return nil
}

func (r *Role) RefuseDelete() error {
	if r.ID == uuid.Nil {
		return ErrRoleBatchDeleteUnsupported
	}

	if r.IsSystem {
		return ErrRoleIsSystem
	}

	return nil
}

// validRoleKey allows lowercase letters, digits and single inner underscores —
// the same shape as the keys in the shipped catalog, so a custom role cannot be
// named in a way a translation key could not address.
func validRoleKey(key string) bool {
	if key == "" || len(key) > MaxRoleKeyLength {
		return false
	}

	if key[0] == '_' || key[len(key)-1] == '_' {
		return false
	}

	for i := range len(key) {
		c := key[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_':
			if key[i-1] == '_' {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// RolePermission joins a role to one permission key.
//
// PermissionKey has no foreign key, because there is no permissions table: the
// catalog in internal/domain/authz is the source of truth. A key this build no
// longer defines is dropped when permissions are resolved rather than honoured,
// so a stale row grants nothing.
type RolePermission struct {
	Model

	RoleID uuid.UUID
	Role   *Role `json:"-"`

	PermissionKey string
}

// MembershipRole assigns a role to one membership. The unique index makes
// assigning the same role twice a constraint violation rather than a duplicate
// grant, which is what lets the API treat a repeated request as idempotent.
type MembershipRole struct {
	Model

	MembershipID uuid.UUID
	Membership   *Membership `json:"-"`

	RoleID uuid.UUID
	Role   *Role `json:"-"`

	GrantedBy *uuid.UUID
}

// UserSystemRole assigns an installation-wide role by key.
//
// System roles are not rows in roles: they belong to no organization, and
// forcing them into a table whose every row has an organization_id would put a
// special case into every query that reads roles.
type UserSystemRole struct {
	Model

	UserID uuid.UUID
	User   *User `json:"-"`

	RoleKey string

	GrantedBy *uuid.UUID
}

func (r *UserSystemRole) Validate() error {
	if !validRoleKey(r.RoleKey) {
		return fmt.Errorf("models: invalid system role key %q", r.RoleKey)
	}

	return nil
}
