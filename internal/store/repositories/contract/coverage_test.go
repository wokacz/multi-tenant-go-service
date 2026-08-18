package contract_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// The cases in this file were written for the migration off GORM (see ENT.md).
//
// Nineteen exported repository methods had no store-level test at all — not here and
// not in the Postgres suites. They were reached only through fixtures, or through the
// API tests, which run against the fake and prove nothing about the SQL. Rewriting
// them against a different query builder with that as the safety net would be a
// rewrite nobody could review honestly.
//
// Every case states a rule the code already has a reason for, taken from the comment
// on the method rather than invented here.

// --- the organization row ---------------------------------------------------

// TestADeletedOrganizationIsNotFound is the soft-delete rule at its simplest, and the
// one an interceptor-based implementation is most likely to get subtly wrong.
func TestADeletedOrganizationIsNotFound(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		if _, err := b.repo.Organization(t.Context(), orgID); err != nil {
			t.Fatalf("Organization() for a live one = %v", err)
		}

		b.deleteOrg(t, orgID)

		if _, err := b.repo.Organization(t.Context(), orgID); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("Organization() for a deleted one = %v, want ErrNotFound", err)
		}
	})
}

// TestRenamingSaysWhenThereWasNothingToRename covers the RowsAffected check. Without
// it an update against a missing id reports success, and the caller returns a 200 for
// a row that does not exist.
func TestRenamingSaysWhenThereWasNothingToRename(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		if err := b.repo.UpdateOrganization(t.Context(), orgID, "Renamed"); err != nil {
			t.Fatalf("UpdateOrganization() = %v", err)
		}

		org, err := b.repo.Organization(t.Context(), orgID)
		if err != nil {
			t.Fatalf("Organization() = %v", err)
		}

		if org.Name != "Renamed" {
			t.Errorf("name = %q, want Renamed", org.Name)
		}

		err = b.repo.UpdateOrganization(t.Context(), uuid.Must(uuid.NewV7()), "Nobody")
		if !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("UpdateOrganization() on an unknown id = %v, want ErrNotFound", err)
		}
	})
}

// TestTheDefaultOrganizationRefusesDeletion is the protection an installation depends
// on: one that lost its only organization has no working accounts and no screen to
// undo it from.
func TestTheDefaultOrganizationRefusesDeletion(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newProtectedOrg(t)

		err := b.repo.DeleteOrganization(t.Context(), orgID)
		if !errors.Is(err, models.ErrProtected) {
			t.Errorf("DeleteOrganization() on a protected organization = %v, want ErrProtected", err)
		}

		if _, err := b.repo.Organization(t.Context(), orgID); err != nil {
			t.Errorf("the protected organization is gone anyway: %v", err)
		}
	})
}

// --- roles ------------------------------------------------------------------

// TestARoleIsFoundByKeyOnlyInsideItsOrganization is the scoping rule. Role keys repeat
// across tenants by design — every organization gets its own "owner" — so a lookup
// that forgot the organization would hand one tenant another's role.
func TestARoleIsFoundByKeyOnlyInsideItsOrganization(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		mine := b.newOrg(t)
		theirs := b.newOrg(t)

		b.newRole(t, mine, "auditor", string(authz.PermOrganizationRead))

		found, err := b.repo.RoleByKey(t.Context(), mine, "auditor")
		if err != nil {
			t.Fatalf("RoleByKey() = %v", err)
		}

		if found.Key != "auditor" {
			t.Errorf("key = %q", found.Key)
		}

		if _, err := b.repo.RoleByKey(t.Context(), theirs, "auditor"); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("RoleByKey() in another organization = %v, want ErrNotFound", err)
		}
	})
}

// TestRenamingARoleIsScopedAndReported pairs the two rules a targeted UPDATE has to
// carry: it may not reach another tenant's row, and it has to say when it changed
// nothing.
func TestRenamingARoleIsScopedAndReported(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		mine := b.newOrg(t)
		theirs := b.newOrg(t)
		role := b.newRole(t, mine, "auditor", string(authz.PermOrganizationRead))

		if err := b.repo.UpdateRole(t.Context(), mine, role, "Audytor", "Czyta"); err != nil {
			t.Fatalf("UpdateRole() = %v", err)
		}

		updated, err := b.repo.Role(t.Context(), mine, role)
		if err != nil {
			t.Fatalf("Role() = %v", err)
		}

		if updated.Name != "Audytor" || updated.Description != "Czyta" {
			t.Errorf("name/description = %q/%q", updated.Name, updated.Description)
		}

		if err := b.repo.UpdateRole(t.Context(), theirs, role, "Hijacked", ""); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("UpdateRole() from another organization = %v, want ErrNotFound", err)
		}

		// And the refused call changed nothing.
		after, err := b.repo.Role(t.Context(), mine, role)
		if err != nil {
			t.Fatalf("Role() = %v", err)
		}

		if after.Name != "Audytor" {
			t.Errorf("name = %q after a refused update from another organization", after.Name)
		}
	})
}

