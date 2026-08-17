package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuthzAction enumerates the authorization changes worth keeping.
//
// Unlike LoginOutcome there is no CHECK constraint behind this. The list is
// long and grows with every new administrative operation, and a check would
// turn each addition into a migration; this table is append-only and written
// from exactly one place, so the Go-side validation below is what actually
// guards it.
type AuthzAction string

const (
	ActionOrganizationCreated AuthzAction = "organization.created"
	ActionOrganizationUpdated AuthzAction = "organization.updated"
	ActionOrganizationDeleted AuthzAction = "organization.deleted"

	ActionMemberInvited  AuthzAction = "member.invited"
	ActionMemberAccepted AuthzAction = "member.accepted"

	// ActionMemberJoined is the provisioning path: registering into the default
	// organization, or an operator promoting the first owner. Nobody was invited
	// and nobody accepted, so reporting either would describe something that did
	// not happen.
	ActionMemberJoined AuthzAction = "member.joined"

	// ActionMemberInvitationDeclined records an offer refused by the person it was
	// sent to. The row is deleted, so this entry is the only trace it existed.
	ActionMemberInvitationDeclined AuthzAction = "member.invitation_declined"

	// ActionMemberInvitationWithdrawn is the organization taking an offer back,
	// as opposed to the invitee refusing it. Two actions rather than one, because
	// "who ended this" is the question the entry exists to answer.
	ActionMemberInvitationWithdrawn AuthzAction = "member.invitation_withdrawn"

	ActionMemberRemoved      AuthzAction = "member.removed"
	ActionMemberSuspended    AuthzAction = "member.suspended"
	ActionMemberReinstated   AuthzAction = "member.reinstated"
	ActionMemberRolesChanged AuthzAction = "member.roles_changed"

	ActionRoleCreated            AuthzAction = "role.created"
	ActionRoleUpdated            AuthzAction = "role.updated"
	ActionRoleDeleted            AuthzAction = "role.deleted"
	ActionRolePermissionsChanged AuthzAction = "role.permissions_changed"

	ActionSystemRoleGranted AuthzAction = "system_role.granted"
	ActionSystemRoleRevoked AuthzAction = "system_role.revoked"
)

func (a AuthzAction) Valid() bool {
	switch a {
	case ActionOrganizationCreated, ActionOrganizationUpdated, ActionOrganizationDeleted,
		ActionMemberInvited, ActionMemberAccepted, ActionMemberJoined,
		ActionMemberInvitationDeclined, ActionMemberInvitationWithdrawn,
		ActionMemberRemoved, ActionMemberSuspended,
		ActionMemberReinstated, ActionMemberRolesChanged,
		ActionRoleCreated, ActionRoleUpdated, ActionRoleDeleted, ActionRolePermissionsChanged,
		ActionSystemRoleGranted, ActionSystemRoleRevoked:
		return true
	default:
		return false
	}
}

// AuthzEvent records who changed whose authority, and when.
//
// It mirrors LoginEvent deliberately: the same shape, the same composite index
// on (scope, created_at), the same transport columns. The write is expected to
// happen inside the transaction that makes the change — an audit row committed
// separately is the one that goes missing exactly when it is needed.
type AuthzEvent struct {
	Model

	// CreatedAt shadows Model.CreatedAt to supply the time column of the
	// composite indexes. GORM only builds a composite index when several fields
	// share one index name, and the embedded field cannot be tagged per-model.
	CreatedAt time.Time `gorm:"index:idx_authz_org_time,priority:2;index:idx_authz_actor_time,priority:2"`

	// OrganizationID is null for a system-scope change, which is why the whole
	// table is not scoped by it.
	OrganizationID *uuid.UUID `gorm:"type:uuid;index:idx_authz_org_time,priority:1"`

	ActorID uuid.UUID `gorm:"type:uuid;not null;index:idx_authz_actor_time,priority:1"`

	// SubjectID is the user the change was about, when there was one. Role edits
	// have no subject.
	SubjectID *uuid.UUID `gorm:"type:uuid;index"`

	Action AuthzAction `gorm:"size:40;not null"`

	// RoleID is a bare column: the role may be deleted later, and the record of
	// having granted it must outlive it.
	RoleID        *uuid.UUID `gorm:"type:uuid"`
	PermissionKey string     `gorm:"size:100"`

	IP        string `gorm:"type:inet;not null"`
	UserAgent string `gorm:"size:512"`
	Detail    string `gorm:"size:500"`
}

func (e *AuthzEvent) BeforeSave(_ *gorm.DB) error {
	if !e.Action.Valid() {
		return fmt.Errorf("models: invalid authz action %q", e.Action)
	}

	if e.ActorID == uuid.Nil {
		return fmt.Errorf("models: authz event %q has no actor", e.Action)
	}

	return nil
}
