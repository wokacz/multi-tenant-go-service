package orgs

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// Service carries the organization rules that need storage to decide.
//
// Every administrative method takes the caller's *authz.Grant rather than a
// user id and an organization id. That is deliberate: the grant is the record
// of a decision the middleware already made, it carries the organization that
// was authorized, and it carries what the caller may do. A signature taking an
// organization id separately would allow acting on one organization with
// another's authorization, and no amount of care at the call site makes that
// impossible — this makes it unrepresentable.
type Service struct {
	repo        Repository
	dir         Directory
	provisioner Provisioner
}

func NewService(repo Repository, dir Directory, provisioner Provisioner) *Service {
	return &Service{repo: repo, dir: dir, provisioner: provisioner}
}

// Organization returns one organization.
//
// It takes no user: whether the caller may see it was already decided by the
// middleware, which resolved their permissions in it and refused with a 404 if
// they had no active membership. Re-deriving that here would be a second answer
// to a question that already has one, and two answers eventually disagree.
func (s *Service) Organization(ctx context.Context, orgID uuid.UUID) (*models.Organization, error) {
	return s.repo.Organization(ctx, orgID)
}

// Mine lists the organizations the account belongs to. This is self-service —
// the account's own identity is the authorization — so it is the one place
// organizations are listed without a permission behind it.
func (s *Service) Mine(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	return s.dir.MembershipsForUser(ctx, userID)
}

// Rename changes the organization's display name. The slug is not editable:
// it appears in links people have already shared.
func (s *Service) Rename(ctx context.Context, grant *authz.Grant, name string) (*models.Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, ErrInvalidName
	}

	orgID := grant.OrganizationID()
	if err := s.repo.UpdateOrganization(ctx, orgID, name); err != nil {
		return nil, err
	}

	return s.repo.Organization(ctx, orgID)
}

// Delete soft deletes the organization. The default one is protected and the
// repository refuses it.
func (s *Service) Delete(ctx context.Context, grant *authz.Grant) error {
	return s.repo.DeleteOrganization(ctx, grant.OrganizationID())
}

func (s *Service) Members(ctx context.Context, grant *authz.Grant) ([]Member, error) {
	return s.repo.Members(ctx, grant.OrganizationID())
}

func (s *Service) Member(ctx context.Context, grant *authz.Grant, memberID uuid.UUID) (*Member, error) {
	return s.repo.Member(ctx, grant.OrganizationID(), memberID)
}

// AddMember invites an address into the organization with the given roles.
//
// The address is stored as an outstanding invitation, not resolved to an
// account. Looking it up would tell the caller whether the person is
// registered anywhere in the installation, which is a cross-tenant fact an
// organization administrator is not entitled to. Unknown and known addresses
// therefore produce the same row and the same response; the invitee accepts
// once they have an account.
func (s *Service) AddMember(
	ctx context.Context,
	grant *authz.Grant,
	email string,
	roleIDs []uuid.UUID,
) (*Member, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, ErrInvalidEmail
	}

	if err := s.ensureRolesAreGrantable(ctx, grant, roleIDs); err != nil {
		return nil, err
	}

	return s.repo.InviteMember(ctx, grant.OrganizationID(), email, roleIDs, grant.Actor(), time.Now().UTC())
}

// AcceptInvitation is self-service: the caller's identity is the authorization.
func (s *Service) AcceptInvitation(ctx context.Context, userID uuid.UUID, email string, memberID uuid.UUID) error {
	return s.dir.AcceptInvitation(ctx, memberID, userID, normalizeEmail(email), time.Now().UTC())
}

// DeclineInvitation withdraws an invitation addressed to this account.
func (s *Service) DeclineInvitation(ctx context.Context, email string, memberID uuid.UUID) error {
	return s.dir.DeclineInvitation(ctx, memberID, normalizeEmail(email))
}

// SetMemberStatus suspends or reinstates somebody.
func (s *Service) SetMemberStatus(
	ctx context.Context,
	grant *authz.Grant,
	memberID uuid.UUID,
	status models.MembershipStatus,
) error {
	if !status.Valid() {
		return ErrInvalidStatus
	}

	orgID := grant.OrganizationID()

	member, err := s.repo.Member(ctx, orgID, memberID)
	if err != nil {
		return err
	}

	// An invitation is accepted by the invitee, not flipped to active by an
	// administrator. PATCH would skip consent and, for an unknown address,
	// have no account to attach.
	if member.Status == models.MembershipInvited {
		return ErrInvalidStatus
	}

	return s.repo.SetMemberStatus(ctx, orgID, memberID, status, time.Now().UTC())
}

func (s *Service) RemoveMember(ctx context.Context, grant *authz.Grant, memberID uuid.UUID) error {
	return s.repo.RemoveMember(ctx, grant.OrganizationID(), memberID)
}