// TestReplacingPermissionsReplacesRatherThanAdds is why the method is called Replace.
// Two administrators editing the same role would otherwise merge into a set neither of
// them chose.
func TestReplacingPermissionsReplacesRatherThanAdds(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "auditor",
			string(authz.PermOrganizationRead), string(authz.PermMembersRead))

		err := b.repo.ReplaceRolePermissions(t.Context(), orgID, role,
			[]authz.Permission{authz.PermAuditRead})
		if err != nil {
			t.Fatalf("ReplaceRolePermissions() = %v", err)
		}

		got, err := b.repo.Role(t.Context(), orgID, role)
		if err != nil {
			t.Fatalf("Role() = %v", err)
		}

		want := []authz.Permission{authz.PermAuditRead}
		if !slices.Equal(got.Permissions, want) {
			t.Errorf("permissions = %v, want exactly %v", got.Permissions, want)
		}
	})
}

// --- what a membership grants ------------------------------------------------

// TestMemberPermissionsIgnoresSuspension is the documented rule, and the one that
// looks like a bug until the reason is read.
//
// A suspended member grants nothing while suspended, but this answers "how much power
// does this row carry" — which is what the rank rule compares against the caller. A
// suspended owner must not become removable by an administrator merely because
// somebody suspended them first.
func TestMemberPermissionsIgnoresSuspension(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "auditor",
			string(authz.PermOrganizationRead), string(authz.PermAuditRead))

		memberID, _ := addMember(t, b, orgID, role)

		before, err := b.repo.MemberPermissions(t.Context(), orgID, memberID)
		if err != nil {
			t.Fatalf("MemberPermissions() = %v", err)
		}

		if len(before) != 2 {
			t.Fatalf("permissions = %v, want two", before)
		}

		err = b.repo.SetMemberStatus(t.Context(), orgID, memberID,
			models.MembershipSuspended, time.Now().UTC(), orgs.RefuseLastOwnerLoss(true))
		if err != nil {
			t.Fatalf("SetMemberStatus() = %v", err)
		}

		after, err := b.repo.MemberPermissions(t.Context(), orgID, memberID)
		if err != nil {
			t.Fatalf("MemberPermissions() = %v", err)
		}

		if !slices.Equal(sortedPermissions(before), sortedPermissions(after)) {
			t.Errorf("suspension changed what the row carries: %v then %v", before, after)
		}
	})
}

// TestPermissionKeysByOrganizationCoversEveryMembership is what the snapshot endpoint
// renders from: one query, every organization the account is in.
func TestPermissionKeysByOrganizationCoversEveryMembership(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		first := b.newOrg(t)
		second := b.newOrg(t)

		userID, _ := b.newAccount(t)

		reader := b.newRole(t, first, "reader", string(authz.PermOrganizationRead))
		auditor := b.newRole(t, second, "auditor", string(authz.PermAuditRead))

		for orgID, role := range map[uuid.UUID]uuid.UUID{first: reader, second: auditor} {
			if _, err := b.repo.AddMember(t.Context(), orgID, userID,
				[]uuid.UUID{role}, uuid.Nil, time.Now().UTC()); err != nil {
				t.Fatalf("AddMember() = %v", err)
			}
		}

		byOrg, err := b.perms.PermissionKeysByOrganization(t.Context(), userID)
		if err != nil {
			t.Fatalf("PermissionKeysByOrganization() = %v", err)
		}

		if got := byOrg[first]; !slices.Contains(got, string(authz.PermOrganizationRead)) {
			t.Errorf("first organization = %v", got)
		}

		if got := byOrg[second]; !slices.Contains(got, string(authz.PermAuditRead)) {
			t.Errorf("second organization = %v", got)
		}

		// A deleted organization contributes nothing: the snapshot must not offer a
		// tenant that no longer exists.
		b.deleteOrg(t, second)

		byOrg, err = b.perms.PermissionKeysByOrganization(t.Context(), userID)
		if err != nil {
			t.Fatalf("PermissionKeysByOrganization() = %v", err)
		}

		if _, ok := byOrg[second]; ok {
			t.Error("a deleted organization is still in the snapshot")
		}
	})
}

