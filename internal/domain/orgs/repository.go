// Package orgs holds the organization, membership and role rules. Like the
// rest of internal/domain it knows nothing about HTTP or SQL.
package orgs

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

var (
	// ErrNotFound covers a missing organization, a deleted one, a row from
	// another organization, and one the caller has no business seeing. They
	// share an error for the same reason authz.ErrNotMember exists: in a
	// multi-tenant system, telling a stranger that something exists hands them
	// a customer list.
	ErrNotFound = errors.New("orgs: not found")

	// ErrLastOwner refuses the change that leaves an organization with nobody
	// who can administer it. Recovering from that needs database access, which
	// is not a support path anyone should have to use.
	ErrLastOwner = errors.New("orgs: the organization would be left without an owner")

	// ErrRoleProtected guards the roles materialised from the shipped catalog.
	// They are visible and clonable, never editable.
	ErrRoleProtected = errors.New("orgs: system roles cannot be changed")

	// ErrRoleInUse refuses deleting a role somebody still holds. Cascading the
	// delete would silently take permissions away from people the caller never
	// looked at.
	ErrRoleInUse = errors.New("orgs: the role is still assigned to members")

	ErrRoleKeyTaken  = errors.New("orgs: a role with that key already exists")
	ErrAlreadyMember = errors.New("orgs: already a member of the organization")

	// ErrSlugTaken is only ever seen by the provisioning path, where two
	// processes racing to seed the same organization is normal rather than an
	// error to report.
	ErrSlugTaken = errors.New("orgs: an organization with that slug already exists")

	ErrInvalidName   = errors.New("orgs: name is empty or too long")
	ErrInvalidStatus = errors.New("orgs: invalid membership status")
	ErrInvalidEmail  = errors.New("orgs: email is empty")
)

// Membership is the view of one organization from one account's point of view.
type Membership struct {
	ID           uuid.UUID // the membership, used to accept or decline an invitation
	Organization models.Organization
	Status       models.MembershipStatus
	RoleKeys     []string
}

// Member is one person's place in an organization, as an administrator sees it.
type Member struct {
	ID       uuid.UUID // the membership, not the account
	UserID   uuid.UUID
	Name     string
	Email    string
	Status   models.MembershipStatus
	JoinedAt *time.Time
	Roles    []RoleSummary
}

// RoleSummary is a role without its permissions, for listing people.
type RoleSummary struct {
	ID       uuid.UUID
	Key      string
	Name     string
	IsSystem bool
}

// Role is a role with everything it grants.
type Role struct {
	models.Role
	Permissions []authz.Permission
	// Members is how many people hold it, which is what the settings screen
	// needs to warn before a deletion and what the last-owner rule counts.
	Members int
}

