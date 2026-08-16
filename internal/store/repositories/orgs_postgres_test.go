package repositories_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/domain/authz"
	"github.com/wokacz/go-example/internal/domain/orgs"
	"github.com/wokacz/go-example/internal/store/models"
	"github.com/wokacz/go-example/internal/store/repositories"
)

// These cover what the in-memory fake reimplements in Go: the transactional
// replaces, the filtered count that refuses another organization's role id, the
// owner count's join, and the cascades.
//
//	POSTGRES_TEST=1 go test ./internal/store/repositories -v

// TestReplaceMemberRolesRefusesAnotherOrganizationsRole is the resource-scope
// check at the SQL level.
//
// A foreign key would not catch this: it only says the role exists somewhere.
// The repository counts how many of the ids belong to *this* organization and
// refuses unless all of them do.
func TestReplaceMemberRolesRefusesAnotherOrganizationsRole(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	foreign := newOrganization(t, db)

	mine := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	theirs := newRole(t, db, foreign.ID, "readers", string(authz.PermMembersRead))

	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive)

	err := repo.ReplaceMemberRoles(t.Context(), org.ID, membership.ID, []uuid.UUID{mine.ID, theirs.ID})
	if !errors.Is(err, orgs.ErrNotFound) {
		t.Fatalf("ReplaceMemberRoles() with a foreign role = %v, want ErrNotFound", err)
	}

	// And the transaction rolled back: the legitimate half must not have landed
	// either, or a partial assignment survives a refused request.
	member, err := repo.Member(t.Context(), org.ID, membership.ID)
	if err != nil {
		t.Fatalf("Member() = _, %v", err)
	}

	if len(member.Roles) != 0 {
		t.Errorf("roles = %+v, want none — the refused call left half its work behind", member.Roles)
	}
}

// TestReplaceMemberRolesIsAtomic proves the delete and the insert share one
// transaction. A crash between them would leave somebody with no roles at all.
func TestReplaceMemberRolesIsAtomic(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)

	first := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	second := newRole(t, db, org.ID, "editors", string(authz.PermRolesUpdate))

	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive, first.ID)

	if err := repo.ReplaceMemberRoles(t.Context(), org.ID, membership.ID, []uuid.UUID{second.ID}); err != nil {
		t.Fatalf("ReplaceMemberRoles() = %v", err)
	}

	member, err := repo.Member(t.Context(), org.ID, membership.ID)
	if err != nil {
		t.Fatalf("Member() = _, %v", err)
	}

	if len(member.Roles) != 1 || member.Roles[0].ID != second.ID {
		t.Errorf("roles = %+v, want exactly the replacement", member.Roles)
	}
}

// TestMemberIsScopedToTheOrganization is the query-level guarantee the whole
// design leans on: a membership id from another tenant simply does not resolve.
func TestMemberIsScopedToTheOrganization(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	other := newOrganization(t, db)

	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive)

	if _, err := repo.Member(t.Context(), other.ID, membership.ID); !errors.Is(err, orgs.ErrNotFound) {
		t.Fatalf("Member() across organizations = %v, want ErrNotFound", err)
	}
}

// TestOwnerCountOnlyCountsActiveOwners is what the last-owner rule leans on.
// Counting a suspended owner would let the last usable one be removed.
func TestOwnerCountOnlyCountsActiveOwners(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	org := newOrganization(t, db)
	owner := newRole(t, db, org.ID, string(authz.RoleOwner), string(authz.PermOrganizationDelete))
	viewer := newRole(t, db, org.ID, string(authz.RoleViewer), string(authz.PermOrganizationRead))

	active := newUser(t, users)
	newMembership(t, db, org.ID, active.ID, models.MembershipActive, owner.ID)

	suspended := newUser(t, users)
	newMembership(t, db, org.ID, suspended.ID, models.MembershipSuspended, owner.ID)

	plain := newUser(t, users)
	newMembership(t, db, org.ID, plain.ID, models.MembershipActive, viewer.ID)

	got, err := repo.OwnerCount(t.Context(), org.ID)
	if err != nil {
		t.Fatalf("OwnerCount() = _, %v", err)
	}

	if got != 1 {
		t.Errorf("OwnerCount() = %d, want 1 — only the active owner counts", got)
	}

	// A deleted account is not an owner either, and a soft delete never fires
	// the foreign key cascade, so the membership row is still there.
	deleted := newUser(t, users)
	newMembership(t, db, org.ID, deleted.ID, models.MembershipActive, owner.ID)

	if err := db.Delete(deleted).Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}

	got, err = repo.OwnerCount(t.Context(), org.ID)
	if err != nil {
		t.Fatalf("OwnerCount() = _, %v", err)
	}

	if got != 1 {
		t.Errorf("OwnerCount() = %d after deleting an owner's account, want 1", got)
	}
}

// TestCreateRoleIsAtomic proves the role and its permissions land together.
func TestCreateRoleIsAtomic(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	org := newOrganization(t, db)

	role, err := repo.CreateRole(t.Context(), org.ID,
		&models.Role{Key: "auditor", Name: "Auditor"},
		[]authz.Permission{authz.PermMembersRead, authz.PermRolesRead},
	)
	if err != nil {
		t.Fatalf("CreateRole() = _, %v", err)
	}

	keys := make([]string, 0, len(role.Permissions))
	for _, perm := range role.Permissions {
		keys = append(keys, string(perm))
	}

	slices.Sort(keys)

	want := []string{string(authz.PermMembersRead), string(authz.PermRolesRead)}
	slices.Sort(want)

	if !slices.Equal(keys, want) {
		t.Errorf("permissions = %v, want %v", keys, want)
	}
}

