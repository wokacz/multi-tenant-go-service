// Package orgs holds the organization, membership and role rules. Like the
// rest of internal/domain it knows nothing about HTTP or SQL.
package orgs

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
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

	// ErrInvitationExpired is separate from ErrNotFound because the holder of the
	// token can act on it: ask whoever invited them to send another. A 404 would
	// send them looking for a mistake they did not make.
	ErrInvitationExpired = errors.New("orgs: the invitation has expired")

	// ErrInvitationAddressMismatch is the refusal when the token is good but the
	// account accepting it has a different address. Revealing that the invitation
	// exists is fine — the caller is holding its token, which is already proof
	// they received it — and a bare 404 would leave them with nothing to say to
	// the person who invited them.
	ErrInvitationAddressMismatch = errors.New("orgs: the invitation was issued to a different address")
	ErrInvalidEmail              = errors.New("orgs: email is empty")

	// ErrInvalidSystemRole refuses a key that is not an installation-wide role
	// this build ships. Role keys are code, the same way permissions are, so a row
	// naming one that does not exist would grant nothing while looking as though
	// it should.
	ErrInvalidSystemRole = errors.New("orgs: not an installation-wide role")
)

// Membership is the view of one organization from one account's point of view.
type Membership struct {
	// ID is the membership itself, which is what a self-service caller names to
	// leave. It used to be how an invitation was accepted or declined, back when an
	// invitation was a membership row; those go by token now.
	ID           uuid.UUID
	Organization ent.Organization
	Status       ent.MembershipStatus
	RoleKeys     []string
}

// Invitation is an outstanding offer of membership.
//
// The token is absent on purpose. It exists once, in the message that was sent;
// this type is what anything reading invitations back out of storage sees, and a
// token that can be read back is a token that can be used by whoever can read the
// table.
type Invitation struct {
	ID           uuid.UUID
	Organization ent.Organization
	Email        string
	InvitedBy    *uuid.UUID
	ExpiresAt    time.Time
	RoleKeys     []string
}

// Member is one person's place in an organization, as an administrator sees it.
type Member struct {
	ID       uuid.UUID // the membership, not the account
	UserID   uuid.UUID
	Name     string
	Email    string
	Status   ent.MembershipStatus
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
	ent.Role
	Permissions []authz.Permission
	// Members is how many people hold it, which is what the settings screen
	// needs to warn before a deletion and what the last-owner rule counts.
	Members int
}

// OwnerState is what the last-owner rule decides from.
//
// It is read inside the same transaction as the change it guards, with the
// organization row locked, so the numbers cannot move between the check and the
// write. That is the whole reason this exists as a type: the rule belongs in the
// domain, the transaction belongs in the store, and passing the facts across is
// what lets both be true at once.
type OwnerState struct {
	// Owners is how many active members hold the owner role. A membership whose
	// account has been deleted is not one of them — see Members for that rule.
	Owners int

	// SubjectHoldsOwner says whether the membership being changed is one of them.
	// It cannot be read before the transaction: the subject's roles could change
	// in between, which is the race this design closes.
	SubjectHoldsOwner bool
}

// OwnerGuard is the domain's verdict on an OwnerState. The repository calls it
// inside the transaction and abandons the change if it returns an error.
//
// It is required, not optional. A change that cannot lose the last owner passes
// RefuseLastOwnerLoss(false) rather than nothing, so there is no nil to forget
// and no branch where the lock is quietly skipped.
type OwnerGuard func(OwnerState) error

// RoleGuard is the same arrangement for deleting a role: holders is counted
// inside the transaction, so a role cannot be assigned to somebody between the
// count and the delete and lose the assignment to the cascade.
type RoleGuard func(holders int) error

// Repository is the organization-scoped persistence: everything an operation
// inside one organization needs.
//
// It is the union of four groups, and it keeps a single name because orgs.Service
// genuinely uses all four. Nothing depends on one group alone today; the groups
// are here because seventeen methods under one comment is a list nobody reads, and
// because a consumer that only needs the roster should be able to say so and get a
// smaller double in its tests. cmd/bootstrap already depends on Provisioner alone,
// which is the same idea one interface further out.
//
// Every method in every group takes the organization id as its second parameter,
// and that is not a formatting rule. It is what makes the resource half of an
// authorization decision structural: the middleware establishes that the caller
// may act in organization X, and there is then no way to load a role, a member or
// a setting except by naming an organization. Forgetting the scope check becomes
// a compile error rather than a hole somebody has to spot in review, and a row
// from another tenant simply cannot be reached.
//
// The paged methods expect a positive limit. Nothing here clamps one: Service is
// where the caps live, so a limit of zero returns nothing rather than everything,
// on both implementations. That is the harmless reading of a mistake — an empty
// page is visible, while silently answering with the whole table is what
// pagination exists to prevent.
//
// TestScopedRepositoryMethodsTakeAnOrganization enforces the shape. It reflects
// over this interface, and embedded methods are promoted, so a group added to the
// list below is covered without anybody remembering to extend the test.
type Repository interface {
	Organizations
	Memberships
	Roles
	Invitations
}

