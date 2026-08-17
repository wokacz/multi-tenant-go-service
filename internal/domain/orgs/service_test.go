package orgs_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// These run against the shared in-memory repository, the same way the authz
// service tests do, so they exercise the membership semantics the API tests see.

func testService(t *testing.T) (*orgs.Service, *memory.Authz) {
	t.Helper()

	repo := memory.NewAuthz(memory.NewUsers())

	return orgs.NewService(repo, repo, repo), repo
}

// TestSetMemberStatusRefusesInvited moves the rule off the edge of the API.
//
// The DTO's enum already stops "invited" arriving over HTTP, which is why no
// handler test can reach this. That left the rule in exactly one place — the
// contract — and out of reach of any caller that is not an HTTP request, while
// the state it produces is one nothing else in the code expects: a membership
// marked as waiting for consent that already has an account attached.
func TestSetMemberStatusRefusesInvited(t *testing.T) {
	service, repo := testService(t)

	orgID := repo.SeedOrganization("acme", "Acme")
	viewer := repo.SeedShippedRole(orgID, authz.RoleViewer)

	actor := uuid.Must(uuid.NewV7())
	repo.SeedMember(orgID, actor, models.MembershipActive, repo.SeedShippedRole(orgID, authz.RoleOwner))

	// A subject holding less than the caller, so the rank rule is satisfied and
	// this test is about the status argument alone.
	subject := uuid.Must(uuid.NewV7())
	member := repo.SeedMember(orgID, subject, models.MembershipActive, viewer)

	grant := authz.NewGrant(actor, orgID, []authz.Permission{
		authz.PermMembersSuspend,
		authz.PermOrganizationRead,
	})

	err := service.SetMemberStatus(t.Context(), grant, member, models.MembershipInvited)
	if !errors.Is(err, orgs.ErrInvalidStatus) {
		t.Errorf("SetMemberStatus(invited) = %v, want ErrInvalidStatus", err)
	}

	// The two the operation is actually for still work, so the narrower check has
	// not taken out more than it should.
	for _, status := range []models.MembershipStatus{models.MembershipSuspended, models.MembershipActive} {
		if err := service.SetMemberStatus(t.Context(), grant, member, status); err != nil {
			t.Errorf("SetMemberStatus(%s) = %v, want it accepted", status, err)
		}
	}
}
