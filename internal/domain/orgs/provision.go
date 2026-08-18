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
	//
	// Each row carries how many owners the organization has, counted by exactly the
	// definition the last-owner rule uses — an active membership holding the owner
	// role whose account has not been deleted. Two answers to "does this have an
	// owner" would eventually disagree, and the disagreement would show up as a
	// listing saying one while the guard says none.
	AllOrganizations(ctx context.Context, filter OrganizationFilter, limit, offset int) ([]OrganizationSummary, error)

	// GrantSystemRole assigns an installation-wide role by key. It is
	// idempotent: granting twice is not an error, because one caller is a
	// deployment step that may well run again.
	//
	// It records the grant when there is an actor on the context. There was no
	// entry at all for a long time, which meant the most consequential change in
	// the installation — platform_admin covers every platform.* key — left no
	// trace, while the design document claimed every change to authority was
	// logged.
	GrantSystemRole(ctx context.Context, userID uuid.UUID, key authz.RoleKey, grantedBy uuid.UUID) error

	// RevokeSystemRole takes one back. Revoking one that was never granted is not
	// an error, for the same reason granting twice is not: the caller asked for a
	// state and that is the state they get.
	RevokeSystemRole(ctx context.Context, userID uuid.UUID, key authz.RoleKey) error

	// SystemRoleHolders lists who holds an installation-wide role, with the names
	// and addresses resolved, because "who administers this installation" is a
	// question about people rather than about ids.
	SystemRoleHolders(ctx context.Context) ([]SystemRoleHolder, error)
}

// OrganizationSummary is one organization as an installation administrator sees
// it: the row, plus the one fact that cannot be read off it.
type OrganizationSummary struct {
	models.Organization

	// Owners is how many people can administer it.
	//
	// Zero is the state this exists to make visible. An organization gets there by
	// losing its last owner to a deleted account — the membership row outlives the
	// person, and every rule that matters stopped counting it — and until now
	// nothing could find such an organization. Appointing a new owner has been
	// possible since the platform endpoint existed; knowing where to appoint one
	// was the missing half.
	Owners int
}

// OrganizationFilter narrows the installation-wide listing.
//
// A struct rather than a bare bool because the call site reads as a sentence, and
// because the next filter goes here instead of becoming a second positional
// argument nobody can tell apart from the first.
type OrganizationFilter struct {
	// WithoutOwner keeps only the organizations nobody can administer.
	WithoutOwner bool
}

// SystemRoleHolder is one installation-wide grant.
type SystemRoleHolder struct {
	UserID    uuid.UUID
	Name      string
	Email     string
	RoleKey   string
	GrantedBy *uuid.UUID
	GrantedAt time.Time
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
// A pending invitation to the default organization stops it, and the reason is no
// longer a unique-index collision — invitations live in their own table now.
// Joining as "member" first would make the invitation unacceptable: accepting
// creates the membership it promised, and an account that is already a member gets
// ErrAlreadyMember, so the roles that were actually offered could never be taken
// up. The invitee ends up with no membership until they accept, which is correct:
// self-service works without one, and the alternative is to silently hand them
// less than they were offered.
func (s *Service) JoinDefaultOrganization(ctx context.Context, userID uuid.UUID, email string) error {
	org, err := s.EnsureDefaultOrganization(ctx)
	if err != nil {
		return err
	}

	waiting, err := s.hasRowIn(ctx, userID, email, org.ID)
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

// hasRowIn reports whether the account already has a membership in the
// organization, or a pending invitation to it.
//
// The two are looked up separately because they are separate things now: a
// membership belongs to an account, an invitation to an address. An invitation
// counts here for the reason described above — joining first would make it
// impossible to accept.
//
// It is still a read before a write, so an invitation issued between this check
// and the insert is missed and the invitee joins as a plain member. That leaves an
// invitation they cannot accept until somebody withdraws it, which is a nuisance
// rather than a privilege problem, and closing it would mean holding a lock across
// two tables on the registration path.
func (s *Service) hasRowIn(ctx context.Context, userID uuid.UUID, email string, orgID uuid.UUID) (bool, error) {
	memberships, err := s.dir.MembershipsForUser(ctx, userID)
	if err != nil {
		return false, err
	}

	for i := range memberships {
		if memberships[i].Organization.ID == orgID {
			return true, nil
		}
	}

	invitations, err := s.dir.InvitationsForEmail(ctx, normalizeEmail(email), time.Now().UTC())
	if err != nil {
		return false, err
	}

	for i := range invitations {
		if invitations[i].Organization.ID == orgID {
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
func (s *Service) AllOrganizations(
	ctx context.Context,
	filter OrganizationFilter,
	limit, offset int,
) ([]OrganizationSummary, error) {
	if limit <= 0 || limit > MaxOrganizationPage {
		limit = MaxOrganizationPage
	}

	if offset < 0 {
		offset = 0
	}

	return s.provisioner.AllOrganizations(ctx, filter, limit, offset)
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
	if err := s.grantOwnership(ctx, orgID, userID); err != nil {
		return err
	}

	if !alsoPlatformAdmin {
		return nil
	}

	return s.provisioner.GrantSystemRole(ctx, userID, authz.RolePlatformAdmin, uuid.Nil)
}

// AppointOwner is the same act from inside the API, authorized at system scope.
//
// It is the half of "create an organization" that was missing. CreateOrganization
// deliberately does not add the creator, so an organization made through the
// platform endpoint had nobody in it — and nobody could be added, because adding a
// member needs a permission inside that organization and a platform administrator
// has none there. The only way out was SQL.
//
// It does not grant the platform role. Being an owner of one organization and
// administering the installation are separate authorizations, and since H3 they have
// separate endpoints; the bootstrap command still does both because breaking the
// circle needs both.
// It takes no grant, like every other system-scoped method here: the grant carries
// the organization a decision was made in, and at system scope there is none. The
// actor for the audit entry travels on the context.
func (s *Service) AppointOwner(ctx context.Context, orgID, userID uuid.UUID) error {
	// The organization has to be live. Its roles outlive a soft delete — roles are
	// not soft-deleted — so without this an owner could be appointed to an
	// organization that no longer exists.
	if _, err := s.repo.Organization(ctx, orgID); err != nil {
		return err
	}

	return s.grantOwnership(ctx, orgID, userID)
}

// grantOwnership makes the account an owner, whether or not it is already a member.
func (s *Service) grantOwnership(ctx context.Context, orgID, userID uuid.UUID) error {
	owner, err := s.repo.RoleByKey(ctx, orgID, string(authz.RoleOwner))
	if err != nil {
		return err
	}

	member, err := s.repo.MemberByUser(ctx, orgID, userID)
	switch {
	case err == nil:
		// The new set is exactly the owner role, so nothing can be losing it.
		return s.repo.ReplaceMemberRoles(ctx, orgID, member.ID,
			[]uuid.UUID{owner.ID}, RefuseLastOwnerLoss(false))
	case errors.Is(err, ErrNotFound):
		_, err := s.repo.AddMember(ctx, orgID, userID,
			[]uuid.UUID{owner.ID}, uuid.Nil, time.Now().UTC())

		return err
	default:
		return err
	}
}
