package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// TestAcceptingAnInvitationJoinsTheOrganization is the invitee's half of the
// flow. Until they accept, the membership confers nothing; afterwards they
// hold the roles they were invited with.
func TestAcceptingAnInvitationJoinsTheOrganization(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	invited := inviteBody(t, f, outsider, viewer)

	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodPost, "/v1/me/invitations/"+invited.ID.String()+"/accept", "", bobToken, ""))
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("accept = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	org := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", bobToken, ""))
	if org.Code != http.StatusOK {
		t.Fatalf("get org after accept = %d, want 200; body %s", org.Code, org.Body.Bytes())
	}

	var list struct {
		Members []memberDetail `json:"members"`
	}
	f.call(t, http.MethodGet, f.orgPath("/members"), "").
		expect(t, http.StatusOK).decode(t, &list)

	found := false
	for _, member := range list.Members {
		if member.Email != outsider {
			continue
		}

		found = true
		if member.Status != string(models.MembershipActive) {
			t.Errorf("status = %q, want active", member.Status)
		}

		if member.UserID == nil {
			t.Error("user_id is omitted after accept")
		}
	}

	if !found {
		t.Fatal("accepted member is missing from the list")
	}
}

func TestDecliningAnInvitationWithdrawsIt(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	invited := inviteBody(t, f, outsider, viewer)
	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodDelete, "/v1/me/invitations/"+invited.ID.String(), "", bobToken, ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("decline = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	org := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", bobToken, ""))
	if org.Code != http.StatusNotFound {
		t.Fatalf("get org after decline = %d, want 404", org.Code)
	}
}

// TestRegisteringDoesNotAcceptAnInvitation closes the hole where signing up
// *was* the accept.
//
// The address on a new account is never verified, so registering proves nothing
// about the mailbox. Accepting on its behalf handed whoever registered an
// invited address first the roles it carried, in an organization they had never
// been part of — and the real invitee could no longer register at all, because
// the address was taken.
func TestRegisteringDoesNotAcceptAnInvitation(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	const pending = "pending@example.com"
	invitation := inviteBody(t, f, pending, viewer)

	registerAccount(t, f, pending)
	token := signIn(t, f.server, pending, "twelve-chars")

	org := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", token, ""))
	if org.Code != http.StatusNotFound {
		t.Fatalf("get org after registering = %d, want 404 — registering is not an accept; body %s",
			org.Code, org.Body.Bytes())
	}

	// Refusing must not cost the invitee anything: the invitation is untouched,
	// so they can still take it up themselves.
	if entry := membershipIn(t, f, token, "acme"); entry.Status != string(models.MembershipInvited) {
		t.Errorf("status after registering = %q, want invited", entry.Status)
	}

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodPost, "/v1/me/invitations/"+invitation.ID.String()+"/accept", "", token, ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	org = do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", token, ""))
	if org.Code != http.StatusOK {
		t.Fatalf("get org after accepting = %d, want 200; body %s", org.Code, org.Body.Bytes())
	}
}

// TestRegisteringDoesNotClaimAnInvitationToTheDefaultOrganization covers the
// interleaving that makes removing the automatic accept insufficient on its own.
//
// Registration also joins the default organization, and that goes through
// AddMember, which collides with an outstanding invitation on
// (organization_id, email) and *claims* it — activating a membership nobody
// accepted and replacing the roles it was issued with by "member". So the
// invitation would still be accepted here, and quietly downgraded on the way.
func TestRegisteringDoesNotClaimAnInvitationToTheDefaultOrganization(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	// Registering Ada created the default organization together with the shipped
	// roles, so both of these are already there.
	defaultOrg, err := f.repo.OrganizationBySlug(t.Context(), models.DefaultOrganizationSlug)
	if err != nil {
		t.Fatalf("default organization: %v", err)
	}

	admin, err := f.repo.RoleByKey(t.Context(), defaultOrg.ID, string(authz.RoleAdmin))
	if err != nil {
		t.Fatalf("admin role: %v", err)
	}

	// Ada joined the default organization as a plain member, and inviting needs
	// members.invite.
	promoteInDefaultOrganization(t, f, defaultOrg.ID)

	const pending = "pending@example.com"
	body := fmt.Sprintf(`{"email":%q,"role_ids":[%q]}`, pending, admin.ID)

	var invitation memberDetail
	f.call(t, http.MethodPost, "/v1/orgs/"+defaultOrg.ID.String()+"/members", body).
		expect(t, http.StatusCreated).decode(t, &invitation)

	registerAccount(t, f, pending)
	token := signIn(t, f.server, pending, "twelve-chars")

	entry := membershipIn(t, f, token, models.DefaultOrganizationSlug)
	if entry.Status != string(models.MembershipInvited) {
		t.Errorf("status after registering = %q, want invited", entry.Status)
	}

	if !slices.Equal(entry.Roles, []string{string(authz.RoleAdmin)}) {
		t.Errorf("roles after registering = %v, want [admin] — claiming the invitation replaces them with member",
			entry.Roles)
	}

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodPost, "/v1/me/invitations/"+invitation.ID.String()+"/accept", "", token, ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	entry = membershipIn(t, f, token, models.DefaultOrganizationSlug)
	if entry.Status != string(models.MembershipActive) {
		t.Errorf("status after accepting = %q, want active", entry.Status)
	}

	if !slices.Equal(entry.Roles, []string{string(authz.RoleAdmin)}) {
		t.Errorf("roles after accepting = %v, want [admin]", entry.Roles)
	}
}

