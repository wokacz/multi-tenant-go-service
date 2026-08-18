package httptest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// AuthzFixture is one signed-in account plus the organization it belongs to.
type AuthzFixture struct {
	Server     *api.Server
	Repo       *memory.Authz
	Accounts   *user.Service
	Mailer     *CapturingMailer
	Token      string
	UserID     uuid.UUID
	OrgID      uuid.UUID
	Membership uuid.UUID
}

// NewAuthzFixture registers Ada, signs her in, and puts her in an organization
// holding the given shipped roles.
func NewAuthzFixture(t *testing.T, roles ...authz.RoleKey) *AuthzFixture {
	t.Helper()

	return NewAuthzFixtureWith(t, telemetry.Disabled(), roles...)
}

// NewAuthzFixtureWith is the same fixture with telemetry attached, for the tests that
// read the counters back.
func NewAuthzFixtureWith(t *testing.T, tel *telemetry.Telemetry, roles ...authz.RoleKey) *AuthzFixture {
	t.Helper()

	mailer := &CapturingMailer{}
	server, repo, accounts := NewTestAPIConfigTel(t, mailer, memory.NewUsers(), tel, nil)

	RegisterAda(t, server)
	session := SignInAda(t, server, "", http.StatusCreated)

	orgID := repo.SeedOrganization("acme", "Acme")

	roleIDs := make([]uuid.UUID, 0, len(roles))
	for _, key := range roles {
		roleIDs = append(roleIDs, repo.SeedShippedRole(orgID, key))
	}

	return &AuthzFixture{
		Server:     server,
		Repo:       repo,
		Accounts:   accounts,
		Mailer:     mailer,
		Token:      session.Token,
		UserID:     session.User.ID,
		OrgID:      orgID,
		Membership: repo.SeedMember(orgID, session.User.ID, ent.MembershipActive, roleIDs...),
	}
}

// GetOrg fetches the fixture's own organization and returns just the status,
// which is all the tests about revocation and suspension care about.
func (f *AuthzFixture) GetOrg(t *testing.T) int {
	t.Helper()

	return Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/orgs/"+f.OrgID.String(), "", f.Token, "")).Code
}
