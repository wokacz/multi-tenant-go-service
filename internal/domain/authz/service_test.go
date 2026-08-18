package authz_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// These run against the shared in-memory repository rather than the stub above,
// so they exercise the same membership and role semantics the API tests do.

func testAuthz(t *testing.T) (*authz.Service, *memory.Authz) {
	t.Helper()

	repo := memory.NewAuthz(nil)

	return authz.NewService(repo), repo
}

// permissions is the assertion these tests share: authorize for something the
// actor holds, then read the whole grant.
func permissions(t *testing.T, svc *authz.Service, user, org uuid.UUID, probe authz.Permission) []authz.Permission {
	t.Helper()

	grant, err := svc.Authorize(t.Context(), authz.Request{Actor: user, Org: org, Permission: probe})
	if err != nil {
		t.Fatalf("Authorize(%q) = _, %v", probe, err)
	}

	return grant.Permissions()
}

func TestPermissionsAreTheUnionOfEveryRole(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")

	readers := repo.SeedRole(org, "readers", string(authz.PermMembersRead))
	editors := repo.SeedRole(org, "editors", string(authz.PermRolesRead), string(authz.PermRolesUpdate))

	repo.SeedMember(org, user, ent.MembershipActive, readers, editors)

	got := permissions(t, svc, user, org, authz.PermMembersRead)
	want := []authz.Permission{authz.PermMembersRead, authz.PermRolesRead, authz.PermRolesUpdate}

	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("permissions = %v, want %v", got, want)
	}
}

// TestOnlyAnActiveMembershipGrantsAnything is the rule that a suspension is not a
// deletion.
//
// It must be indistinguishable from "no such organization" at the boundary, which
// is why it resolves to ErrNotMember rather than to an empty grant: an empty grant
// would produce a 403, and a 403 confirms the organization exists. An invitation
// used to be the second case here and is no longer a membership at all.
func TestOnlyAnActiveMembershipGrantsAnything(t *testing.T) {
	for name, status := range map[string]ent.MembershipStatus{
		"suspended": ent.MembershipSuspended,
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo := testAuthz(t)

			user := uuid.Must(uuid.NewV7())
			org := repo.SeedOrganization("acme", "Acme")
			role := repo.SeedRole(org, "readers", string(authz.PermMembersRead))

			repo.SeedMember(org, user, status, role)

			_, err := svc.Authorize(t.Context(), authz.Request{
				Actor: user, Org: org, Permission: authz.PermMembersRead,
			})
			if !errors.Is(err, authz.ErrNotMember) {
				t.Fatalf("Authorize() for a %s membership = %v, want ErrNotMember", name, err)
			}
		})
	}
}

// TestAMemberWithNoRolesIsStillAMember is the other side of the same coin. This
// person must get a 403, not a 404: they can see the organization exists, so
// hiding it from them would only confuse.
func TestAMemberWithNoRolesIsStillAMember(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")

	repo.SeedMember(org, user, ent.MembershipActive)

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: org, Permission: authz.PermMembersRead,
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Authorize() for a member with no roles = %v, want ErrForbidden", err)
	}
}

// TestRevokingARoleTakesEffectImmediately is why permissions are not carried in
// the token. Nothing is re-issued here and no session is touched; the next
// decision simply reads the new state.
func TestRevokingARoleTakesEffectImmediately(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")
	role := repo.SeedRole(org, "editors", string(authz.PermRolesUpdate))
	membership := repo.SeedMember(org, user, ent.MembershipActive, role)

	if _, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: org, Permission: authz.PermRolesUpdate,
	}); err != nil {
		t.Fatalf("Authorize() before revoking = %v, want nil", err)
	}

	repo.SeedMemberRoles(membership)

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: org, Permission: authz.PermRolesUpdate,
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Authorize() after revoking = %v, want ErrForbidden", err)
	}
}

// TestSuspendingAMemberTakesEffectImmediately is the same guarantee for the
// heavier action.
func TestSuspendingAMemberTakesEffectImmediately(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")
	role := repo.SeedShippedRole(org, authz.RoleAdmin)
	membership := repo.SeedMember(org, user, ent.MembershipActive, role)

	repo.SeedMemberStatus(membership, ent.MembershipSuspended)

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: org, Permission: authz.PermMembersRead,
	})
	if !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("Authorize() after suspension = %v, want ErrNotMember", err)
	}
}