// --- accounts ----------------------------------------------------------------

// TestSuspendingBumpsTheEpochAndRestoringDoesNot is the asymmetry the sessions rely
// on: suspending has to take effect on tokens already issued, and restoring must not
// invalidate the session of somebody who was never suspended in between.
func TestSuspendingBumpsTheEpochAndRestoringDoesNot(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, _ := b.newAccount(t)

		before, err := b.users.ByID(t.Context(), userID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}

		at := time.Now().UTC()
		if err := b.users.SetSuspended(t.Context(), userID, &at); err != nil {
			t.Fatalf("SetSuspended() = %v", err)
		}

		suspended, err := b.users.ByID(t.Context(), userID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}

		if suspended.SessionEpoch != before.SessionEpoch+1 {
			t.Errorf("epoch = %d, want %d", suspended.SessionEpoch, before.SessionEpoch+1)
		}

		if !suspended.IsSuspended() {
			t.Error("the account does not report itself suspended")
		}

		if err := b.users.SetSuspended(t.Context(), userID, nil); err != nil {
			t.Fatalf("SetSuspended(nil) = %v", err)
		}

		restored, err := b.users.ByID(t.Context(), userID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}

		if restored.SessionEpoch != suspended.SessionEpoch {
			t.Errorf("restoring moved the epoch to %d from %d; it must not",
				restored.SessionEpoch, suspended.SessionEpoch)
		}

		if restored.IsSuspended() {
			t.Error("the account is still suspended after being restored")
		}
	})
}

// TestTheAccountListingIsPagedAndHidesDeleted covers the one listing that crosses
// tenants, which is why it sits behind a system-scope permission.
func TestTheAccountListingIsPagedAndHidesDeleted(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		var ids []uuid.UUID

		for range 3 {
			id, _ := b.newAccount(t)
			ids = append(ids, id)
		}

		all, err := b.users.All(t.Context(), wholePage, 0)
		if err != nil {
			t.Fatalf("All() = %v", err)
		}

		if len(all) < 3 {
			t.Fatalf("All() returned %d accounts, want at least the three just created", len(all))
		}

		page, err := b.users.All(t.Context(), 2, 0)
		if err != nil {
			t.Fatalf("All(2, 0) = %v", err)
		}

		if len(page) != 2 {
			t.Errorf("All(2, 0) returned %d, want 2", len(page))
		}

		b.deleteAccount(t, ids[0])

		after, err := b.users.All(t.Context(), wholePage, 0)
		if err != nil {
			t.Fatalf("All() = %v", err)
		}

		for i := range after {
			if after[i].ID == ids[0] {
				t.Error("a deleted account is still in the installation listing")
			}
		}
	})
}

// --- invitations -------------------------------------------------------------

// TestTheInvitationListingsHideWhatIsNoLongerOpen pins the clock and the accepted
// flag on both listings at once, since they answer the same question from two sides.
func TestTheInvitationListingsHideWhatIsNoLongerOpen(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		now := time.Now().UTC()

		open, err := b.repo.InviteMember(t.Context(), orgID, "open@example.com",
			freshToken("open"), nil, uuid.Nil, now.Add(time.Hour), now)
		if err != nil {
			t.Fatalf("InviteMember() = %v", err)
		}

		expired, err := b.repo.InviteMember(t.Context(), orgID, "expired@example.com",
			freshToken("expired"), nil, uuid.Nil, now.Add(time.Hour), now)
		if err != nil {
			t.Fatalf("InviteMember() = %v", err)
		}

		// Reissue is the one operation allowed to move an invitation's clock, so it is
		// how a test produces an expired one without waiting a week.
		_, err = b.repo.ReissueInvitation(t.Context(), orgID, expired.ID,
			freshToken("expired-again"), now.Add(-time.Hour))
		if err != nil {
			t.Fatalf("ReissueInvitation() = %v", err)
		}

		forOrg, err := b.repo.InvitationsForOrganization(t.Context(), orgID, now)
		if err != nil {
			t.Fatalf("InvitationsForOrganization() = %v", err)
		}

		if len(forOrg) != 1 || forOrg[0].ID != open.ID {
			t.Errorf("the organization sees %d invitations, want only the open one", len(forOrg))
		}

		forEmail, err := b.dir.InvitationsForEmail(t.Context(), "expired@example.com", now)
		if err != nil {
			t.Fatalf("InvitationsForEmail() = %v", err)
		}

		if len(forEmail) != 0 {
			t.Errorf("the invitee sees %d expired invitations, want none", len(forEmail))
		}

		if _, err := b.dir.InvitationsForEmail(t.Context(), "open@example.com", now); err != nil {
			t.Fatalf("InvitationsForEmail() for the open one = %v", err)
		}
	})
}

