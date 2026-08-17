package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

type systemRoleHolders struct {
	Holders []struct {
		UserID    uuid.UUID  `json:"user_id"`
		Email     string     `json:"email"`
		RoleKey   string     `json:"role_key"`
		GrantedBy *uuid.UUID `json:"granted_by"`
	} `json:"holders"`
}

func listSystemRoles(t *testing.T, f *authzFixture) systemRoleHolders {
	t.Helper()

	var out systemRoleHolders
	f.call(t, http.MethodGet, "/v1/platform/system-roles", "").
		expect(t, http.StatusOK).decode(t, &out)

	return out
}

// TestGrantingAnInstallationRoleIsRecorded is the gap this closes.
//
// platform_admin covers every platform.* permission, so granting it is the most
// consequential change anybody can make — and it left no trace at all. There was no
// endpoint, GrantSystemRole never called record, and the two action constants for it
// were dead code, while the design document claimed every change to authority was
// logged.
func TestGrantingAnInstallationRoleIsRecorded(t *testing.T) {
	f := newAuthzFixture(t)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	target := registerOutsider(t, f)

	var account struct {
		Users []struct {
			ID    uuid.UUID `json:"id"`
			Email string    `json:"email"`
		} `json:"users"`
	}
	f.call(t, http.MethodGet, "/v1/platform/users", "").
		expect(t, http.StatusOK).decode(t, &account)

	var targetID uuid.UUID

	for _, u := range account.Users {
		if u.Email == target {
			targetID = u.ID
		}
	}

	if targetID == uuid.Nil {
		t.Fatalf("the second account is missing from the platform listing: %+v", account.Users)
	}

	body := fmt.Sprintf(`{"user_id":%q,"role_key":%q}`, targetID, authz.RolePlatformAdmin)
	f.call(t, http.MethodPost, "/v1/platform/system-roles", body).expect(t, http.StatusNoContent)

	// Visible, with who granted it.
	holders := listSystemRoles(t, f)

	found := false

	for _, holder := range holders.Holders {
		if holder.UserID != targetID {
			continue
		}

		found = true

		if holder.RoleKey != string(authz.RolePlatformAdmin) {
			t.Errorf("role_key = %q, want platform_admin", holder.RoleKey)
		}

		if holder.GrantedBy == nil || *holder.GrantedBy != f.userID {
			t.Errorf("granted_by = %v, want the caller %v", holder.GrantedBy, f.userID)
		}
	}

	if !found {
		t.Fatalf("the grant is not listed; holders are %+v", holders.Holders)
	}

	// And recorded. The entry has no organization, which is what makes it a
	// platform-scoped one, so it is read from the installation-wide log.
	var log struct {
		Events []auditEventBody `json:"events"`
	}
	f.call(t, http.MethodGet, "/v1/platform/audit", "").
		expect(t, http.StatusOK).decode(t, &log)

	if !hasEvent(log.Events, string(models.ActionSystemRoleGranted), f.userID, targetID) {
		t.Errorf("no %s entry attributing the grant to the caller; the log has %+v",
			models.ActionSystemRoleGranted, log.Events)
	}

	// Granting again changes nothing and records nothing: an entry for a grant that
	// did not happen would be a second answer to "when did they get this".
	before := len(log.Events)

	f.call(t, http.MethodPost, "/v1/platform/system-roles", body).expect(t, http.StatusNoContent)
	f.call(t, http.MethodGet, "/v1/platform/audit", "").expect(t, http.StatusOK).decode(t, &log)

	if len(log.Events) != before {
		t.Errorf("the log grew from %d to %d on a repeated grant", before, len(log.Events))
	}
}

