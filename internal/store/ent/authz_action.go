package ent

import (
	"fmt"

	"github.com/google/uuid"
)

// AuthzAction enumerates the authorization changes worth keeping.
//
// Unlike login outcome there is no CHECK constraint behind this. The list is
// long and grows with every new administrative operation, and a check would
// turn each addition into a migration; this table is append-only and written
// from exactly one place, so the Go-side validation below is what actually
// guards it.
type AuthzAction string

const (
	ActionOrganizationCreated = "organization.created"
	ActionOrganizationUpdated = "organization.updated"
	ActionOrganizationDeleted = "organization.deleted"

	ActionMemberInvited  = "member.invited"
	ActionMemberAccepted = "member.accepted"

	ActionMemberJoined = "member.joined"

	ActionMemberInvitationDeclined  = "member.invitation_declined"
	ActionMemberInvitationWithdrawn = "member.invitation_withdrawn"

	ActionMemberRemoved      = "member.removed"
	ActionMemberLeft         = "member.left"
	ActionMemberSuspended    = "member.suspended"
	ActionMemberReinstated   = "member.reinstated"
	ActionMemberRolesChanged = "member.roles_changed"

	ActionRoleCreated            = "role.created"
	ActionRoleUpdated            = "role.updated"
	ActionRoleDeleted            = "role.deleted"
	ActionRolePermissionsChanged = "role.permissions_changed"

	ActionSystemRoleGranted = "system_role.granted"
	ActionSystemRoleRevoked = "system_role.revoked"

	ActionFileUploaded = "file.uploaded"
	ActionFileDeleted  = "file.deleted"
)

func (a AuthzAction) Valid() bool {
	switch string(a) {
	case ActionOrganizationCreated, ActionOrganizationUpdated, ActionOrganizationDeleted,
		ActionMemberInvited, ActionMemberAccepted, ActionMemberJoined,
		ActionMemberInvitationDeclined, ActionMemberInvitationWithdrawn,
		ActionMemberRemoved, ActionMemberLeft, ActionMemberSuspended,
		ActionMemberReinstated, ActionMemberRolesChanged,
		ActionRoleCreated, ActionRoleUpdated, ActionRoleDeleted, ActionRolePermissionsChanged,
		ActionSystemRoleGranted, ActionSystemRoleRevoked,
		ActionFileUploaded, ActionFileDeleted:
		return true
	default:
		return false
	}
}

func (e *AuthzEvent) Validate() error {
	if !AuthzAction(e.Action).Valid() {
		return fmt.Errorf("ent: invalid authz action %q", e.Action)
	}

	if e.ActorID == uuid.Nil {
		return fmt.Errorf("ent: authz event %q has no actor", e.Action)
	}

	return nil
}