// Repository is the organization-scoped persistence.
//
// Every method takes the organization id as its second parameter, and that is
// not a formatting rule. It is what makes the resource half of an authorization
// decision structural: the middleware establishes that the caller may act in
// organization X, and there is then no way to load a role, a member or a
// setting except by naming an organization. Forgetting the scope check becomes
// a compile error rather than a hole somebody has to spot in review, and a row
// from another tenant simply cannot be reached.
//
// TestScopedRepositoryMethodsTakeAnOrganization enforces the shape.
type Repository interface {
	// Organization returns the organization, or ErrNotFound when it does not
	// exist or has been deleted.
	Organization(ctx context.Context, orgID uuid.UUID) (*models.Organization, error)

	// UpdateOrganization renames it.
	UpdateOrganization(ctx context.Context, orgID uuid.UUID, name string) error

	// DeleteOrganization soft deletes it. It returns models.ErrProtected for an
	// organization marked as protected — the default one is.
	DeleteOrganization(ctx context.Context, orgID uuid.UUID) error

	// Members lists everyone in the organization, including invitations and
	// suspensions, with the roles each holds.
	//
	// A membership whose account has been deleted is not listed. Soft deleting an
	// account does not fire the foreign key cascade, so the membership row
	// outlives its person; reporting it would put an entry with no name and
	// nobody behind it into every administrator's list. An invitation, which has
	// no account yet, *is* listed — the difference between "nobody has taken this
	// up" and "the person is gone" is worth keeping.
	//
	// This is the one statement of that rule. Every method here that resolves a
	// membership to a person answers it the same way: Member, MemberByUser, and
	// the last-owner check.
	Members(ctx context.Context, orgID uuid.UUID) ([]Member, error)

	// Member returns one membership by its id, or ErrNotFound when it belongs
	// to another organization or its account has been deleted.
	Member(ctx context.Context, orgID, memberID uuid.UUID) (*Member, error)

	// AddMember creates an active membership for an existing account and
	// assigns the given roles, in one transaction. It is the provisioning
	// path — bootstrap, promoting the first owner — not the invitation path.
	//
	// An outstanding invitation for the same address is claimed (activated and
	// given these roles) rather than refused. That is wanted where the caller is
	// an operator acting out of band: without it, an account that happens to
	// have been invited could never be promoted, because the invitation blocks
	// the insert and nobody yet holds the permission to withdraw it. It is not
	// wanted on the registration path, which is why JoinDefaultOrganization
	// looks for an invitation before calling this — claiming one there would
	// accept an invitation on the invitee's behalf and downgrade its roles.
	//
	// It returns ErrAlreadyMember when the account is already a live member.
	AddMember(ctx context.Context, orgID, userID uuid.UUID, roleIDs []uuid.UUID, invitedBy uuid.UUID, at time.Time) (*Member, error)

	// InviteMember records an outstanding invitation for the address. It does
	// not look the address up in the account table: that lookup is what would
	// tell the caller whether the person is registered anywhere in the
	// installation. UserID stays nil until they accept. ErrAlreadyMember when
	// this organization already has that email.
	InviteMember(ctx context.Context, orgID uuid.UUID, email string, roleIDs []uuid.UUID, invitedBy uuid.UUID, at time.Time) (*Member, error)

	// SetMemberStatus suspends or reinstates a membership.
	SetMemberStatus(ctx context.Context, orgID, memberID uuid.UUID, status models.MembershipStatus, at time.Time) error

	// RemoveMember deletes the membership and, by cascade, its role
	// assignments.
	//
	// It is the one method here that still works on a membership whose account
	// has been deleted, and it must stay that way: everything else reports such a
	// row as not found, so refusing here too would leave it in the organization
	// with no way to take it out. Do not add a Member lookup in front of this.
	RemoveMember(ctx context.Context, orgID, memberID uuid.UUID) error

	// ReplaceMemberRoles sets the member's roles to exactly roleIDs, in one
	// transaction.
	//
	// It replaces rather than adds and removes because two administrators
	// editing the same person concurrently would otherwise merge into a set
	// neither of them chose. It returns ErrNotFound when any role id is not
	// this organization's, which is what stops a role being borrowed across
	// tenants.
	ReplaceMemberRoles(ctx context.Context, orgID, memberID uuid.UUID, roleIDs []uuid.UUID) error

	// Roles lists the organization's roles with their permissions and holder
	// counts.
	Roles(ctx context.Context, orgID uuid.UUID) ([]Role, error)

	// Role returns one role, or ErrNotFound when it belongs to another
	// organization.
	Role(ctx context.Context, orgID, roleID uuid.UUID) (*Role, error)

	// RoleByKey finds a role by its stable key, which is how the shipped roles
	// are addressed by anything that did not read them from the database first.
	RoleByKey(ctx context.Context, orgID uuid.UUID, key string) (*Role, error)

	// MemberByUser finds the membership belonging to an account, or ErrNotFound.
	// A deleted account is ErrNotFound, the same as in Members and Member.
	MemberByUser(ctx context.Context, orgID, userID uuid.UUID) (*Member, error)

	// MemberPermissions is the union of what this membership's roles grant, in
	// one query rather than one per role.
	//
	// Status is deliberately not considered. A suspended member grants nothing
	// while suspended, but the question this answers is "how much power does this
	// row carry", which is what EnsureCanAffect compares against the caller — and
	// a suspended owner should not become removable by an administrator merely
	// because somebody suspended them first.
	MemberPermissions(ctx context.Context, orgID, memberID uuid.UUID) ([]authz.Permission, error)

	// CreateRole stores a role and its permissions in one transaction. It
	// returns ErrRoleKeyTaken when the key is already used here.
	CreateRole(ctx context.Context, orgID uuid.UUID, role *models.Role, permissions []authz.Permission) (*Role, error)

	// UpdateRole renames a role. Permissions are changed separately, because
	// the two operations need different checks.
	UpdateRole(ctx context.Context, orgID, roleID uuid.UUID, name, description string) error

	// DeleteRole removes a custom role. The service refuses protected ones and
	// ones still held; this is the storage half.
	DeleteRole(ctx context.Context, orgID, roleID uuid.UUID) error

	// ReplaceRolePermissions sets the role's permissions to exactly those
	// given, in one transaction.
	ReplaceRolePermissions(ctx context.Context, orgID, roleID uuid.UUID, permissions []authz.Permission) error

	// OwnerCount is how many active members hold the owner role. The
	// last-owner rule counts with it.
	OwnerCount(ctx context.Context, orgID uuid.UUID) (int, error)
}

// Directory is the persistence that deliberately is not scoped to one
// organization: the questions an account asks about itself, before any
// organization has been chosen, and the account lookup an invitation needs.
//
// It is a separate interface rather than a handful of exceptions inside
// Repository, so that "every method here is scoped" stays a rule with no
// asterisks.
type Directory interface {
	// MembershipsForUser lists the live organizations the account belongs to,
	// including invitations (matched by the account's email while still
	// outstanding) and suspensions, so the client can render them differently
	// rather than have them silently disappear.
	MembershipsForUser(ctx context.Context, userID uuid.UUID) ([]Membership, error)

	// AcceptInvitation turns an outstanding invitation into an active
	// membership for this account. The address on the row must match email;
	// a mismatch is ErrNotFound, the same as a missing id, so a caller cannot
	// probe invitations that are not theirs.
	AcceptInvitation(ctx context.Context, memberID, userID uuid.UUID, email string, at time.Time) error

	// DeclineInvitation withdraws an outstanding invitation addressed to
	// email. A mismatch is ErrNotFound, for the same reason as AcceptInvitation.
	DeclineInvitation(ctx context.Context, memberID uuid.UUID, email string) error
}

// There is deliberately no AcceptInvitationsByEmail here.
//
// Registration used to call one, so that a new account landed in every
// organization it had been invited to without a second round trip. That made
// registering an accept — and the address on a new account is never verified,
// so whoever registers an invited address first inherits the invitation and its
// roles. An invitation is accepted by the invitee, through /v1/me/invitations,
// and by nothing else.