// TestRevokingAnInstallationRoleIsRecorded covers the other half, which had no way
// to happen at all: the role could be granted and never taken back except in SQL.
func TestRevokingAnInstallationRoleIsRecorded(t *testing.T) {
	f := newAuthzFixture(t)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	other := uuid.Must(uuid.NewV7())
	f.repo.SeedSystemRole(other, string(authz.RolePlatformAdmin))

	path := "/v1/platform/system-roles/" + other.String() + "/" + string(authz.RolePlatformAdmin)
	f.call(t, http.MethodDelete, path, "").expect(t, http.StatusNoContent)

	for _, holder := range listSystemRoles(t, f).Holders {
		if holder.UserID == other {
			t.Errorf("the role is still held after revoking: %+v", holder)
		}
	}

	var log struct {
		Events []auditEventBody `json:"events"`
	}
	f.call(t, http.MethodGet, "/v1/platform/audit", "").
		expect(t, http.StatusOK).decode(t, &log)

	if !hasEvent(log.Events, string(models.ActionSystemRoleRevoked), f.userID, other) {
		t.Errorf("no %s entry; the log has %+v", models.ActionSystemRoleRevoked, log.Events)
	}

	// Revoking again is not an error and records nothing.
	before := len(log.Events)

	f.call(t, http.MethodDelete, path, "").expect(t, http.StatusNoContent)
	f.call(t, http.MethodGet, "/v1/platform/audit", "").expect(t, http.StatusOK).decode(t, &log)

	if len(log.Events) != before {
		t.Errorf("the log grew from %d to %d on a repeated revoke", before, len(log.Events))
	}
}

// TestTheLastPlatformAdminCannotRevokeThemselves keeps the one mistake that has no
// way back inside the product.
//
// Revoking your own platform_admin takes away the permission needed to grant it
// again, and with no other holder there is nobody left who can. Recovering means the
// bootstrap command and database access.
func TestTheLastPlatformAdminCannotRevokeThemselves(t *testing.T) {
	f := newAuthzFixture(t)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	path := "/v1/platform/system-roles/" + f.userID.String() + "/" + string(authz.RolePlatformAdmin)

	res := f.call(t, http.MethodDelete, path, "").expect(t, http.StatusConflict)

	var doc problemBody
	res.decode(t, &doc)

	if doc.Code != problem.CodeLastSystemRole {
		t.Errorf("code = %q, want %q", doc.Code, problem.CodeLastSystemRole)
	}

	// Still an administrator, so the refusal refused rather than half-acting.
	f.call(t, http.MethodGet, "/v1/platform/system-roles", "").expect(t, http.StatusOK)

	// With a second holder it goes through: the rule counts rather than forbidding.
	f.repo.SeedSystemRole(uuid.Must(uuid.NewV7()), string(authz.RolePlatformAdmin))
	f.call(t, http.MethodDelete, path, "").expect(t, http.StatusNoContent)
}

// TestOnlyAnInstallationRoleCanBeGrantedThere stops an organization role key being
// written into user_system_roles, where nothing would ever read it.
func TestOnlyAnInstallationRoleCanBeGrantedThere(t *testing.T) {
	f := newAuthzFixture(t)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	for _, key := range []string{string(authz.RoleOwner), "not_a_role"} {
		body := fmt.Sprintf(`{"user_id":%q,"role_key":%q}`, uuid.Must(uuid.NewV7()), key)

		res := f.call(t, http.MethodPost, "/v1/platform/system-roles", body).
			expect(t, http.StatusUnprocessableEntity)

		var doc problemBody
		res.decode(t, &doc)

		if doc.Code != problem.CodeInvalidSystemRole {
			t.Errorf("%s: code = %q, want %q", key, doc.Code, problem.CodeInvalidSystemRole)
		}
	}
}

// hasEvent reports whether the log carries an entry with this action, actor and
// subject.
func hasEvent(events []auditEventBody, action string, actor, subject uuid.UUID) bool {
	for _, event := range events {
		if event.Action != action || event.Actor.ID != actor {
			continue
		}

		if event.Subject != nil && event.Subject.ID == subject {
			return true
		}
	}

	return false
}