// TestStoredKeysOutsideTheCatalogGrantNothing is the drift guard, end to end. A
// permission dropped from the code leaves its rows behind; they must stop
// conferring anything the moment the catalog no longer defines them.
func TestStoredKeysOutsideTheCatalogGrantNothing(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")
	role := repo.SeedRole(org, "legacy", "organization.teleport", string(authz.PermMembersRead))

	repo.SeedMember(org, user, ent.MembershipActive, role)

	got := permissions(t, svc, user, org, authz.PermMembersRead)
	want := []authz.Permission{authz.PermMembersRead}

	if !slices.Equal(got, want) {
		t.Errorf("permissions = %v, want %v — a key outside the catalog was honoured", got, want)
	}
}

// TestAnAssignmentToADeletedRoleGrantsNothing covers the window between
// deleting a role and the cascade landing.
func TestAnAssignmentToADeletedRoleGrantsNothing(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")
	kept := repo.SeedRole(org, "readers", string(authz.PermMembersRead))
	gone := repo.SeedRole(org, "editors", string(authz.PermRolesUpdate))

	repo.SeedMember(org, user, ent.MembershipActive, kept, gone)
	repo.SeedDeleteRole(gone)

	got := permissions(t, svc, user, org, authz.PermMembersRead)
	want := []authz.Permission{authz.PermMembersRead}

	if !slices.Equal(got, want) {
		t.Errorf("permissions = %v, want %v", got, want)
	}
}

func TestASoftDeletedOrganizationGrantsNothing(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")
	role := repo.SeedShippedRole(org, authz.RoleOwner)

	repo.SeedMember(org, user, ent.MembershipActive, role)
	repo.SeedSoftDeletedOrganization(org)

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: org, Permission: authz.PermMembersRead,
	})
	if !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("Authorize() into a deleted organization = %v, want ErrNotMember", err)
	}
}

// TestASoftDeletedAccountHoldsNothing matters because a soft delete never fires
// the foreign key cascade, so the memberships are all still there.
func TestASoftDeletedAccountHoldsNothing(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())
	org := repo.SeedOrganization("acme", "Acme")
	role := repo.SeedShippedRole(org, authz.RoleOwner)

	repo.SeedMember(org, user, ent.MembershipActive, role)
	repo.SeedSystemRole(user, string(authz.RolePlatformAdmin))
	repo.SeedSoftDeletedUser(user)

	if _, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: org, Permission: authz.PermMembersRead,
	}); !errors.Is(err, authz.ErrNotMember) {
		t.Errorf("Authorize() for a deleted account = %v, want ErrNotMember", err)
	}

	if _, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Permission: authz.PermPlatformUsersRead,
	}); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("Authorize() for a deleted account's system role = %v, want ErrForbidden", err)
	}
}

// TestOrganizationsDoNotBleedIntoEachOther is the multi-tenancy guarantee. The
// same person is an owner in one organization and a viewer in another, and the
// decision must depend on which one was asked about.
func TestOrganizationsDoNotBleedIntoEachOther(t *testing.T) {
	svc, repo := testAuthz(t)

	user := uuid.Must(uuid.NewV7())

	acme := repo.SeedOrganization("acme", "Acme")
	repo.SeedMember(acme, user, ent.MembershipActive, repo.SeedShippedRole(acme, authz.RoleOwner))

	globex := repo.SeedOrganization("globex", "Globex")
	repo.SeedMember(globex, user, ent.MembershipActive, repo.SeedShippedRole(globex, authz.RoleViewer))

	if _, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: acme, Permission: authz.PermOrganizationDelete,
	}); err != nil {
		t.Fatalf("Authorize() as owner of acme = %v, want nil", err)
	}

	if _, err := svc.Authorize(t.Context(), authz.Request{
		Actor: user, Org: globex, Permission: authz.PermOrganizationDelete,
	}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Authorize() as viewer of globex = %v, want ErrForbidden", err)
	}

	// And the grant returned for one must not describe the other.
	got := permissions(t, svc, user, globex, authz.PermOrganizationRead)
	if slices.Contains(got, authz.PermOrganizationDelete) {
		t.Errorf("the globex grant carries acme's permissions: %v", got)
	}
}

// TestAStrangerIsNotAMember is the case that must never become a 403.
func TestAStrangerIsNotAMember(t *testing.T) {
	svc, repo := testAuthz(t)

	org := repo.SeedOrganization("acme", "Acme")

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor: uuid.Must(uuid.NewV7()), Org: org, Permission: authz.PermOrganizationRead,
	})
	if !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("Authorize() for a stranger = %v, want ErrNotMember", err)
	}
}