// TestAnInvitationToADeletedOrganizationCannotBeAccepted stops a row that would
// immediately start lying.
//
// Deleting an organization does not remove its invitations. Accepting one produced
// an active membership in an organization that no longer exists — harmless while
// every read joins to organizations and filters it out, and wrong the moment
// something counts memberships without that join.
func TestAnInvitationToADeletedOrganizationCannotBeAccepted(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	invited := inviteBody(t, f, outsider, viewer)
	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	f.repo.SeedSoftDeletedOrganization(f.orgID)

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodPost, "/v1/me/invitations/"+invited.ID.String()+"/accept", "", bobToken, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("accept into a deleted organization = %d, want 404; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestAForeignInvitationIsNotFound(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	invited := inviteBody(t, f, "stranger@example.com", viewer)

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodPost, "/v1/me/invitations/"+invited.ID.String()+"/accept", "", f.token, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("accept someone else's invitation = %d, want 404", rec.Code)
	}
}

func registerAccount(t *testing.T, f *authzFixture, email string) {
	t.Helper()

	body := fmt.Sprintf(
		`{"name":"Pat","email":%q,"password":"twelve-chars","password_confirm":"twelve-chars"}`, email)
	if rec := postJSON(t, f.server.http.Handler, "/v1/users", body); rec.Code != http.StatusNoContent {
		t.Fatalf("register %s = %d; body %s", email, rec.Code, rec.Body.Bytes())
	}
}

// promoteInDefaultOrganization gives the fixture's account the owner role in the
// default organization, which registration only put it in as a plain member.
func promoteInDefaultOrganization(t *testing.T, f *authzFixture, defaultOrgID uuid.UUID) {
	t.Helper()

	owner, err := f.repo.RoleByKey(t.Context(), defaultOrgID, string(authz.RoleOwner))
	if err != nil {
		t.Fatalf("owner role: %v", err)
	}

	memberships, err := f.repo.MembershipsForUser(t.Context(), f.userID)
	if err != nil {
		t.Fatalf("memberships: %v", err)
	}

	for i := range memberships {
		if memberships[i].Organization.ID == defaultOrgID {
			f.repo.SeedMemberRoles(memberships[i].ID, owner.ID)

			return
		}
	}

	t.Fatal("the account that registered is not in the default organization")
}

// myOrganization is one entry of /v1/me/organizations — the only view an invitee
// has of an organization before they accept, and therefore the only place a test
// can see an invitation's status and roles without a permission in it.
type myOrganization struct {
	ID           uuid.UUID `json:"id"`
	Organization struct {
		ID   uuid.UUID `json:"id"`
		Slug string    `json:"slug"`
	} `json:"organization"`
	Status string   `json:"status"`
	Roles  []string `json:"roles"`
}

func membershipIn(t *testing.T, f *authzFixture, token, slug string) myOrganization {
	t.Helper()

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/me/organizations", "", token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list my organizations = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	var body struct {
		Organizations []myOrganization `json:"organizations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.Bytes())
	}

	for _, entry := range body.Organizations {
		if entry.Organization.Slug == slug {
			return entry
		}
	}

	t.Fatalf("organization %q is missing from the caller's own list; body %s", slug, rec.Body.Bytes())

	return myOrganization{}
}

func signIn(t *testing.T, s *Server, email, password string) string {
	t.Helper()

	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	rec := do(t, s.http.Handler, withDeviceToken(request(t, http.MethodPost, "/v1/sessions", body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign in %s = %d; body %s", email, rec.Code, rec.Body.Bytes())
	}

	var session sessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	return session.Token
}