// TestARoleKeyIsUniquePerOrganization pins the index the settings screen relies
// on, and its scope: two organizations may each have their own "auditor".
func TestARoleKeyIsUniquePerOrganization(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	org := newOrganization(t, db)
	other := newOrganization(t, db)

	if _, err := repo.CreateRole(t.Context(), org.ID,
		&models.Role{Key: "auditor", Name: "Auditor"}, nil); err != nil {
		t.Fatalf("CreateRole() = _, %v", err)
	}

	_, err := repo.CreateRole(t.Context(), org.ID, &models.Role{Key: "auditor", Name: "Again"}, nil)
	if !errors.Is(err, orgs.ErrRoleKeyTaken) {
		t.Errorf("CreateRole() with a duplicate key = %v, want ErrRoleKeyTaken", err)
	}

	if _, err := repo.CreateRole(t.Context(), other.ID,
		&models.Role{Key: "auditor", Name: "Auditor"}, nil); err != nil {
		t.Errorf("CreateRole() in another organization = %v, want nil", err)
	}
}

// TestDeletingARoleIsRefusedForSystemRoles covers the model hook reaching the
// database path. The repository loads the row first precisely so BeforeDelete
// sees IsSystem; a bare Where(...).Delete() would hand it a zero receiver.
func TestDeletingARoleIsRefusedForSystemRoles(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	org := newOrganization(t, db)

	role := &models.Role{OrganizationID: org.ID, Key: "admin", Name: "Administrator", IsSystem: true}
	if err := db.Create(role).Error; err != nil {
		t.Fatalf("create role: %v", err)
	}

	if err := repo.DeleteRole(t.Context(), org.ID, role.ID); !errors.Is(err, models.ErrRoleIsSystem) {
		t.Errorf("DeleteRole() on a system role = %v, want ErrRoleIsSystem", err)
	}
}

// TestDeletingAnOrganizationIsRefusedWhenProtected is the same trap on the
// organization, and covers the default organization staying undeletable.
func TestDeletingAnOrganizationIsRefusedWhenProtected(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	org := newOrganization(t, db)
	if err := db.Model(org).Update("is_protected", true).Error; err != nil {
		t.Fatalf("protect organization: %v", err)
	}

	if err := repo.DeleteOrganization(t.Context(), org.ID); !errors.Is(err, models.ErrProtected) {
		t.Errorf("DeleteOrganization() on a protected organization = %v, want ErrProtected", err)
	}
}

// TestAddMemberRefusesADuplicate relies on the unique index rather than a
// lookup first: two concurrent adds would both pass a check and one would still
// fail on insert.
func TestAddMemberRefusesADuplicate(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)

	if _, err := repo.AddMember(t.Context(), org.ID, u.ID, nil, uuid.Nil, time.Now().UTC()); err != nil {
		t.Fatalf("AddMember() = _, %v", err)
	}

	_, err := repo.AddMember(t.Context(), org.ID, u.ID, nil, uuid.Nil, time.Now().UTC())
	if !errors.Is(err, orgs.ErrAlreadyMember) {
		t.Errorf("AddMember() twice = %v, want ErrAlreadyMember", err)
	}
}

// TestReinstatingKeepsTheOriginalJoinDate covers the COALESCE. Overwriting it
// would turn "joined three years ago" into "joined on Tuesday" every time
// somebody was suspended and brought back.
func TestReinstatingKeepsTheOriginalJoinDate(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)

	joined := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Millisecond)

	member, err := repo.AddMember(t.Context(), org.ID, u.ID, nil, uuid.Nil, joined)
	if err != nil {
		t.Fatalf("AddMember() = _, %v", err)
	}

	if err := repo.SetMemberStatus(t.Context(), org.ID, member.ID,
		models.MembershipSuspended, time.Now().UTC()); err != nil {
		t.Fatalf("SetMemberStatus(suspended) = %v", err)
	}

	if err := repo.SetMemberStatus(t.Context(), org.ID, member.ID,
		models.MembershipActive, time.Now().UTC()); err != nil {
		t.Fatalf("SetMemberStatus(active) = %v", err)
	}

	reloaded, err := repo.Member(t.Context(), org.ID, member.ID)
	if err != nil {
		t.Fatalf("Member() = _, %v", err)
	}

	if reloaded.JoinedAt == nil || !reloaded.JoinedAt.Truncate(time.Millisecond).Equal(joined) {
		t.Errorf("JoinedAt = %v, want the original %v", reloaded.JoinedAt, joined)
	}
}

// TestRemovingAMemberCascadesTheirRoleAssignments proves the foreign key does
// what the fake does in Go.
func TestRemovingAMemberCascadesTheirRoleAssignments(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))

	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive, role.ID)

	if err := repo.RemoveMember(t.Context(), org.ID, membership.ID); err != nil {
		t.Fatalf("RemoveMember() = %v", err)
	}

	var remaining int64
	if err := db.Model(&models.MembershipRole{}).
		Where("membership_id = ?", membership.ID).
		Count(&remaining).Error; err != nil {
		t.Fatalf("count assignments: %v", err)
	}

	if remaining != 0 {
		t.Errorf("assignments left behind = %d, want 0", remaining)
	}
}
