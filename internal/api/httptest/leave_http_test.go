package httptest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// leavePath is the self-service route. It names the membership, not the
// organization: an {orgID} in a path means the middleware resolved a permission
// there, and this operation has none behind it.
func leavePath(membershipID uuid.UUID) string {
	return "/v1/me/memberships/" + membershipID.String()
}

// TestAMemberWithNoPermissionsCanLeave is the point of the endpoint.
//
// The fixture holds no roles at all, so every gated route in the organization
// refuses her. Until this existed her only ways out were asking an administrator
// or calling remove-member on herself, which needs members.remove — so the callers
// most likely to want out were exactly the ones who could not.
func TestAMemberWithNoPermissionsCanLeave(t *testing.T) {
	f := NewAuthzFixture(t)

	// 403 rather than 404: she is an active member, so the organization is not
	// hidden from her — she simply may not Do anything in it, which is the caller
	// this endpoint exists for.
	if got := f.GetOrg(t); got != http.StatusForbidden {
		t.Fatalf("reading the organization with no roles = %d, want 403 — the "+
			"fixture is not the permissionless member this test needs", got)
	}

	f.call(t, http.MethodDelete, leavePath(f.Membership), "").
		expect(t, http.StatusNoContent)

	var body struct {
		Organizations []struct {
			ID uuid.UUID `json:"id"`
		} `json:"organizations"`
	}
	f.call(t, http.MethodGet, "/v1/me/organizations", "").
		expect(t, http.StatusOK).decode(t, &body)

	// Not "the list is empty": registering also joined the default organization, and
	// leaving one place is not leaving everywhere.
	for _, org := range body.Organizations {
		if org.ID == f.OrgID {
			t.Errorf("%v is still in the caller's own list after a 204", f.OrgID)
		}
	}

	if len(body.Organizations) == 0 {
		t.Error("the caller left every organization; only the one named should go")
	}
}

// TestLeavingTwiceIsNotFound pins that the second attempt is a 404 rather than a
// 204. The row is gone, and reporting success for a membership that does not exist
// would make "am I out" unanswerable.
func TestLeavingTwiceIsNotFound(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	f.call(t, http.MethodDelete, leavePath(f.Membership), "").
		expect(t, http.StatusNoContent)
	f.call(t, http.MethodDelete, leavePath(f.Membership), "").
		expect(t, http.StatusNotFound)
}

// TestLeavingSomebodyElsesMembershipIsNotFound is the authorization.
//
// There is no permission to check here, so what stands in for one is that the
// membership has to be in the caller's own list. Somebody else's id answers 404 —
// not 403, which would confirm it exists — and the row survives.
func TestLeavingSomebodyElsesMembershipIsNotFound(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	other := f.Repo.SeedMember(f.OrgID, uuid.Must(uuid.NewV7()), ent.MembershipActive)

	f.call(t, http.MethodDelete, leavePath(other), "").
		expect(t, http.StatusNotFound)

	var body struct {
		Members []struct {
			ID uuid.UUID `json:"id"`
		} `json:"members"`
	}
	f.call(t, http.MethodGet, f.orgPath("/members"), "").
		expect(t, http.StatusOK).decode(t, &body)

	found := false

	for _, m := range body.Members {
		if m.ID == other {
			found = true
		}
	}

	if !found {
		t.Error("the other membership is gone; a 404 to the caller must not mean a " +
			"deletion happened anyway")
	}
}

// TestTheLastOwnerCannotLeave keeps the one rule that outlives self-service.
//
// Somebody has to be able to administer an organization, and "I left" is not a
// reason to make an exception. The refusal is a 409 with a code the client can act
// on, because appointing another owner is something the caller can actually Do.
func TestTheLastOwnerCannotLeave(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	var doc ProblemBody
	f.call(t, http.MethodDelete, leavePath(f.Membership), "").
		expect(t, http.StatusConflict).decode(t, &doc)

	if doc.Code != "last_owner" {
		t.Errorf("code = %q, want %q", doc.Code, "last_owner")
	}

	if got := f.GetOrg(t); got != http.StatusOK {
		t.Errorf("reading the organization after the refusal = %d, want 200; the "+
			"membership must survive a refused departure", got)
	}
}

// TestAnOwnerCanLeaveOnceThereIsAnother is the other half: the rule is about the
// organization keeping an owner, not about owners being trapped.
func TestAnOwnerCanLeaveOnceThereIsAnother(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	owner := f.Repo.SeedShippedRole(f.OrgID, authz.RoleOwner)
	f.Repo.SeedMember(f.OrgID, uuid.Must(uuid.NewV7()), ent.MembershipActive, owner)

	f.call(t, http.MethodDelete, leavePath(f.Membership), "").
		expect(t, http.StatusNoContent)
}

// TestLeavingIsRecordedAsLeaving checks the audit vocabulary.
//
// Both departures delete the same row, and a reader could tell them apart by
// comparing actor to subject, but "Ada removed Ada from the organization" is not
// what happened. The entry is read from the installation-wide log because the
// caller is no longer in the organization whose log it is — which is itself worth
// pinning: leaving takes the audit route away with everything else.
func TestLeavingIsRecordedAsLeaving(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	second := f.Repo.SeedShippedRole(f.OrgID, authz.RoleOwner)
	f.Repo.SeedMember(f.OrgID, uuid.Must(uuid.NewV7()), ent.MembershipActive, second)

	f.call(t, http.MethodDelete, leavePath(f.Membership), "").
		expect(t, http.StatusNoContent)

	f.call(t, http.MethodGet, f.orgPath("/audit"), "").
		expect(t, http.StatusNotFound)

	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	var body struct {
		Events []auditEventBody `json:"events"`
	}
	f.call(t, http.MethodGet, "/v1/platform/audit", "").
		expect(t, http.StatusOK).decode(t, &body)

	if len(body.Events) == 0 {
		t.Fatal("no audit entries at all; leaving recorded nothing")
	}

	newest := body.Events[0]

	if newest.Action != string(ent.ActionMemberLeft) {
		t.Errorf("action = %q, want %q — a departure is not a removal",
			newest.Action, ent.ActionMemberLeft)
	}

	if newest.Actor.ID != f.UserID {
		t.Errorf("actor = %v, want the caller %v", newest.Actor.ID, f.UserID)
	}

	if newest.Subject.ID != f.UserID {
		t.Errorf("subject = %v, want the caller %v; actor and subject being the "+
			"same person is what a departure looks like", newest.Subject.ID, f.UserID)
	}
}
