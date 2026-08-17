package orgs

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// Provisioner creates organizations and the roles they start with.
//
// It is separate from Repository because none of it is organization-scoped —
// creating one is the act of bringing the scope into being — and folding it in
// would put an exception into the rule that every scoped method names an
// organization.
type Provisioner interface {
	// OrganizationBySlug returns the organization with that slug, or ErrNotFound.
	OrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error)

	// CreateOrganization stores the organization together with the roles from
	// the shipped catalog, in one transaction. An organization that existed
	// briefly without its roles would be one nobody could administer.
	CreateOrganization(ctx context.Context, org *models.Organization, roles []authz.RoleDefinition) (*models.Organization, error)

	// AllOrganizations lists every organization in the installation. It is the
	// one listing that crosses tenants, which is why it sits behind a
	// system-scope permission rather than an organization-scoped one.
	AllOrganizations(ctx context.Context, limit, offset int) ([]models.Organization, error)

	// GrantSystemRole assigns an installation-wide role by key. It is
	// idempotent: granting twice is not an error, because the only caller is a
	// deployment step that may well run again.
	GrantSystemRole(ctx context.Context, userID uuid.UUID, key authz.RoleKey, grantedBy uuid.UUID) error
}

// DefaultOrganizationName is what the seeded organization is called until
// somebody renames it.
const DefaultOrganizationName = "Default"

// EnsureDefaultOrganization returns the default organization, creating it with
// its shipped roles if this is the first time.
//
// It is idempotent and safe to call on every start. The alternative — seeding
// it from a migration — would put the permission lists of every shipped role
// into SQL, where no test can compare them against the catalog they were copied
// from, and where changing a role means writing a backfill by hand.
func (s *Service) EnsureDefaultOrganization(ctx context.Context) (*models.Organization, error) {
	existing, err := s.provisioner.OrganizationBySlug(ctx, models.DefaultOrganizationSlug)
	if err == nil {
		return existing, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	org := &models.Organization{
		Slug: models.DefaultOrganizationSlug,
		Name: DefaultOrganizationName,
	}

	// Protected, so the ordinary delete path refuses it. An installation whose
	// only organization was deleted has no working accounts and no screen to
	// undo it from.
	org.IsProtected = true

	created, err := s.provisioner.CreateOrganization(ctx, org, authz.OrganizationRoles())
	if err == nil {
		return created, nil
	}

	// Two processes starting at once both miss the lookup and both insert; the
	// unique index on the slug decides, and the loser simply reads what the
	// winner wrote.
	if errors.Is(err, ErrSlugTaken) {
		return s.provisioner.OrganizationBySlug(ctx, models.DefaultOrganizationSlug)
	}

	return nil, err
}

// JoinDefaultOrganization puts a newly registered account into the default
// organization as a plain member.
//
// This is what stops a fresh account being able to do nothing at all. The role
// is "member" rather than something wider on purpose: registering is not an act
// of authorization, and an installation that wants more gives it out
// explicitly.
//
// An outstanding invitation to the default organization stops it. AddMember
// would collide with that invitation on (organization_id, email) and the
// repository would claim it — activating a membership nobody accepted and
// replacing the roles it was issued with by "member". Registering is not an
// accept: the address on a fresh account is not verified, so it proves nothing
// about the mailbox. Such an account therefore has no active membership at all
// until it accepts through /v1/me/invitations, which is correct — self-service
// works without one, and the alternative silently downgrades what the invitee
// was offered.
func (s *Service) JoinDefaultOrganization(ctx context.Context, userID uuid.UUID) error {
	org, err := s.EnsureDefaultOrganization(ctx)
	if err != nil {
		return err
	}

	waiting, err := s.hasRowIn(ctx, userID, org.ID)
	if err != nil {
		return err
	}

	if waiting {
		return nil
	}

	role, err := s.repo.RoleByKey(ctx, org.ID, string(authz.RoleMember))
	if err != nil {
		return err
	}

	_, err = s.repo.AddMember(ctx, org.ID, userID, []uuid.UUID{role.ID}, uuid.Nil, time.Now().UTC())
	if errors.Is(err, ErrAlreadyMember) {
		return nil
	}

	return err
}

// hasRowIn reports whether the account already has any row in the organization:
// an active membership, a suspension, or an invitation addressed to it.
//
// It goes through the directory rather than the scoped repository because an
// invited row carries no user id — MemberByUser cannot see it, and matching the
// address is exactly what MembershipsForUser already does.
//
// It is a read before a write, so there is a window: an invitation created
// between this check and AddMember's insert is still claimed. The outcome is a
// downgrade rather than an escalation, and the window closes for good when
// invitations move to their own table and stop sharing the uniqueness
// constraint with memberships.
func (s *Service) hasRowIn(ctx context.Context, userID, orgID uuid.UUID) (bool, error) {
	memberships, err := s.dir.MembershipsForUser(ctx, userID)
	if err != nil {
		return false, err
	}

	for i := range memberships {
		if memberships[i].Organization.ID == orgID {
			return true, nil
		}
	}

	return false, nil
}

// MaxOrganizationPage caps the installation-wide listing, the same way
// user.MaxUserPage caps the account listing.
const MaxOrganizationPage = 100

// AllOrganizations lists every organization. Authorization happened at system
// scope, so there is no grant to read an organization from.
func (s *Service) AllOrganizations(ctx context.Context, limit, offset int) ([]models.Organization, error) {
	if limit <= 0 || limit > MaxOrganizationPage {
		limit = MaxOrganizationPage
	}

	if offset < 0 {
		offset = 0
	}

	return s.provisioner.AllOrganizations(ctx, limit, offset)
}

// CreateOrganization sets up a new organization with the shipped roles.
//
// It deliberately does not add the creator as owner. A platform administrator
// creating an organization for somebody else should not silently end up inside
// it — that is a separate, visible act, and PromoteToOwner is what performs it.
func (s *Service) CreateOrganization(ctx context.Context, slug, name string) (*models.Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, ErrInvalidName
	}

	org := &models.Organization{Slug: strings.TrimSpace(slug), Name: name}

	return s.provisioner.CreateOrganization(ctx, org, authz.OrganizationRoles())
}