// TestDecliningRemovesTheOffer is the invitee's side of ending an invitation. The row
// is deleted, so the audit entry is the only trace it existed.
func TestDecliningRemovesTheOffer(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		now := time.Now().UTC()

		token := freshToken("declined")

		invitation, err := b.repo.InviteMember(t.Context(), orgID, "bo@example.com",
			token, nil, uuid.Nil, now.Add(time.Hour), now)
		if err != nil {
			t.Fatalf("InviteMember() = %v", err)
		}

		if err := b.dir.DeclineInvitation(t.Context(), invitation.ID); err != nil {
			t.Fatalf("DeclineInvitation() = %v", err)
		}

		if _, err := b.dir.InvitationByToken(t.Context(), token, now); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("InvitationByToken() after declining = %v, want ErrNotFound", err)
		}

		// Declining twice is not an error the second time either: the offer is gone,
		// which is the state the caller asked for.
		if err := b.dir.DeclineInvitation(t.Context(), invitation.ID); err != nil &&
			!errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("DeclineInvitation() twice = %v, want nil or ErrNotFound", err)
		}
	})
}

// --- the email change --------------------------------------------------------

// TestReplacingAnEmailChangeSupersedesThePreviousOne is why the method is Replace:
// asking again must not leave two codes that both work.
func TestReplacingAnEmailChangeSupersedesThePreviousOne(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, _ := b.newAccount(t)
		now := time.Now().UTC()

		first := &models.EmailChange{
			UserID: userID, NewEmail: "first@example.com",
			CodeHash: "hash-first", ExpiresAt: now.Add(time.Hour),
		}
		if err := b.users.ReplaceEmailChange(t.Context(), first); err != nil {
			t.Fatalf("ReplaceEmailChange() = %v", err)
		}

		second := &models.EmailChange{
			UserID: userID, NewEmail: "second@example.com",
			CodeHash: "hash-second", ExpiresAt: now.Add(time.Hour),
		}
		if err := b.users.ReplaceEmailChange(t.Context(), second); err != nil {
			t.Fatalf("ReplaceEmailChange() = %v", err)
		}

		active, err := b.users.ActiveEmailChange(t.Context(), userID, now)
		if err != nil {
			t.Fatalf("ActiveEmailChange() = %v", err)
		}

		if active.CodeHash != "hash-second" {
			t.Errorf("the active code is %q, want the one asked for last", active.CodeHash)
		}
	})
}

// TestAnExpiredEmailChangeIsNotActive is the clock, asked of the store rather than of
// the service that usually reads it.
func TestAnExpiredEmailChangeIsNotActive(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, _ := b.newAccount(t)
		now := time.Now().UTC()

		change := &models.EmailChange{
			UserID: userID, NewEmail: "late@example.com",
			CodeHash: "hash-late", ExpiresAt: now.Add(-time.Minute),
		}
		if err := b.users.ReplaceEmailChange(t.Context(), change); err != nil {
			t.Fatalf("ReplaceEmailChange() = %v", err)
		}

		if _, err := b.users.ActiveEmailChange(t.Context(), userID, now); !errors.Is(err, user.ErrNotFound) {
			t.Errorf("ActiveEmailChange() for an expired one = %v, want ErrNotFound", err)
		}
	})
}

