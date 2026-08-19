package httptest

import (
	"net/http"
	stdhttptest "net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

type auditEventBody struct {
	ID     uuid.UUID `json:"id"`
	Action string    `json:"action"`
	Actor  struct {
		ID    uuid.UUID `json:"id"`
		Email string    `json:"email"`
	} `json:"actor"`
	Subject *struct {
		ID uuid.UUID `json:"id"`
	} `json:"subject"`
	RoleKey string `json:"role_key"`
	Detail  string `json:"detail"`
	IP      string `json:"ip"`
}

func (f *AuthzFixture) auditLog(t *testing.T) []auditEventBody {
	t.Helper()

	var body struct {
		Events []auditEventBody `json:"events"`
	}
	f.call(t, http.MethodGet, f.orgPath("/audit"), "").
		expect(t, http.StatusOK).decode(t, &body)

	return body.Events
}

// TestRegisteringIsAudited covers the one mutation that reaches the store without
// a session behind it.
//
// Nothing is recorded without an actor, and only requireBearer sets one — so
// joining the default organization at registration left an active membership and
// no record of how anybody got there. The actor is the new account itself, which
// is both the honest description and an id that resolves to a real row, so the
// entry renders with a name rather than a bare uuid.
func TestRegisteringIsAudited(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	defaultOrg, err := f.Repo.OrganizationBySlug(t.Context(), ent.DefaultOrganizationSlug)
	if err != nil {
		t.Fatalf("default organization: %v", err)
	}

	// Reading the default organization's log needs audit.read there, and
	// registration only made Ada a plain member of it.
	promoteInDefaultOrganization(t, f, defaultOrg.ID)

	const joiner = "pat@example.com"
	registerAccount(t, f, joiner)

	var body struct {
		Events []auditEventBody `json:"events"`
	}
	f.call(t, http.MethodGet, "/v1/orgs/"+defaultOrg.ID.String()+"/audit", "").
		expect(t, http.StatusOK).decode(t, &body)

	found := false

	for _, event := range body.Events {
		if event.Action != string(ent.ActionMemberJoined) || event.Actor.Email != joiner {
			continue
		}

		found = true

		// Actor and subject are the same person, which is what makes this
		// entry different from every other one in the log.
		if event.Subject == nil || event.Subject.ID != event.Actor.ID {
			t.Errorf("subject = %v, want the same account as the actor %v", event.Subject, event.Actor.ID)
		}
	}

	if !found {
		t.Errorf("no %s entry for %s; the log has %+v",
			ent.ActionMemberJoined, joiner, body.Events)
	}
}

// mutatingProbes are the organization-scoped operations that change something,
// with the action each must record.
//
// The read-only ones are absent by design; TestEveryMutatingOperationIsAudited
// checks that the split still matches operationAccess, so a new mutating
// operation cannot be quietly left out of both.
var mutatingProbes = map[string]ent.AuthzAction{
	"update-organization":  ent.ActionOrganizationUpdated,
	"delete-organization":  ent.ActionOrganizationDeleted,
	"add-member":           ent.ActionMemberInvited,
	"update-member-status": ent.ActionMemberReinstated,
	"remove-member":        ent.ActionMemberRemoved,
	"set-member-roles":     ent.ActionMemberRolesChanged,
	"create-role":          ent.ActionRoleCreated,
	"update-role":          ent.ActionRoleUpdated,
	"set-role-permissions": ent.ActionRolePermissionsChanged,
	"delete-role":          ent.ActionRoleDeleted,
	"reissue-invitation":   ent.ActionMemberInvited,
	// The batch records one entry per address, the same as inviting one at a time —
	// which is the point of it reusing Invite rather than a bulk insert.
	"invite-members":      ent.ActionMemberInvited,
	"withdraw-invitation": ent.ActionMemberInvitationWithdrawn,
	"upload-file":         ent.ActionFileUploaded,
	"delete-file":         ent.ActionFileDeleted,
}

var readOnlyProbes = []string{
	"get-organization", "list-members", "list-roles", "get-role", "list-audit-events",
	"list-invitations",
	"list-files", "get-file", "download-file",
}

// TestEveryMutatingOperationIsAudited is the guard the audit trail actually
// needs.
//
// The actor travels on the context, which is implicit and therefore forgettable:
// a repository method that never reads it writes no row, and nothing about the
// change itself looks wrong. So the check is on the outcome. Each mutating
// endpoint is called for real and must leave exactly the entry it claims to.
//
// It also fails when a gated operation appears in neither table, which is what
// stops a new mutating endpoint being added without anybody deciding whether it
// belongs in the log.
func TestEveryMutatingOperationIsAudited(t *testing.T) {
	t.Run("every gated operation is classified as mutating or read-only", func(t *testing.T) {
		for id, rule := range api.OperationAccess() {
			if rule.Scope != authz.ScopeOrganization {
				continue
			}

			_, mutating := mutatingProbes[id]
			readOnly := slices.Contains(readOnlyProbes, id)

			switch {
			case mutating && readOnly:
				t.Errorf("%s is listed as both mutating and read-only", id)
			case !mutating && !readOnly:
				t.Errorf("%s is gated but appears in neither table; does it change "+
					"anything, and if so what should the audit log say?", id)
			}
		}
	})

	for id, want := range mutatingProbes {
		t.Run(id, func(t *testing.T) {
			// A fresh organization per operation: several of these delete the
			// thing the next one would act on, and the log has to start empty
			// for the assertion below to mean anything.
			f := NewAuthzFixture(t, authz.RoleOwner)

			// Two roles: one held by somebody, and a free one for delete-role —
			// deleting an assigned role is refused, and the refusal would look
			// like a missing audit entry.
			held := f.Repo.SeedRole(f.OrgID, "held_role")
			free := f.Repo.SeedRole(f.OrgID, "free_role")

			// Somebody else to act on, so remove-member and the status change Do
			// not hit the last owner and get refused for an unrelated reason.
			other := f.Repo.SeedMember(f.OrgID, uuid.Must(uuid.NewV7()),
				ent.MembershipActive, held)

			// A registered account that is not yet in the organization, for
			// add-member. Ada is already the owner.
			outsider := registerOutsider(t, f)

			// An outstanding invitation for the two operations that act on one.
			invitation := f.Repo.SeedInvitation(f.OrgID, "invited@example.com",
				"a-probe-token", time.Now().UTC().Add(orgs.InvitationTTL), held)

			seededFile := f.Repo.SeedFile(f.OrgID, uuid.Nil, f.UserID, "seed.png", "image/png")

			probes := auditProbes(f, held, free, other, invitation, outsider, seededFile.ID)

			p, ok := probes[id]
			if !ok {
				t.Fatalf("%s has no probe", id)
			}

			before := len(f.auditLog(t))

			var rec *stdhttptest.ResponseRecorder
			if id == "upload-file" {
				rec = Do(t, f.Server.Handler(), authedUpload(t, f, p.path, "shot.png", minimalPNG()))
			} else {
				rec = Do(t, f.Server.Handler(),
					Authed(t, p.method, p.path, p.body, f.Token, ""))
			}
			if rec.Code >= http.StatusBadRequest {
				t.Fatalf("%s %s = %d; body %s", p.method, p.path, rec.Code, rec.Body.Bytes())
			}

			// Deleting the organization takes the log with it, so the entry is
			// read from the installation-wide view instead.
			var events []auditEventBody
			if id == "delete-organization" {
				f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

				var body struct {
					Events []auditEventBody `json:"events"`
				}
				f.call(t, http.MethodGet, "/v1/platform/audit", "").
					expect(t, http.StatusOK).decode(t, &body)

				events = body.Events
			} else {
				events = f.auditLog(t)
			}

			if len(events) <= before {
				t.Fatalf("%s recorded nothing; the audit trail is missing the change "+
					"it exists to describe", id)
			}

			newest := events[0]

			if newest.Action != string(want) {
				t.Errorf("action = %q, want %q", newest.Action, want)
			}

			// Attribution is the whole point: an entry that does not say who is
			// worse than no entry, because it looks like coverage.
			if newest.Actor.ID != f.UserID {
				t.Errorf("actor = %v, want the caller %v", newest.Actor.ID, f.UserID)
			}

			if newest.Actor.Email != TestEmail {
				t.Errorf("actor email = %q, want %q — the reader should not need a "+
					"second lookup to know who this was", newest.Actor.Email, TestEmail)
			}
		})
	}
}

// registerOutsider signs up a second account through the API, so add-member has
// somebody real to add who is not already a member.
func registerOutsider(t *testing.T, f *AuthzFixture) string {
	t.Helper()

	const email = "bob@example.com"

	body := `{"name":"Bob","email":"` + email + `","password":"twelve-chars","password_confirm":"twelve-chars"}`
	if rec := PostJSON(t, f.Server.Handler(), "/v1/users", body); rec.Code != http.StatusNoContent {
		t.Fatalf("register outsider = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	return email
}

// auditProbes renders each mutating operation against the fixture's own ids.
//
// heldRole is assigned to memberID; freeRole is not, so deleting it is not
// refused for being in use.
func auditProbes(
	f *AuthzFixture,
	heldRole, freeRole, memberID, invitationID uuid.UUID,
	outsider string,
	fileID uuid.UUID,
) map[string]probe {
	org := f.orgPath("")
	member := f.orgPath("/members/" + memberID.String())
	role := f.orgPath("/roles/" + heldRole.String())
	invitation := f.orgPath("/invitations/" + invitationID.String())

	return map[string]probe{
		"update-organization":  {http.MethodPatch, org, `{"name":"Renamed"}`},
		"delete-organization":  {http.MethodDelete, org, ""},
		"add-member":           {http.MethodPost, org + "/members", `{"email":"` + outsider + `","role_ids":[]}`},
		"invite-members":       {http.MethodPost, org + "/invitations", `{"emails":["batch@example.com"],"role_ids":[]}`},
		"update-member-status": {http.MethodPatch, member, `{"status":"active"}`},
		"remove-member":        {http.MethodDelete, member, ""},
		"set-member-roles":     {http.MethodPut, member + "/roles", `{"role_ids":[]}`},
		"create-role":          {http.MethodPost, org + "/roles", `{"key":"probe","name":"Probe","permissions":[]}`},
		"update-role":          {http.MethodPatch, role, `{"name":"Probed"}`},
		"set-role-permissions": {http.MethodPut, role + "/permissions", `{"permissions":[]}`},
		"delete-role":          {http.MethodDelete, f.orgPath("/roles/" + freeRole.String()), ""},

		// Reissue is listed under add-member's action: mailing a fresh token is the
		// same event from the invitee's side, and the entry says which address.
		"reissue-invitation":  {http.MethodPost, invitation + "/reissue", ""},
		"withdraw-invitation": {http.MethodDelete, invitation, ""},

		"upload-file": {http.MethodPost, org + "/files", ""},
		"delete-file": {http.MethodDelete, org + "/files/" + fileID.String(), ""},
	}
}

// TestTheAuditLogNamesWhoItWasAbout covers the subject, which is resolved from
// the membership before the row is deleted — otherwise "who removed them" has
// nothing left to point at.
func TestTheAuditLogNamesWhoItWasAbout(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	victim := uuid.Must(uuid.NewV7())
	member := f.Repo.SeedMember(f.OrgID, victim, ent.MembershipActive)

	f.call(t, http.MethodDelete, f.orgPath("/members/"+member.String()), "").
		expect(t, http.StatusNoContent)

	events := f.auditLog(t)
	if len(events) == 0 {
		t.Fatal("nothing recorded")
	}

	newest := events[0]
	if newest.Subject == nil {
		t.Fatalf("entry %q has no subject", newest.Action)
	}

	if newest.Subject.ID != victim {
		t.Errorf("subject = %v, want the removed account %v", newest.Subject.ID, victim)
	}
}

// TestARefusedChangeIsNotAudited is the other half of writing in the same
// transaction. A log that records attempts as if they succeeded is worse than
// one that records nothing.
func TestARefusedChangeIsNotAudited(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	before := len(f.auditLog(t))

	// Refused because a role in an organization cannot carry an installation-wide
	// permission. Not the anti-escalation rule, which this fixture cannot trip: an
	// owner holds every organization permission there is. A genuine escalation
	// refusal is covered by TestCreatingARoleCannotGrantWhatTheCallerLacks.
	f.call(t, http.MethodPost, f.orgPath("/roles"),
		`{"key":"sneaky","name":"Sneaky","permissions":["platform.users.delete"]}`).
		expect(t, http.StatusUnprocessableEntity)

	// Refused by the last-owner rule.
	f.call(t, http.MethodDelete, f.orgPath("/members/"+f.Membership.String()), "").
		expect(t, http.StatusConflict)

	if got := len(f.auditLog(t)); got != before {
		t.Errorf("the log grew from %d to %d entries on requests that changed nothing",
			before, got)
	}
}

// TestTheAuditLogIsScopedToTheOrganization is the tenancy check on the history,
// which is the one read where a leak would hand over another customer's
// administrative activity.
func TestTheAuditLogIsScopedToTheOrganization(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	// Something happens in another organization.
	foreign := f.Repo.SeedOrganization("globex", "Globex")
	foreignMember := f.Repo.SeedMember(foreign, uuid.Must(uuid.NewV7()), ent.MembershipActive)
	_ = foreignMember

	f.call(t, http.MethodPatch, f.orgPath(""), `{"name":"Renamed"}`).expect(t, http.StatusOK)

	for _, event := range f.auditLog(t) {
		if event.Action == string(ent.ActionOrganizationCreated) && event.Detail == "globex" {
			t.Error("the organization's log names activity from another organization")
		}
	}
}

// TestReadingTheAuditLogNeedsThePermission keeps the history behind its own
// permission rather than behind "can administer".
func TestReadingTheAuditLogNeedsThePermission(t *testing.T) {
	f := NewAuthzFixture(t)

	// An administrator over roles and members, but not over the log.
	role := f.Repo.SeedRole(f.OrgID, "people_admin",
		string(authz.PermMembersRead),
		string(authz.PermRolesRead),
	)
	f.Repo.SeedMemberRoles(f.Membership, role)

	res := f.call(t, http.MethodGet, f.orgPath("/audit"), "").expect(t, http.StatusForbidden)

	body := DecodeProblem(t, res.body)
	if body.RequiredPermission != string(authz.PermAuditRead) {
		t.Errorf("required_permission = %q, want %q", body.RequiredPermission, authz.PermAuditRead)
	}
}