// Organizations is the organization row itself.
type Organizations interface {
	// Organization returns the organization, or ErrNotFound when it does not
	// exist or has been deleted.
	Organization(ctx context.Context, orgID uuid.UUID) (*ent.Organization, error)

	// UpdateOrganization renames it.
	UpdateOrganization(ctx context.Context, orgID uuid.UUID, name string) error

	// DeleteOrganization soft deletes it. It returns ent.ErrProtected for an
	// organization marked as protected — the default one is.
	DeleteOrganization(ctx context.Context, orgID uuid.UUID) error
}

// Memberships is who is in the organization, and what each of them holds.
type Memberships interface {
	// Members lists everyone in the organization, suspensions included, with the
	// roles each holds.
	//
	// It does not list invitations, and this comment claimed otherwise for two
	// commits after they moved to a table of their own. InvitationsForOrganization
	// is where they are now.
	//
	// A membership whose account has been deleted is not listed either. Soft
	// deleting an account does not fire the foreign key cascade, so the membership
	// row outlives its person; reporting it would put an entry with no name and
	// nobody behind it into every administrator's list.
	//
	// That rule has one statement, and this is it. Every method here that resolves
	// a membership to a person answers it the same way: Member, MemberByUser, and
	// the last-owner check.
	//
	// Paged, because it was not: an organization with fifty thousand members
	// answered with all of them, and then asked for their roles with an IN list of
	// fifty thousand ids. Ordered by (name, id), which is total, so a page boundary
	// falls in the same place twice.
	Members(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]Member, error)

	// Member returns one membership by its id, or ErrNotFound when it belongs
	// to another organization or its account has been deleted.
	Member(ctx context.Context, orgID, memberID uuid.UUID) (*Member, error)

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

	// AddMember creates an active membership for an existing account and
	// assigns the given roles, in one transaction. It is the provisioning
	// path — bootstrap, promoting the first owner — not the invitation path.
	//
	// It returns ErrAlreadyMember when the account is already a live member, and
	// ErrNotFound when there is no such account.
	AddMember(ctx context.Context, orgID, userID uuid.UUID, roleIDs []uuid.UUID, invitedBy uuid.UUID, at time.Time) (*Member, error)

	// SetMemberStatus suspends or reinstates a membership. guard is called inside
	// the transaction, with the organization row locked.
	SetMemberStatus(ctx context.Context, orgID, memberID uuid.UUID, status ent.MembershipStatus, at time.Time, guard OwnerGuard) error

	// RemoveMember deletes the membership and, by cascade, its role
	// assignments.
	//
	// action is what the audit entry says, and it is a parameter for the same
	// reason guard is: the row disappearing looks identical from here, and only the
	// caller knows whether an administrator removed somebody or somebody left. The
	// store times the write; the domain decides what it means.
	//
	// It is the one method here that still works on a membership whose account
	// has been deleted, and it must stay that way: everything else reports such a
	// row as not found, so refusing here too would leave it in the organization
	// with no way to take it out. Do not add a Member lookup in front of this.
	RemoveMember(ctx context.Context, orgID, memberID uuid.UUID, action string, guard OwnerGuard) error

	// ReplaceMemberRoles sets the member's roles to exactly roleIDs, in one
	// transaction.
	//
	// It replaces rather than adds and removes because two administrators
	// editing the same person concurrently would otherwise merge into a set
	// neither of them chose. It returns ErrNotFound when any role id is not
	// this organization's, which is what stops a role being borrowed across
	// tenants.
	ReplaceMemberRoles(ctx context.Context, orgID, memberID uuid.UUID, roleIDs []uuid.UUID, guard OwnerGuard) error
}

// Roles is the organization's roles and what they grant.
type Roles interface {
	// Roles lists the organization's roles with their permissions and holder
	// counts, paged. The shipped ones come first, then by key, which is unique
	// within an organization and makes the order total.
	Roles(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]Role, error)

	// Role returns one role, or ErrNotFound when it belongs to another
	// organization.
	Role(ctx context.Context, orgID, roleID uuid.UUID) (*Role, error)

	// RoleByKey finds a role by its stable key, which is how the shipped roles
	// are addressed by anything that did not read them from the database first.
	RoleByKey(ctx context.Context, orgID uuid.UUID, key string) (*Role, error)

	// CreateRole stores a role and its permissions in one transaction. It
	// returns ErrRoleKeyTaken when the key is already used here.
	CreateRole(ctx context.Context, orgID uuid.UUID, role *ent.Role, permissions []authz.Permission) (*Role, error)

	// UpdateRole renames a role. Permissions are changed separately, because
	// the two operations need different checks.
	UpdateRole(ctx context.Context, orgID, roleID uuid.UUID, name, description string) error

	// DeleteRole removes a custom role. The service refuses protected ones and
	// ones still held; this is the storage half.
	DeleteRole(ctx context.Context, orgID, roleID uuid.UUID, guard RoleGuard) error

	// ReplaceRolePermissions sets the role's permissions to exactly those
	// given, in one transaction.
	ReplaceRolePermissions(ctx context.Context, orgID, roleID uuid.UUID, permissions []authz.Permission) error
}