// TestTheEmailChangeAttemptCounterSpendsTheCode is the same rule as the password
// reset's, and it has to move in one statement for the same reason: a
// read-modify-write lets concurrent guesses share an attempt, and a five-attempt cap
// stops capping.
func TestTheEmailChangeAttemptCounterSpendsTheCode(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		const maxAttempts = 3

		userID, _ := b.newAccount(t)
		now := time.Now().UTC()

		change := &models.EmailChange{
			UserID: userID, NewEmail: "guarded@example.com",
			CodeHash: "hash-guarded", ExpiresAt: now.Add(time.Hour),
		}
		if err := b.users.ReplaceEmailChange(t.Context(), change); err != nil {
			t.Fatalf("ReplaceEmailChange() = %v", err)
		}

		stored, err := b.users.ActiveEmailChange(t.Context(), userID, now)
		if err != nil {
			t.Fatalf("ActiveEmailChange() = %v", err)
		}

		for i := 1; i <= maxAttempts; i++ {
			if err := b.users.FailEmailChange(t.Context(), stored.ID, maxAttempts, now); err != nil {
				t.Fatalf("FailEmailChange() attempt %d = %v", i, err)
			}
		}

		if _, err := b.users.ActiveEmailChange(t.Context(), userID, now); !errors.Is(err, user.ErrNotFound) {
			t.Errorf("the code survived %d wrong guesses: %v", maxAttempts, err)
		}
	})
}

// TestConfirmingAnAddressReportsOneThatWasTaken is the one place ErrEmailTaken reaches
// the outside. By then the caller has read a code out of that mailbox, so the answer
// tells them nothing they could not already find out.
func TestConfirmingAnAddressReportsOneThatWasTaken(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, _ := b.newAccount(t)
		_, takenEmail := b.newAccount(t)
		now := time.Now().UTC()

		change := &models.EmailChange{
			UserID: userID, NewEmail: takenEmail,
			CodeHash: "hash-collision", ExpiresAt: now.Add(time.Hour),
		}
		if err := b.users.ReplaceEmailChange(t.Context(), change); err != nil {
			t.Fatalf("ReplaceEmailChange() = %v", err)
		}

		stored, err := b.users.ActiveEmailChange(t.Context(), userID, now)
		if err != nil {
			t.Fatalf("ActiveEmailChange() = %v", err)
		}

		spent := now
		stored.ConsumedAt = &spent

		if err := b.users.ConsumeEmailChange(t.Context(), stored, takenEmail); !errors.Is(err, user.ErrEmailTaken) {
			t.Errorf("ConsumeEmailChange() onto a taken address = %v, want ErrEmailTaken", err)
		}

		// And the account keeps the address it had.
		account, err := b.users.ByID(t.Context(), userID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}

		if account.Email == takenEmail {
			t.Error("the address moved anyway")
		}
	})
}

// freshToken is a token hash no other run has used.
//
// invitations.token_hash is unique across the whole installation, so a literal here
// makes the suite pass once and then fail on the same database — which is exactly what
// happened, and what the older invitation tests already had a note about after
// colliding on the literal "a-token". task test:store hides it behind a throwaway
// container; a developer pointing the suite at a persistent database does not have one.
func freshToken(prefix string) string {
	return prefix + "-" + uuid.Must(uuid.NewV7()).String()
}

// sortedPermissions makes two permission sets comparable regardless of the order the
// two implementations happen to produce.
func sortedPermissions(in []authz.Permission) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, string(p))
	}

	slices.Sort(out)

	return out
}

// --- the audit history ------------------------------------------------------

// actingAs puts an actor on the context, which is what makes a change recordable: the
// store writes no entry without one.
func actingAs(t *testing.T, userID uuid.UUID) context.Context {
	t.Helper()

	return audit.WithActor(t.Context(), audit.Actor{
		ID:        userID,
		IP:        "127.0.0.1",
		UserAgent: "contract",
	})
}

// TestTheHistoryIsScopedToItsOrganization is the reader's half of the tenancy rule. The
// installation-wide listing sits behind a system-scope permission precisely because the
// organization-scoped one must never show another tenant's history.
func TestTheHistoryIsScopedToItsOrganization(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		mine := b.newOrg(t)
		theirs := b.newOrg(t)

		_, actor := addMember(t, b, mine, b.newRole(t, mine, "auditor", string(authz.PermAuditRead)))
		ctx := actingAs(t, actor)

		// A change in each organization, so each has something to show.
		if err := b.repo.UpdateOrganization(ctx, mine, "Mine"); err != nil {
			t.Fatalf("UpdateOrganization(mine) = %v", err)
		}

		if err := b.repo.UpdateOrganization(ctx, theirs, "Theirs"); err != nil {
			t.Fatalf("UpdateOrganization(theirs) = %v", err)
		}

		events, err := b.audit.Events(t.Context(), mine, wholePage, 0)
		if err != nil {
			t.Fatalf("Events() = %v", err)
		}

		if len(events) == 0 {
			t.Fatal("the organization's own history is empty")
		}

		for _, event := range events {
			if event.OrganizationID == nil || *event.OrganizationID != mine {
				t.Errorf("an entry for %v appeared in %v's history", event.OrganizationID, mine)
			}
		}
	})
}

