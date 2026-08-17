package repositories_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
)

// wholePage is the limit a case passes when the page is not what it is testing.
// The listings are paged, and the repository does not clamp a non-positive limit
// into "everything" — see orgs.Repository — so a case that just wants the rows has
// to name a number.
const wholePage = 1000

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

	err := repo.ReplaceMemberRoles(t.Context(), org.ID, membership.ID, []uuid.UUID{mine.ID, theirs.ID}, orgs.RefuseLastOwnerLoss(true))
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

	if err := repo.ReplaceMemberRoles(t.Context(), org.ID, membership.ID, []uuid.UUID{second.ID}, orgs.RefuseLastOwnerLoss(true)); err != nil {
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

// TestTheOwnerStateTheGuardSeesCountsOnlyRealOwners is what the last-owner rule
// leans on. Counting a suspended owner, or one whose account is gone, would let
// the last usable one be removed.
//
// It reads the state through a guard rather than through a counting method,
// because that is the only place the number exists now: it is assembled inside
// the transaction, with the organization row locked, and handed to the domain.
// Asserting on it here is stronger than the method this replaces — it is the
// value the rule actually decides from, not a second query that resembles it.
func TestTheOwnerStateTheGuardSeesCountsOnlyRealOwners(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	org := newOrganization(t, db)
	owner := newRole(t, db, org.ID, string(authz.RoleOwner), string(authz.PermOrganizationDelete))
	viewer := newRole(t, db, org.ID, string(authz.RoleViewer), string(authz.PermOrganizationRead))

	active := newUser(t, users)
	activeMembership := newMembership(t, db, org.ID, active.ID, models.MembershipActive, owner.ID)

	suspended := newUser(t, users)
	newMembership(t, db, org.ID, suspended.ID, models.MembershipSuspended, owner.ID)

	plain := newUser(t, users)
	plainMembership := newMembership(t, db, org.ID, plain.ID, models.MembershipActive, viewer.ID)

	// A deleted account is not an owner either, and a soft delete never fires the
	// foreign key cascade, so the membership row is still there.
	deleted := newUser(t, users)
	newMembership(t, db, org.ID, deleted.ID, models.MembershipActive, owner.ID)

	if err := db.Delete(deleted).Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}

	// errStop abandons the transaction once the state has been captured, so the
	// probe reads without changing anything.
	errStop := errors.New("stop")

	capture := func(memberID uuid.UUID) orgs.OwnerState {
		t.Helper()

		var seen orgs.OwnerState

		err := repo.RemoveMember(t.Context(), org.ID, memberID, func(state orgs.OwnerState) error {
			seen = state

			return errStop
		})
		if !errors.Is(err, errStop) {
			t.Fatalf("RemoveMember() = %v, want the guard's own error back", err)
		}

		return seen
	}

	if got := capture(activeMembership.ID); got.Owners != 1 || !got.SubjectHoldsOwner {
		t.Errorf("state for the one real owner = %+v, want {Owners:1 SubjectHoldsOwner:true}", got)
	}

	if got := capture(plainMembership.ID); got.Owners != 1 || got.SubjectHoldsOwner {
		t.Errorf("state for a viewer = %+v, want {Owners:1 SubjectHoldsOwner:false}", got)
	}
}

// TestTheRoleGuardCountsHoldersInsideTheTransaction is the M8 half of moving the
// rules into the domain.
//
// The service used to read Role().Members on one connection and call DeleteRole on
// another, so a role assigned in between kept the caller's "nobody holds this"
// answer and lost the assignment to the cascade. The count now happens inside the
// delete's own transaction, after the organization row is locked — the same lock
// ReplaceMemberRoles takes, so the two cannot interleave.
//
// The ordering itself is a property of the lock rather than something a test can
// observe without a deliberate interleaving, which would be flaky. What is checked
// here is that the number the domain decides from is assembled in there at all,
// and that it is the right number.
func TestTheRoleGuardCountsHoldersInsideTheTransaction(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, "auditor", string(authz.PermMembersRead))

	first := newUser(t, users)
	newMembership(t, db, org.ID, first.ID, models.MembershipActive, role.ID)

	second := newUser(t, users)
	newMembership(t, db, org.ID, second.ID, models.MembershipActive, role.ID)

	var seen int

	errStop := errors.New("stop")

	err := repo.DeleteRole(t.Context(), org.ID, role.ID, func(holders int) error {
		seen = holders

		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("DeleteRole() = %v, want the guard's own error back", err)
	}

	if seen != 2 {
		t.Errorf("holders = %d, want 2", seen)
	}

	// The guard refusing means the role is still there: the count and the delete
	// share one transaction, so abandoning it takes the delete with it.
	if _, err := repo.Role(t.Context(), org.ID, role.ID); err != nil {
		t.Errorf("Role() after a refused delete = %v, want it still there", err)
	}

	// And the real rule agrees about this state.
	if err := repo.DeleteRole(t.Context(), org.ID, role.ID, orgs.RefuseRoleInUse()); !errors.Is(err, orgs.ErrRoleInUse) {
		t.Errorf("DeleteRole() with the real rule = %v, want ErrRoleInUse", err)
	}
}

