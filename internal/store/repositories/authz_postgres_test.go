package repositories_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
)

// These cover the one query the in-memory fake reimplements in Go: the join
// that resolves a user's permissions in an organization. Its LEFT JOINs, its
// deleted_at filters and its status filter are each load-bearing, and each of
// them is invisible to every test that runs against the fake.
//
//	POSTGRES_TEST=1 go test ./internal/store/repositories -v

// newOrganization inserts an organization with a unique slug so repeated runs
// against the same database do not collide.
func newOrganization(t *testing.T, db *store.DB) *models.Organization {
	t.Helper()

	org := &models.Organization{
		Slug: "org-" + uuid.Must(uuid.NewV7()).String(),
		Name: "Test organization",
	}

	if err := db.Create(org).Error; err != nil {
		t.Fatalf("create organization: %v", err)
	}

	return org
}

// newRole inserts a role and its permissions. The keys are written as given so a
// test can store one the catalog does not define.
func newRole(t *testing.T, db *store.DB, orgID uuid.UUID, key string, permissions ...string) *models.Role {
	t.Helper()

	role := &models.Role{OrganizationID: orgID, Key: key, Name: key}
	if err := db.Create(role).Error; err != nil {
		t.Fatalf("create role: %v", err)
	}

	for _, perm := range permissions {
		rp := &models.RolePermission{RoleID: role.ID, PermissionKey: perm}
		if err := db.Create(rp).Error; err != nil {
			t.Fatalf("create role permission: %v", err)
		}
	}

	return role
}

func newMembership(
	t *testing.T,
	db *store.DB,
	orgID, userID uuid.UUID,
	status models.MembershipStatus,
	roleIDs ...uuid.UUID,
) *models.Membership {
	t.Helper()

	membership := &models.Membership{
		OrganizationID: orgID,
		UserID:         userID,
		Status:         status,
	}
	if err := db.Create(membership).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	for _, roleID := range roleIDs {
		mr := &models.MembershipRole{MembershipID: membership.ID, RoleID: roleID}
		if err := db.Create(mr).Error; err != nil {
			t.Fatalf("create membership role: %v", err)
		}
	}

	return membership
}

func TestOrganizationPermissionKeysUnionsEveryRole(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewAuthz(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)

	readers := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	editors := newRole(t, db, org.ID, "editors", string(authz.PermRolesRead), string(authz.PermRolesUpdate))

	newMembership(t, db, org.ID, u.ID, models.MembershipActive, readers.ID, editors.ID)

	keys, err := repo.OrganizationPermissionKeys(t.Context(), u.ID, org.ID)
	if err != nil {
		t.Fatalf("OrganizationPermissionKeys() = _, %v", err)
	}

	slices.Sort(keys)

	want := []string{
		string(authz.PermMembersRead),
		string(authz.PermRolesRead),
		string(authz.PermRolesUpdate),
	}
	slices.Sort(want)

	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

// TestAMemberWithNoRolesIsDistinctFromAStranger is the reason the query uses
// LEFT JOINs. An inner join returns nothing in both cases, and the API has to
// answer 403 for the first and 404 for the second.
func TestAMemberWithNoRolesIsDistinctFromAStranger(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewAuthz(db)
	users := repositories.NewUser(db)

	org := newOrganization(t, db)

	member := newUser(t, users)
	newMembership(t, db, org.ID, member.ID, models.MembershipActive)

	keys, err := repo.OrganizationPermissionKeys(t.Context(), member.ID, org.ID)
	if err != nil {
		t.Fatalf("OrganizationPermissionKeys() for a member with no roles = _, %v, want nil", err)
	}

	if len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}

	stranger := newUser(t, users)

	if _, err := repo.OrganizationPermissionKeys(t.Context(), stranger.ID, org.ID); !errors.Is(err, authz.ErrNotMember) {
		t.Errorf("OrganizationPermissionKeys() for a stranger = %v, want ErrNotMember", err)
	}
}

func TestOnlyActiveMembershipsResolve(t *testing.T) {
	for _, status := range []models.MembershipStatus{models.MembershipSuspended} {
		t.Run(string(status), func(t *testing.T) {
			db := testDB(t)
			repo := repositories.NewAuthz(db)

			u := newUser(t, repositories.NewUser(db))
			org := newOrganization(t, db)
			role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))

			newMembership(t, db, org.ID, u.ID, status, role.ID)

			_, err := repo.OrganizationPermissionKeys(t.Context(), u.ID, org.ID)
			if !errors.Is(err, authz.ErrNotMember) {
				t.Errorf("OrganizationPermissionKeys() for %s = %v, want ErrNotMember", status, err)
			}
		})
	}
}

// TestSoftDeletesStopGranting covers the filters GORM does not add for us. This
// query is built from a table name, so its soft-delete scope never applies and
// the conditions have to be written out; drop either one and a deleted account
// or a deleted organization keeps working.
func TestSoftDeletesStopGranting(t *testing.T) {
	t.Run("deleted organization", func(t *testing.T) {
		db := testDB(t)
		repo := repositories.NewAuthz(db)

		u := newUser(t, repositories.NewUser(db))
		org := newOrganization(t, db)
		role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
		newMembership(t, db, org.ID, u.ID, models.MembershipActive, role.ID)

		if err := db.Delete(org).Error; err != nil {
			t.Fatalf("delete organization: %v", err)
		}

		if _, err := repo.OrganizationPermissionKeys(t.Context(), u.ID, org.ID); !errors.Is(err, authz.ErrNotMember) {
			t.Errorf("OrganizationPermissionKeys() = %v, want ErrNotMember", err)
		}
	})

	t.Run("deleted account", func(t *testing.T) {
		db := testDB(t)
		repo := repositories.NewAuthz(db)

		u := newUser(t, repositories.NewUser(db))
		org := newOrganization(t, db)
		role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
		newMembership(t, db, org.ID, u.ID, models.MembershipActive, role.ID)

		if err := db.Delete(u).Error; err != nil {
			t.Fatalf("delete user: %v", err)
		}

		if _, err := repo.OrganizationPermissionKeys(t.Context(), u.ID, org.ID); !errors.Is(err, authz.ErrNotMember) {
			t.Errorf("OrganizationPermissionKeys() = %v, want ErrNotMember", err)
		}
	})
}