// TestTheHistoryIsNewestFirstAndPaged pins the order the screen renders in, and the page
// boundary. An audit log read in the wrong order is one nobody trusts.
func TestTheHistoryIsNewestFirstAndPaged(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		_, actor := addMember(t, b, orgID)
		ctx := actingAs(t, actor)

		for _, name := range []string{"First", "Second", "Third"} {
			if err := b.repo.UpdateOrganization(ctx, orgID, name); err != nil {
				t.Fatalf("UpdateOrganization(%s) = %v", name, err)
			}
		}

		all, err := b.audit.Events(t.Context(), orgID, wholePage, 0)
		if err != nil {
			t.Fatalf("Events() = %v", err)
		}

		if len(all) < 3 {
			t.Fatalf("%d entries, want at least the three renames", len(all))
		}

		for i := 1; i < len(all); i++ {
			if all[i].At.After(all[i-1].At) {
				t.Errorf("entry %d is newer than the one before it; the order is not newest first", i)
			}
		}

		// A page of one, then the next: the two must not be the same entry.
		first, err := b.audit.Events(t.Context(), orgID, 1, 0)
		if err != nil {
			t.Fatalf("Events(1, 0) = %v", err)
		}

		second, err := b.audit.Events(t.Context(), orgID, 1, 1)
		if err != nil {
			t.Fatalf("Events(1, 1) = %v", err)
		}

		if len(first) != 1 || len(second) != 1 {
			t.Fatalf("pages of one returned %d and %d entries", len(first), len(second))
		}

		if first[0].ID == second[0].ID {
			t.Error("the second page repeats the first entry")
		}
	})
}

// TestTheHistoryKeepsEntriesWhoseActorIsGone is the rule the joins are LEFT for, and the
// one the port to ent could have broken silently.
//
// Dropping an entry because its account was deleted would erase exactly the changes
// somebody is most likely to be looking for — what did the person who has since left do.
// Reading it also means the joined name and address come back NULL, which the SQL layer
// has to fold into empty strings rather than fail on.
func TestTheHistoryKeepsEntriesWhoseActorIsGone(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		_, actor := addMember(t, b, orgID)

		if err := b.repo.UpdateOrganization(actingAs(t, actor), orgID, "Renamed"); err != nil {
			t.Fatalf("UpdateOrganization() = %v", err)
		}

		b.deleteAccount(t, actor)

		events, err := b.audit.Events(t.Context(), orgID, wholePage, 0)
		if err != nil {
			t.Fatalf("Events() after deleting the actor = %v", err)
		}

		var found bool

		for _, event := range events {
			if event.Actor.ID == actor {
				found = true
			}
		}

		if !found {
			t.Error("the entry disappeared with its actor; the history has to outlive the account")
		}
	})
}

// TestTheInstallationHistoryCrossesOrganizations is the other reader: the same rows,
// without the tenancy filter, which is why it sits behind its own permission.
func TestTheInstallationHistoryCrossesOrganizations(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		first := b.newOrg(t)
		second := b.newOrg(t)

		_, actor := addMember(t, b, first)
		ctx := actingAs(t, actor)

		if err := b.repo.UpdateOrganization(ctx, first, "First"); err != nil {
			t.Fatalf("UpdateOrganization(first) = %v", err)
		}

		if err := b.repo.UpdateOrganization(ctx, second, "Second"); err != nil {
			t.Fatalf("UpdateOrganization(second) = %v", err)
		}

		events, err := b.platformAudit.AllEvents(t.Context(), wholePage, 0)
		if err != nil {
			t.Fatalf("AllEvents() = %v", err)
		}

		seen := map[uuid.UUID]bool{}

		for _, event := range events {
			if event.OrganizationID != nil {
				seen[*event.OrganizationID] = true
			}
		}

		for _, orgID := range []uuid.UUID{first, second} {
			if !seen[orgID] {
				t.Errorf("%v is missing from the installation-wide history", orgID)
			}
		}
	})
}
