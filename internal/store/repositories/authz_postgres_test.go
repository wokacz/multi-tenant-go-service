package repositories_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/membership"
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
//
// The insert goes through ent, not the repository: CreateOrganization also
// materialises the shipped roles, and several cases below need an empty tenant
// so they can plant a role the catalog does not define.
func newOrganization(t *testing.T, db *store.DB) *models.Organization {
	t.Helper()

	row, err := db.Ent().Organization.Create().
		SetSlug("org-" + uuid.Must(uuid.NewV7()).String()).
		SetName("Test organization").
		Save(t.Context())
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}

	org := &models.Organization{Slug: row.Slug, Name: row.Name}
	org.ID = row.ID
	org.CreatedAt = row.CreatedAt
	org.UpdatedAt = row.UpdatedAt
	org.IsProtected = row.IsProtected
	org.DeletedAt = row.DeletedAt

	return org
}

// newRole inserts a role and its permissions. The keys are written as given so a
// test can store one the catalog does not define.
func newRole(t *testing.T, db *store.DB, orgID uuid.UUID, key string, permissions ...string) *models.Role {
	t.Helper()

	row, err := db.Ent().Role.Create().
		SetOrganizationID(orgID).
		SetKey(key).
		SetName(key).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	for _, perm := range permissions {
		if _, err := db.Ent().RolePermission.Create().
			SetRoleID(row.ID).
			SetPermissionKey(perm).
			Save(t.Context()); err != nil {
			t.Fatalf("create role permission: %v", err)
		}
	}

	role := &models.Role{OrganizationID: orgID, Key: key, Name: key}
	role.ID = row.ID
	role.CreatedAt = row.CreatedAt
	role.UpdatedAt = row.UpdatedAt
	role.IsSystem = row.IsSystem

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

	row, err := db.Ent().Membership.Create().
		SetOrganizationID(orgID).
		SetUserID(userID).
		SetStatus(membership.Status(status)).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create membership: %v", err)
	}

	for _, roleID := range roleIDs {
		if _, err := db.Ent().MembershipRole.Create().
			SetMembershipID(row.ID).
			SetRoleID(roleID).
			Save(t.Context()); err != nil {
			t.Fatalf("create membership role: %v", err)
		}
	}

	out := &models.Membership{
		OrganizationID: orgID,
		UserID:         userID,
		Status:         status,
	}
	out.ID = row.ID
	out.CreatedAt = row.CreatedAt
	out.UpdatedAt = row.UpdatedAt

	return out
}

// retireAccount soft-deletes the user the way the repository does, without going
// through User.Delete — these cases need the membership row to survive, which a
// cascade would not leave behind, and they are not testing device revocation.
func retireAccount(t *testing.T, db *store.DB, id uuid.UUID) {
	t.Helper()

	if err := db.Ent().User.DeleteOneID(id).Exec(t.Context()); err != nil {
		t.Fatalf("delete user: %v", err)
	}
}

func grantSystemRole(t *testing.T, db *store.DB, userID uuid.UUID, key string) {
	t.Helper()

	if _, err := db.Ent().UserSystemRole.Create().
		SetUserID(userID).
		SetRoleKey(key).
		Save(t.Context()); err != nil {
		t.Fatalf("create system role: %v", err)
	}
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

// TestSoftDeletesStopGranting covers the filters a query built from table names
// has to write itself. Drop either one and a deleted account or a deleted
// organization keeps working.
func TestSoftDeletesStopGranting(t *testing.T) {
	t.Run("deleted organization", func(t *testing.T) {
		db := testDB(t)
		repo := repositories.NewAuthz(db)

		u := newUser(t, repositories.NewUser(db))
		org := newOrganization(t, db)
		role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
		newMembership(t, db, org.ID, u.ID, models.MembershipActive, role.ID)

		if err := db.Ent().Organization.DeleteOneID(org.ID).Exec(t.Context()); err != nil {
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

		retireAccount(t, db, u.ID)

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

	if err := db.Ent().Role.DeleteOneID(gone.ID).Exec(t.Context()); err != nil {
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

	grantSystemRole(t, db, admin.ID, string(authz.RolePlatformAdmin))

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

	_, err := db.Ent().MembershipRole.Create().
		SetMembershipID(membership.ID).
		SetRoleID(role.ID).
		Save(t.Context())
	if err == nil {
		t.Error("assigning the same role twice succeeded; idx_membership_role is not unique")
	} else if !ent.IsConstraintError(err) {
		t.Errorf("second assignment = %v, want a unique constraint violation", err)
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

	grantSystemRole(t, db, admin.ID, string(authz.RolePlatformAdmin))

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