// SetMemberRoles replaces somebody's roles.
//
// Two rules apply, and they are the two ways this endpoint would otherwise be a
// way around the whole scheme: the caller may only assign roles whose
// permissions they hold themselves, and the last owner may not be demoted.
//
// Only the first is enforced here. The last-owner rule lives in the repository,
// because it has to read the owner count and write the new roles inside one
// transaction that locks the organization row — a check on this side of the
// boundary and a mutation on the other would reopen the race it exists to close.
// That is why ErrLastOwner comes back from a repository call rather than from
// anything visible in this function.
func (s *Service) SetMemberRoles(
	ctx context.Context,
	grant *authz.Grant,
	memberID uuid.UUID,
	roleIDs []uuid.UUID,
) (*Member, error) {
	orgID := grant.OrganizationID()

	if _, err := s.repo.Member(ctx, orgID, memberID); err != nil {
		return nil, err
	}

	if err := s.ensureRolesAreGrantable(ctx, grant, roleIDs); err != nil {
		return nil, err
	}

	if err := s.repo.ReplaceMemberRoles(ctx, orgID, memberID, roleIDs); err != nil {
		return nil, err
	}

	return s.repo.Member(ctx, orgID, memberID)
}

func (s *Service) Roles(ctx context.Context, grant *authz.Grant) ([]Role, error) {
	return s.repo.Roles(ctx, grant.OrganizationID())
}

func (s *Service) Role(ctx context.Context, grant *authz.Grant, roleID uuid.UUID) (*Role, error) {
	return s.repo.Role(ctx, grant.OrganizationID(), roleID)
}

// CreateRole defines a new role.
//
// The permissions are checked against the caller's own grant. Without that,
// roles.create is a permission to acquire every other permission: define a role
// holding platform-wide powers, assign it to yourself, and the authorization
// system has been talked out of its own rules.
func (s *Service) CreateRole(
	ctx context.Context,
	grant *authz.Grant,
	key, name, description string,
	permissions []authz.Permission,
) (*Role, error) {
	if err := authz.EnsureCanGrant(grant, permissions); err != nil {
		return nil, err
	}

	role := &models.Role{
		OrganizationID: grant.OrganizationID(),
		Key:            strings.TrimSpace(key),
		Name:           strings.TrimSpace(name),
		Description:    strings.TrimSpace(description),
	}

	return s.repo.CreateRole(ctx, grant.OrganizationID(), role, dedupe(permissions))
}

// UpdateRole renames a role. Shipped roles are refused: every organization's
// copy of "admin" must keep meaning the same thing, and a renamed one is a
// different role wearing the same key.
func (s *Service) UpdateRole(
	ctx context.Context,
	grant *authz.Grant,
	roleID uuid.UUID,
	name, description string,
) (*Role, error) {
	orgID := grant.OrganizationID()

	role, err := s.repo.Role(ctx, orgID, roleID)
	if err != nil {
		return nil, err
	}

	if role.IsSystem {
		return nil, ErrRoleProtected
	}

	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, ErrInvalidName
	}

	if err := s.repo.UpdateRole(ctx, orgID, roleID, name, strings.TrimSpace(description)); err != nil {
		return nil, err
	}

	return s.repo.Role(ctx, orgID, roleID)
}

// SetRolePermissions replaces what a role grants, subject to the same
// anti-escalation rule as creating one.
func (s *Service) SetRolePermissions(
	ctx context.Context,
	grant *authz.Grant,
	roleID uuid.UUID,
	permissions []authz.Permission,
) (*Role, error) {
	orgID := grant.OrganizationID()

	role, err := s.repo.Role(ctx, orgID, roleID)
	if err != nil {
		return nil, err
	}

	if role.IsSystem {
		return nil, ErrRoleProtected
	}

	if err := authz.EnsureCanGrant(grant, permissions); err != nil {
		return nil, err
	}

	if err := s.repo.ReplaceRolePermissions(ctx, orgID, roleID, dedupe(permissions)); err != nil {
		return nil, err
	}

	return s.repo.Role(ctx, orgID, roleID)
}

// DeleteRole removes a custom role that nobody holds.
//
// Refusing while it is assigned is the point. Deleting it anyway would take
// permissions away from people the caller never looked at, and the cascade
// leaves nothing behind to explain why they lost access.
func (s *Service) DeleteRole(ctx context.Context, grant *authz.Grant, roleID uuid.UUID) error {
	orgID := grant.OrganizationID()

	role, err := s.repo.Role(ctx, orgID, roleID)
	if err != nil {
		return err
	}

	if role.IsSystem {
		return ErrRoleProtected
	}

	if role.Members > 0 {
		return ErrRoleInUse
	}

	return s.repo.DeleteRole(ctx, orgID, roleID)
}

// ensureRolesAreGrantable is the anti-escalation rule for assignment.
//
// Checking the role's permissions rather than the role itself is what makes it
// airtight: a caller who may not grant organization.delete may not grant it by
// naming a role that happens to contain it either.
func (s *Service) ensureRolesAreGrantable(ctx context.Context, grant *authz.Grant, roleIDs []uuid.UUID) error {
	orgID := grant.OrganizationID()

	for _, roleID := range roleIDs {
		role, err := s.repo.Role(ctx, orgID, roleID)
		if err != nil {
			return err
		}

		if err := authz.EnsureCanGrant(grant, role.Permissions); err != nil {
			return err
		}
	}

	return nil
}

func dedupe(permissions []authz.Permission) []authz.Permission {
	out := make([]authz.Permission, 0, len(permissions))

	for _, perm := range permissions {
		if !slices.Contains(out, perm) {
			out = append(out, perm)
		}
	}

	return out
}

// normalizeEmail mirrors user.NormalizeEmail. It is repeated rather than
// imported so this package does not depend on the user domain for one string
// operation. Looking the address up would re-open the registration oracle
// AddMember exists to close.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