// TestADeletedAccountIsNotAMember pins the one rule every membership lookup has
// to agree on: a row whose account is gone is not a member.
//
// It is a Postgres test because this is where the rule was broken and the fake
// could not show it. The join had to be a left one while an invitation was a
// membership with no account, and a condition in a LEFT JOIN does not remove rows
// — so "AND u.deleted_at IS NULL" filtered nothing and a deleted account stayed on
// the list with an empty name. The join is inner now, which is what makes the rule
// the join itself.
func TestADeletedAccountIsNotAMember(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, string(authz.RoleViewer), string(authz.PermOrganizationRead))

	live := newUser(t, users)
	liveMembership := newMembership(t, db, org.ID, live.ID, models.MembershipActive, role.ID)

	gone := newUser(t, users)
	goneMembership := newMembership(t, db, org.ID, gone.ID, models.MembershipActive, role.ID)

	// A soft delete never fires the foreign key cascade, so the membership row
	// is still there afterwards.
	if err := db.Delete(gone).Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}

	members, err := repo.Members(t.Context(), org.ID, wholePage, 0)
	if err != nil {
		t.Fatalf("Members() = _, %v", err)
	}

	listed := make([]string, 0, len(members))
	for i := range members {
		listed = append(listed, members[i].ID.String())
	}

	slices.Sort(listed)

	want := []string{liveMembership.ID.String()}

	if !slices.Equal(listed, want) {
		t.Errorf("Members() listed %v, want only the live account (%v) — "+
			"the deleted account's membership is %v", listed, want, goneMembership.ID)
	}

	if _, err := repo.Member(t.Context(), org.ID, goneMembership.ID); !errors.Is(err, orgs.ErrNotFound) {
		t.Errorf("Member() for a deleted account = %v, want ErrNotFound", err)
	}

	if _, err := repo.MemberByUser(t.Context(), org.ID, gone.ID); !errors.Is(err, orgs.ErrNotFound) {
		t.Errorf("MemberByUser() for a deleted account = %v, want ErrNotFound", err)
	}

	// The live account is still reachable, so the join has not taken out more than
	// it should.
	if _, err := repo.Member(t.Context(), org.ID, liveMembership.ID); err != nil {
		t.Errorf("Member() for a live account = %v, want it found", err)
	}
}