// TestKeysAreReturnedRaw pins the layering. The store does not know which
// permissions this build defines; dropping the ones it does not is the
// catalog's job, and doing it here would put a second copy of the list in SQL.
func TestKeysAreReturnedRaw(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewAuthz(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, "legacy", "organization.teleport")

	newMembership(t, db, org.ID, u.ID, models.MembershipActive, role.ID)

	keys, err := repo.OrganizationPermissionKeys(t.Context(), u.ID, org.ID)
	if err != nil {
		t.Fatalf("OrganizationPermissionKeys() = _, %v", err)
	}

	if !slices.Contains(keys, "organization.teleport") {
		t.Errorf("keys = %v, want the stale key returned for the catalog to drop", keys)
	}

	// And the catalog is what actually drops it.
	if got := authz.Sanitize(keys); len(got) != 0 {
		t.Errorf("Sanitize(%v) = %v, want nothing", keys, got)
	}
}

// TestDeletingARoleCascadesItsAssignments proves the foreign key does what the
// fake does in Go: a dangling assignment cannot survive to grant anything.
func TestDeletingARoleCascadesItsAssignments(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewAuthz(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	kept := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	gone := newRole(t, db, org.ID, "editors", string(authz.PermRolesUpdate))

	newMembership(t, db, org.ID, u.ID, models.MembershipActive, kept.ID, gone.ID)

	if err := db.Delete(gone).Error; err != nil {
		t.Fatalf("delete role: %v", err)
	}

	keys, err := repo.OrganizationPermissionKeys(t.Context(), u.ID, org.ID)
	if err != nil {
		t.Fatalf("OrganizationPermissionKeys() = _, %v", err)
	}

	if !slices.Equal(keys, []string{string(authz.PermMembersRead)}) {
		t.Errorf("keys = %v, want only the surviving role's permission", keys)
	}
}

// TestSystemRolesAreScopedToTheUser is the cross-account check: one person's
// platform role must never resolve for another.
func TestSystemRolesAreScopedToTheUser(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewAuthz(db)
	users := repositories.NewUser(db)

	admin := newUser(t, users)
	other := newUser(t, users)

	grant := &models.UserSystemRole{UserID: admin.ID, RoleKey: string(authz.RolePlatformAdmin)}
	if err := db.Create(grant).Error; err != nil {
		t.Fatalf("create system role: %v", err)
	}

	keys, err := repo.SystemRoleKeys(t.Context(), admin.ID)
	if err != nil {
		t.Fatalf("SystemRoleKeys() = _, %v", err)
	}

	if !slices.Equal(keys, []string{string(authz.RolePlatformAdmin)}) {
		t.Errorf("keys = %v, want the granted role", keys)
	}

	keys, err = repo.SystemRoleKeys(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("SystemRoleKeys() = _, %v", err)
	}

	if len(keys) != 0 {
		t.Errorf("keys = %v for an account that was granted nothing, want none", keys)
	}
}

// TestARepeatedRoleAssignmentIsRefusedByTheIndex is what lets the API treat
// assigning a role twice as idempotent rather than as a duplicate grant that
// revoking once would leave behind.
func TestARepeatedRoleAssignmentIsRefusedByTheIndex(t *testing.T) {
	db := testDB(t)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive, role.ID)

	dup := &models.MembershipRole{MembershipID: membership.ID, RoleID: role.ID}
	if err := db.Create(dup).Error; err == nil {
		t.Error("assigning the same role twice succeeded; idx_membership_role is not unique")
	}
}

// TestADeletedAccountHoldsNoSystemRole is the filter the query carries and nothing else
// enforces.
//
// A platform administrator's account being deleted has to end their platform powers. The
// grant row survives — a soft delete fires no cascade — so if the query stopped joining
// to live accounts, a deleted administrator would keep answering yes to every
// platform.* check, and the only visible symptom would be somebody who should be gone
// still getting in.
func TestADeletedAccountHoldsNoSystemRole(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewAuthz(db)
	users := repositories.NewUser(db)

	admin := newUser(t, users)

	grant := &models.UserSystemRole{UserID: admin.ID, RoleKey: string(authz.RolePlatformAdmin)}
	if err := db.Create(grant).Error; err != nil {
		t.Fatalf("create system role: %v", err)
	}

	keys, err := repo.SystemRoleKeys(t.Context(), admin.ID)
	if err != nil {
		t.Fatalf("SystemRoleKeys() = _, %v", err)
	}

	if len(keys) != 1 {
		t.Fatalf("keys = %v before the deletion, want the granted role", keys)
	}

	if err := users.Delete(t.Context(), admin.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}

	keys, err = repo.SystemRoleKeys(t.Context(), admin.ID)
	if err != nil {
		t.Fatalf("SystemRoleKeys() after the deletion = _, %v", err)
	}

	if len(keys) != 0 {
		t.Errorf("a deleted account still holds %v", keys)
	}
}
