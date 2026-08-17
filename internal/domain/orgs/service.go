package orgs

import (
	"context"
	"errors"
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

// SetMemberStatus suspends or reinstates somebody.
func (s *Service) SetMemberStatus(
	ctx context.Context,
	grant *authz.Grant,
	memberID uuid.UUID,
	status models.MembershipStatus,
) error {
	// status.Valid() would do now that "invited" is gone from the enum, but the
	// two are written out on purpose: this operation suspends and reinstates, and
	// a status added to the enum later must not become settable here by default.
	if status != models.MembershipActive && status != models.MembershipSuspended {
		return ErrInvalidStatus
	}

	orgID := grant.OrganizationID()

	member, err := s.repo.Member(ctx, orgID, memberID)
	if err != nil {
		return err
	}

	if err := s.ensureCanAffectMember(ctx, grant, member); err != nil {
		return err
	}

	// Suspending takes the capability away; reinstating gives it back, so only the
	// first can leave the organization without an owner.
	guard := RefuseLastOwnerLoss(!status.GrantsPermissions())

	return s.repo.SetMemberStatus(ctx, orgID, memberID, status, time.Now().UTC(), guard)
}

func (s *Service) RemoveMember(ctx context.Context, grant *authz.Grant, memberID uuid.UUID) error {
	orgID := grant.OrganizationID()

	// Read first, which this did not need to do before the rank rule existed. It
	// also turns a membership whose account has been deleted into ErrNotFound
	// here — Member refuses those — so the one path that must still work on such a
	// row calls the repository directly, further down.
	member, err := s.repo.Member(ctx, orgID, memberID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// No member to compare ranks with. The row may still exist with a
			// deleted account behind it, and removing it is the only way to clean
			// that up, so the repository decides.
			return s.repo.RemoveMember(ctx, orgID, memberID, RefuseLastOwnerLoss(true))
		}

		return err
	}

	if err := s.ensureCanAffectMember(ctx, grant, member); err != nil {
		return err
	}

	return s.repo.RemoveMember(ctx, orgID, memberID, RefuseLastOwnerLoss(true))
}

