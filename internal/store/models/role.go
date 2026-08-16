package models

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

	OrganizationID uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_role_org_key,priority:1"`
	Organization   *Organization `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	Key string `gorm:"size:64;not null;uniqueIndex:idx_role_org_key,priority:2"`

	// Name and Description are the fallback labels, used when no translation
	// matches the caller's language. They are never the primary source: see
	// RoleTranslation.
	Name        string `gorm:"size:100;not null"`
	Description string `gorm:"size:255"`

	// IsSystem marks a row materialised from the shipped catalog when the
	// organization was created.
	IsSystem bool `gorm:"not null;default:false"`

	Permissions  []RolePermission  `gorm:"constraint:OnDelete:CASCADE"`
	Translations []RoleTranslation `gorm:"constraint:OnDelete:CASCADE"`
}

func (r *Role) BeforeSave(_ *gorm.DB) error {
	if !validRoleKey(r.Key) {
		return fmt.Errorf("models: invalid role key %q", r.Key)
	}

	if r.Name == "" || utf8.RuneCountInString(r.Name) > 100 {
		return fmt.Errorf("models: invalid role name %q", r.Name)
	}

	return nil
}

func (r *Role) BeforeDelete(_ *gorm.DB) error {
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

	RoleID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_role_permission,priority:1"`
	Role   *Role     `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	PermissionKey string `gorm:"size:100;not null;uniqueIndex:idx_role_permission,priority:2"`
}

// MembershipRole assigns a role to one membership. The unique index makes
// assigning the same role twice a constraint violation rather than a duplicate
// grant, which is what lets the API treat a repeated request as idempotent.
type MembershipRole struct {
	Model

	MembershipID uuid.UUID   `gorm:"type:uuid;not null;uniqueIndex:idx_membership_role,priority:1"`
	Membership   *Membership `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	RoleID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_membership_role,priority:2"`
	Role   *Role     `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	GrantedBy *uuid.UUID `gorm:"type:uuid"`
}

// UserSystemRole assigns an installation-wide role by key.
//
// System roles are not rows in roles: they belong to no organization, and
// forcing them into a table whose every row has an organization_id would put a
// special case into every query that reads roles.
type UserSystemRole struct {
	Model

	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_user_system_role,priority:1"`
	User   *User     `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	RoleKey string `gorm:"size:64;not null;uniqueIndex:idx_user_system_role,priority:2"`

	GrantedBy *uuid.UUID `gorm:"type:uuid"`
}

func (r *UserSystemRole) BeforeSave(_ *gorm.DB) error {
	if !validRoleKey(r.RoleKey) {
		return fmt.Errorf("models: invalid system role key %q", r.RoleKey)
	}

	return nil
}

// RoleTranslation holds the localised label for a role.
//
// Only roles created at runtime need it: the shipped ones are translated in the
// message catalog, which travels with the code and goes through review. This
// table is what makes a customer's own "Ksiegowosc" role displayable in English
// without a deploy.
type RoleTranslation struct {
	Model

	RoleID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_role_translation,priority:1"`
	Role   *Role     `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	// Locale is a BCP 47 tag, stored as given so that a pl-PL override and a pl
	// override can coexist and the more specific one can win.
	Locale string `gorm:"size:10;not null;uniqueIndex:idx_role_translation,priority:2"`

	Name        string `gorm:"size:100;not null"`
	Description string `gorm:"size:255"`
}

func (t *RoleTranslation) BeforeSave(_ *gorm.DB) error {
	if t.Locale == "" || len(t.Locale) > 10 {
		return fmt.Errorf("models: invalid translation locale %q", t.Locale)
	}

	if t.Name == "" || utf8.RuneCountInString(t.Name) > 100 {
		return fmt.Errorf("models: invalid translated role name %q", t.Name)
	}

	return nil
}