// DeleteOrganizationByID removes any organization, for the installation-wide
// path where the caller was authorized at system scope rather than inside it.
func (s *Service) DeleteOrganizationByID(ctx context.Context, orgID uuid.UUID) error {
	return s.repo.DeleteOrganization(ctx, orgID)
}

// PromoteToOwner makes an account an owner of an organization, and optionally a
// platform administrator.
//
// It is the deployment step that breaks the bootstrap circle: the first owner
// cannot be created through the API, because creating them would need a
// permission nobody holds yet. It deliberately does not run on "the first
// account to register" — with open registration that is a race anybody can win.
func (s *Service) PromoteToOwner(ctx context.Context, orgID, userID uuid.UUID, alsoPlatformAdmin bool) error {
	owner, err := s.repo.RoleByKey(ctx, orgID, string(authz.RoleOwner))
	if err != nil {
		return err
	}

	member, err := s.repo.MemberByUser(ctx, orgID, userID)
	switch {
	case err == nil:
		if err := s.repo.ReplaceMemberRoles(ctx, orgID, member.ID, []uuid.UUID{owner.ID}); err != nil {
			return err
		}
	case errors.Is(err, ErrNotFound):
		if _, err := s.repo.AddMember(ctx, orgID, userID,
			[]uuid.UUID{owner.ID}, uuid.Nil, time.Now().UTC()); err != nil {
			return err
		}
	default:
		return err
	}

	if !alsoPlatformAdmin {
		return nil
	}

	return s.provisioner.GrantSystemRole(ctx, userID, authz.RolePlatformAdmin, uuid.Nil)
}