// SetMemberRoles replaces somebody's roles.
//
// Three rules apply, and each is a way this endpoint would otherwise be a route
// around the whole scheme: the caller may not act on somebody who outranks them,
// may only assign roles whose permissions they hold themselves, and may not leave
// the organization without an owner.
//
// All three are stated here. The last one is handed to the repository as a guard
// rather than checked inline, because it has to read the owner count and write the
// new roles inside one transaction with the organization row locked — a check on
// this side and a mutation on the other would reopen the race it exists to close.
// The rule is domain code; only its timing belongs to the store. That is why
// ErrLastOwner still surfaces from a repository call rather than from
// anything visible in this function.
func (s *Service) SetMemberRoles(
	ctx context.Context,
	grant *authz.Grant,
	memberID uuid.UUID,
	roleIDs []uuid.UUID,
) (*Member, error) {
	orgID := grant.OrganizationID()

	member, err := s.repo.Member(ctx, orgID, memberID)
	if err != nil {
		return nil, err
	}

	// Both directions: what the member already holds must be within the caller's
	// reach, and so must what they are about to be given. Checking only the second
	// let an administrator replace an owner's roles with "viewer" — viewer being
	// well inside an admin's own powers.
	if err := s.ensureCanAffectMember(ctx, grant, member); err != nil {
		return nil, err
	}

	if err := s.ensureRolesAreGrantable(ctx, grant, roleIDs); err != nil {
		return nil, err
	}

	losing, err := s.replacingDropsOwner(ctx, orgID, roleIDs)
	if err != nil {
		return nil, err
	}

	if err := s.repo.ReplaceMemberRoles(ctx, orgID, memberID, roleIDs, RefuseLastOwnerLoss(losing)); err != nil {
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
	if err := ensureOrganizationScoped(permissions); err != nil {
		return nil, err
	}

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

	if err := ensureOrganizationScoped(permissions); err != nil {
		return nil, err
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

	// role.Members is not consulted: it was read on another connection and the
	// delete runs in its own transaction, so a role assigned in between would lose
	// the assignment to the cascade. The count that decides happens inside.
	return s.repo.DeleteRole(ctx, orgID, roleID, RefuseRoleInUse())
}

// ensureRolesAreGrantable is the anti-escalation rule for assignment.
//
// Checking the role's permissions rather than the role itself is what makes it
// airtight: a caller who may not grant organization.delete may not grant it by
// naming a role that happens to contain it either.
// ensureOrganizationScoped refuses an installation-wide permission on a role.
//
// A role lives in an organization, so a platform.* key could never be granted
// through one. Without this, EnsureCanGrant reports it as an escalation attempt —
// telling the caller they lack a permission, when the truth is that no role here
// could carry it however their own roles were configured.
//
// It runs before EnsureCanGrant so the more specific answer wins: a caller who
// holds neither would otherwise be told about the wrong problem.
func ensureOrganizationScoped(permissions []authz.Permission) error {
	for _, perm := range permissions {
		def, ok := authz.Lookup(perm)
		if !ok {
			return authz.ErrUnknownPermission
		}

		if def.Scope != authz.ScopeOrganization {
			return authz.ErrWrongScope
		}
	}

	return nil
}

// RefuseLastOwnerLoss is the last-owner rule.
//
// losing says whether the change takes the owner capability away from the
// subject — suspending them, removing them, or replacing their roles with a set
// that has no owner in it. Reinstating somebody or widening their roles is not a
// loss and needs no check.
//
// The rule lived in SQL until now, and in a hand-written copy inside the
// in-memory fake. That is how the two came to disagree about a membership whose
// account had been deleted: one counted it as an owner, the other did not, and the
// row became impossible to remove. Here there is one statement of it, in Go,
// testable without a database — and the repository still runs it inside the
// transaction that makes the change, with the organization row locked, so nothing
// about the serialisation is given up in exchange.
// It is exported because the store's own tests need to exercise the real rule
// against real SQL. A test that wrote its own copy of the comparison could pass
// while the rule the service uses said something else, which is the failure this
// whole rearrangement exists to prevent.
func RefuseLastOwnerLoss(losing bool) OwnerGuard {
	return func(state OwnerState) error {
		if !losing || !state.SubjectHoldsOwner {
			return nil
		}

		if state.Owners <= 1 {
			return ErrLastOwner
		}

		return nil
	}
}

// RefuseRoleInUse is the companion rule for deleting a role.
//
// Cascading the delete would take permissions away from people the caller never
// looked at, and without counting inside the transaction that is exactly what
// happened: the count was read on one connection and the delete ran on another,
// so a role assigned in between lost the assignment to the cascade.
func RefuseRoleInUse() RoleGuard {
	return func(holders int) error {
		if holders > 0 {
			return ErrRoleInUse
		}

		return nil
	}
}

// replacingDropsOwner reports whether roleIDs leaves out the owner role.
//
// The lookup happens outside the transaction, which is safe because the answer
// cannot go stale: the owner role is materialised from the shipped catalog and
// carries IsSystem, so nothing can delete or rekey it while this runs.
func (s *Service) replacingDropsOwner(ctx context.Context, orgID uuid.UUID, roleIDs []uuid.UUID) (bool, error) {
	owner, err := s.repo.RoleByKey(ctx, orgID, string(authz.RoleOwner))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// No owner role here at all, so no change can drop it.
			return false, nil
		}

		return false, err
	}

	return !slices.Contains(roleIDs, owner.ID), nil
}

// ensureCanAffectMember applies the rank rule to one membership.
//
// Acting on oneself returns early. That is a guarantee rather than a fix: the
// grant is resolved from the caller's own roles in this organization, so their
// membership's permissions are the same set and the comparison would pass anyway.
// Saying it outright means "remove me from this organization" cannot start
// failing because the two are computed from different places later on.
func (s *Service) ensureCanAffectMember(ctx context.Context, grant *authz.Grant, member *Member) error {
	if member.UserID != uuid.Nil && member.UserID == grant.Actor() {
		return nil
	}

	permissions, err := s.repo.MemberPermissions(ctx, grant.OrganizationID(), member.ID)
	if err != nil {
		return err
	}

	return authz.EnsureCanAffect(grant, permissions)
}

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