// Invitations is the offer of membership, from the organization's side.
//
// Its counterparts on the invitee's side — finding an invitation by its token,
// accepting one, declining one, listing the ones addressed to you — are in
// Directory, because none of them can name an organization: the token is all the
// holder has. Splitting the surface along that line is not an accident of who
// wrote what. It is the difference between authorization by permission and
// authorization by holding a secret, and each half is scoped by what it can be.
type Invitations interface {
	// InviteMember records an invitation for the address and stores the hash of
	// its token.
	//
	// It does not look the address up in the account table: that lookup is what
	// would tell the caller whether the person is registered anywhere in the
	// installation. Unknown and known addresses produce the same row and the same
	// response.
	//
	// ErrAlreadyMember covers both "this organization already has an outstanding
	// invitation for that address" and "that address is already a member" — one
	// answer, because the caller may act on either the same way and telling them
	// apart is not worth a second code.
	InviteMember(ctx context.Context, orgID uuid.UUID, email, tokenHash string, roleIDs []uuid.UUID, invitedBy uuid.UUID, expiresAt, at time.Time) (*Invitation, error)

	// InvitationsForOrganization lists the pending invitations an organization has
	// issued, so an administrator can see what is outstanding. Before invitations
	// had their own table they appeared in the members list, which is where anybody
	// looking for them still expects to find them — hence a listing of their own
	// rather than nothing at all.
	InvitationsForOrganization(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Invitation, error)

	// ReissueInvitation replaces the token hash and the expiry on an outstanding
	// invitation, so the old link stops working. Anything else called "resend"
	// either keeps a leaked link alive or collides with the row already there.
	ReissueInvitation(ctx context.Context, orgID, invitationID uuid.UUID, tokenHash string, expiresAt time.Time) (*Invitation, error)

	// WithdrawInvitation removes an offer the organization made.
	WithdrawInvitation(ctx context.Context, orgID, invitationID uuid.UUID) error
}

// Directory is the persistence that deliberately is not scoped to one
// organization: the questions an account asks about itself, before any
// organization has been chosen, and the invitation an invitee reaches through the
// token they were sent.
//
// It is a separate interface rather than a handful of exceptions inside
// Repository, so that "every method here is scoped" stays a rule with no
// asterisks. It had grown three of them — listing, reissuing and withdrawing an
// invitation all name an organization and all sat here, because that is where the
// invitation code was being written at the time. They are in Invitations now, and
// the scoping test covers them for the first time as a result.
type Directory interface {
	// MembershipsForUser lists the live organizations the account belongs to,
	// including suspensions, so the client can render those differently rather
	// than have them silently disappear. Invitations are not memberships and are
	// listed by InvitationsForEmail.
	MembershipsForUser(ctx context.Context, userID uuid.UUID) ([]Membership, error)

	// InvitationByToken finds a pending invitation by the hash of its token, or
	// ErrNotFound. An expired or already-accepted one is not found either: the
	// caller holding the token learns only that it no longer works, which is all
	// they can act on.
	InvitationByToken(ctx context.Context, tokenHash string, now time.Time) (*Invitation, error)

	// AcceptInvitation spends the invitation and creates the active membership it
	// promised, in one transaction.
	//
	// The roles come from the invitation, never from the caller: whoever accepts
	// must not get to choose what they are accepting. It returns ErrAlreadyMember
	// when the account is already in that organization, and ErrNotFound when the
	// organization has been deleted — checked inside the transaction, because
	// InvitationByToken having filtered it out a statement earlier is not the same
	// as it still being true.
	AcceptInvitation(ctx context.Context, invitationID, userID uuid.UUID, at time.Time) error

	// DeclineInvitation removes a pending invitation. Holding the token is the
	// authorization: the person who can read the mailbox is the person entitled to
	// refuse.
	DeclineInvitation(ctx context.Context, invitationID uuid.UUID) error

	// InvitationsForEmail lists the pending invitations addressed to one account,
	// so a client can show "you have been invited to X" without the token. Taking
	// one up still needs the token from the message.
	InvitationsForEmail(ctx context.Context, email string, now time.Time) ([]Invitation, error)
}

// There is deliberately no AcceptInvitationsByEmail here.
//
// Registration used to call one, so that a new account landed in every
// organization it had been invited to without a second round trip. That made
// registering an accept — and the address on a new account is never verified,
// so whoever registers an invited address first inherits the invitation and its
// roles. An invitation is accepted by the invitee, through /v1/me/invitations,
// and by nothing else.