// TestAnOwnerWhoseAccountIsDeletedDoesNotBlockRemoval is the SQL half of the
// last-owner fix.
//
// The check that refuses the change and the count that can overrule it have to
// use the same rule. The first joined users with a LEFT JOIN, where the
// deleted_at condition filtered nothing, so a membership whose account had been
// deleted counted as holding owner there and was invisible to the count. The row
// could not be removed however many live owners the organization had.
func TestAnOwnerWhoseAccountIsDeletedDoesNotBlockRemoval(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	org := newOrganization(t, db)
	owner := newRole(t, db, org.ID, string(authz.RoleOwner), string(authz.PermOrganizationDelete))

	live := newUser(t, users)
	liveMembership := newMembership(t, db, org.ID, live.ID, models.MembershipActive, owner.ID)

	gone := newUser(t, users)
	goneMembership := newMembership(t, db, org.ID, gone.ID, models.MembershipActive, owner.ID)

	if err := db.Delete(gone).Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if err := repo.RemoveMember(t.Context(), org.ID, goneMembership.ID, orgs.RefuseLastOwnerLoss(true)); err != nil {
		t.Fatalf("RemoveMember() for an owner whose account is deleted = %v, want it removed", err)
	}

	// The rule still holds for the owner who is actually there.
	err := repo.RemoveMember(t.Context(), org.ID, liveMembership.ID, orgs.RefuseLastOwnerLoss(true))
	if !errors.Is(err, orgs.ErrLastOwner) {
		t.Errorf("RemoveMember() for the last live owner = %v, want ErrLastOwner", err)
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

	if err := repo.DeleteRole(t.Context(), org.ID, role.ID, orgs.RefuseRoleInUse()); !errors.Is(err, models.ErrRoleIsSystem) {
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
		models.MembershipSuspended, time.Now().UTC(), orgs.RefuseLastOwnerLoss(true)); err != nil {
		t.Fatalf("SetMemberStatus(suspended) = %v", err)
	}

	if err := repo.SetMemberStatus(t.Context(), org.ID, member.ID,
		models.MembershipActive, time.Now().UTC(), orgs.RefuseLastOwnerLoss(false)); err != nil {
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

	if err := repo.RemoveMember(t.Context(), org.ID, membership.ID, orgs.RefuseLastOwnerLoss(true)); err != nil {
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

// TestInviteMemberDoesNotLookTheAddressUp is the storage half of closing the
// registration oracle: an unknown email is stored as an outstanding invitation
// rather than refused.
func TestInviteMemberStoresAnUnknownAddress(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	org := newOrganization(t, db)

	now := time.Now().UTC()

	// A token unique to this test: token_hash carries an installation-wide unique
	// index, on purpose — a token has to identify exactly one invitation — so two
	// tests sharing a literal collide in a database they also share.
	token := "unknown-address-" + uuid.Must(uuid.NewV7()).String()

	invitation, err := repo.InviteMember(t.Context(), org.ID, "nobody@example.com",
		orgs.HashInvitationToken(token), nil, uuid.Nil, now.Add(orgs.InvitationTTL), now)
	if err != nil {
		t.Fatalf("InviteMember() = _, %v", err)
	}

	if invitation.Email != "nobody@example.com" {
		t.Errorf("email = %q", invitation.Email)
	}

	// No membership was created: an invitation is an offer, and the address it was
	// sent to is not evidence that anybody holds it.
	members, err := repo.Members(t.Context(), org.ID, wholePage, 0)
	if err != nil {
		t.Fatalf("Members() = _, %v", err)
	}

	if len(members) != 0 {
		t.Errorf("Members() = %+v, want none — inviting is not joining", members)
	}

	// And it is reachable by the token that was mailed, not by the address.
	found, err := repo.InvitationByToken(t.Context(), orgs.HashInvitationToken(token), now)
	if err != nil {
		t.Fatalf("InvitationByToken() = _, %v", err)
	}

	if found.ID != invitation.ID {
		t.Errorf("InvitationByToken() found %v, want %v", found.ID, invitation.ID)
	}
}

// TestConcurrentDemotionsLeaveOneOwner is the TOCTOU the last-owner rule used
// to have: two owners, two overlapping demotions, both observing owners > 1.
// The organization row is locked inside the same transaction as the write, so
// one of them must be refused and exactly one owner remains.
func TestConcurrentDemotionsLeaveOneOwner(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	a := newUser(t, users)
	b := newUser(t, users)
	org := newOrganization(t, db)

	owner := newRole(t, db, org.ID, string(authz.RoleOwner), string(authz.PermOrganizationDelete))
	member := newRole(t, db, org.ID, string(authz.RoleMember), string(authz.PermMembersRead))

	first := newMembership(t, db, org.ID, a.ID, models.MembershipActive, owner.ID)
	second := newMembership(t, db, org.ID, b.ID, models.MembershipActive, owner.ID)

	errs := make(chan error, 2)
	go func() {
		errs <- repo.ReplaceMemberRoles(t.Context(), org.ID, first.ID, []uuid.UUID{member.ID}, orgs.RefuseLastOwnerLoss(true))
	}()
	go func() {
		errs <- repo.ReplaceMemberRoles(t.Context(), org.ID, second.ID, []uuid.UUID{member.ID}, orgs.RefuseLastOwnerLoss(true))
	}()

	firstErr, secondErr := <-errs, <-errs
	switch {
	case firstErr == nil && errors.Is(secondErr, orgs.ErrLastOwner),
		secondErr == nil && errors.Is(firstErr, orgs.ErrLastOwner):
	default:
		t.Fatalf("concurrent demotions = %v, %v, want one success and one ErrLastOwner",
			firstErr, secondErr)
	}

	// One owner has to be left. Read through a guard, which is where the count
	// lives now: it is assembled inside the transaction rather than by a method
	// anyone can call.
	errStop := errors.New("stop")

	var seen orgs.OwnerState

	err := repo.RemoveMember(t.Context(), org.ID, first.ID, func(state orgs.OwnerState) error {
		seen = state

		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("RemoveMember() = %v, want the guard's own error back", err)
	}

	if seen.Owners != 1 {
		t.Errorf("owners = %d, want 1 — overlapping demotions left the organization without an owner", seen.Owners)
	}
}
