package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// acceptToken posts a token to the accept endpoint and returns the raw result, so
// each test can say what it expects.
func acceptToken(t *testing.T, s *Server, token, bearer string) *httpResult {
	t.Helper()

	body := fmt.Sprintf(`{"token":%q}`, token)
	rec := do(t, s.http.Handler, authed(t, http.MethodPost, "/v1/me/invitations/accept", body, bearer, ""))

	return &httpResult{code: rec.Code, body: rec.Body.Bytes()}
}

func declineToken(t *testing.T, s *Server, token, bearer string) *httpResult {
	t.Helper()

	body := fmt.Sprintf(`{"token":%q}`, token)
	rec := do(t, s.http.Handler, authed(t, http.MethodPost, "/v1/me/invitations/decline", body, bearer, ""))

	return &httpResult{code: rec.Code, body: rec.Body.Bytes()}
}

// TestAcceptingAnInvitationJoinsTheOrganization is the invitee's half of the flow.
//
// The token is what proves they received the offer, and the roles come from the
// invitation rather than from the request — somebody accepting must not get to
// choose what they are accepting.
func TestAcceptingAnInvitationJoinsTheOrganization(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	inviteBody(t, f, outsider, viewer)

	// The token exists only in the message.
	token := f.mailer.inviteToken
	if token == "" {
		t.Fatal("no token was mailed")
	}

	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	// Before accepting, the organization is not theirs.
	before := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", bobToken, ""))
	if before.Code != http.StatusNotFound {
		t.Fatalf("get org before accepting = %d, want 404", before.Code)
	}

	acceptToken(t, f.server, token, bobToken).expect(t, http.StatusNoContent)

	after := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", bobToken, ""))
	if after.Code != http.StatusOK {
		t.Fatalf("get org after accepting = %d, want 200; body %s", after.Code, after.Body.Bytes())
	}

	// The membership carries the invited role, and the invitation is spent.
	entry := membershipIn(t, f, bobToken, "acme")
	if !slices.Equal(entry.Roles, []string{string(authz.RoleViewer)}) {
		t.Errorf("roles = %v, want [viewer] — the roles come from the invitation", entry.Roles)
	}

	acceptToken(t, f.server, token, bobToken).expect(t, http.StatusNotFound)
}

// TestAnInvitationCannotBeAcceptedByAnotherAccount: the token proves the
// mailbox, the address says whose mailbox was meant.
//
// It is a 409 naming the reason rather than a 404. The caller is holding the token,
// so the invitation's existence is not a secret from them, and a bare "not found"
// would leave them with nothing to tell whoever invited them.
func TestAnInvitationCannotBeAcceptedByAnotherAccount(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	// Issued to an address nobody holds, then presented by Ada.
	inviteBody(t, f, "somebody.else@example.com", viewer)

	res := acceptToken(t, f.server, f.mailer.inviteToken, f.token).expect(t, http.StatusConflict)

	var doc problemBody
	res.decode(t, &doc)

	if doc.Code != "invitation_address_mismatch" {
		t.Errorf("code = %q, want invitation_address_mismatch", doc.Code)
	}
}

// TestAnExpiredInvitationIsGone separates "ran out" from "never existed".
//
// The holder of the token can act on an expiry — ask for another invitation — and a
// 404 would send them looking for a mistake they did not make.
func TestAnExpiredInvitationIsGone(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	const token = "an-expired-invitation-token"
	f.repo.SeedInvitation(f.orgID, outsider, token, time.Now().UTC().Add(-time.Hour), viewer)

	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	res := acceptToken(t, f.server, token, bobToken).expect(t, http.StatusGone)

	var doc problemBody
	res.decode(t, &doc)

	if doc.Code != "invitation_expired" {
		t.Errorf("code = %q, want invitation_expired", doc.Code)
	}

	// A token that was never issued is a plain 404: there is nothing to act on.
	acceptToken(t, f.server, "no-such-token-at-all", bobToken).expect(t, http.StatusNotFound)
}

func TestDecliningAnInvitationWithdrawsIt(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	inviteBody(t, f, outsider, viewer)
	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	declineToken(t, f.server, f.mailer.inviteToken, bobToken).expect(t, http.StatusNoContent)

	// Gone for good: the same token cannot then be accepted.
	acceptToken(t, f.server, f.mailer.inviteToken, bobToken).expect(t, http.StatusNotFound)

	org := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", bobToken, ""))
	if org.Code != http.StatusNotFound {
		t.Fatalf("get org after declining = %d, want 404", org.Code)
	}
}

// TestMyInvitationsCarriesNoToken keeps the list from becoming a second way to
// accept.
//
// It exists so an offer is visible somewhere in the product — somebody who deleted
// the message can see one is open and ask again — but reading it must not be enough
// to take it up, or storing a hash instead of the token would have bought nothing.
func TestMyInvitationsCarriesNoToken(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	inviteBody(t, f, outsider, viewer)
	bobToken := signIn(t, f.server, outsider, "twelve-chars")

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/me/invitations", "", bobToken, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body %s", rec.Code, rec.Body.Bytes())
	}

	if strings.Contains(rec.Body.String(), f.mailer.inviteToken) {
		t.Error("the listing contains the token")
	}

	var out struct {
		Invitations []struct {
			Organization struct {
				Slug string `json:"slug"`
			} `json:"organization"`
			Email string   `json:"email"`
			Roles []string `json:"roles"`
		} `json:"invitations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.Invitations) != 1 || out.Invitations[0].Organization.Slug != "acme" {
		t.Fatalf("invitations = %+v, want the one offer to acme", out.Invitations)
	}

	if !slices.Equal(out.Invitations[0].Roles, []string{string(authz.RoleViewer)}) {
		t.Errorf("roles = %v, want [viewer]", out.Invitations[0].Roles)
	}
}

// TestRegisteringDoesNotAcceptAnInvitation is C1, now closed by the model rather
// than by a rule.
//
// The address used to be the invitation's identity, so whoever registered it first
// inherited the offer and its roles. Registering now proves nothing about the
// mailbox and there is nothing to inherit: the offer is reachable only through the
// token that was mailed.
func TestRegisteringDoesNotAcceptAnInvitation(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	const pending = "pending@example.com"
	inviteBody(t, f, pending, viewer)

	registerAccount(t, f, pending)
	token := signIn(t, f.server, pending, "twelve-chars")

	org := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", token, ""))
	if org.Code != http.StatusNotFound {
		t.Fatalf("get org after registering = %d, want 404 — registering is not an accept; body %s",
			org.Code, org.Body.Bytes())
	}

	// And it costs the invitee nothing: the token still works.
	acceptToken(t, f.server, f.mailer.inviteToken, token).expect(t, http.StatusNoContent)

	org = do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", token, ""))
	if org.Code != http.StatusOK {
		t.Fatalf("get org after accepting = %d, want 200; body %s", org.Code, org.Body.Bytes())
	}
}

// TestAnInvitationToTheDefaultOrganizationSurvivesRegistration is the interleaving
// that used to silently downgrade an offer.
//
// Registration joins the default organization, and accepting creates the membership
// the invitation promised — so joining first would make the offer impossible to
// take up, and the invitee would end up with "member" instead of what they were
// actually offered. Registration therefore leaves the default organization alone
// while an invitation to it is open.
func TestAnInvitationToTheDefaultOrganizationSurvivesRegistration(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	defaultOrg, err := f.repo.OrganizationBySlug(t.Context(), ent.DefaultOrganizationSlug)
	if err != nil {
		t.Fatalf("default organization: %v", err)
	}

	admin, err := f.repo.RoleByKey(t.Context(), defaultOrg.ID, string(authz.RoleAdmin))
	if err != nil {
		t.Fatalf("admin role: %v", err)
	}

	promoteInDefaultOrganization(t, f, defaultOrg.ID)

	const pending = "pending@example.com"
	body := fmt.Sprintf(`{"email":%q,"role_ids":[%q]}`, pending, admin.ID)
	f.call(t, http.MethodPost, "/v1/orgs/"+defaultOrg.ID.String()+"/members", body).
		expect(t, http.StatusCreated)

	registerAccount(t, f, pending)
	token := signIn(t, f.server, pending, "twelve-chars")

	// No membership yet, so the offer is still takeable.
	acceptToken(t, f.server, f.mailer.inviteToken, token).expect(t, http.StatusNoContent)

	entry := membershipIn(t, f, token, ent.DefaultOrganizationSlug)
	if entry.Status != string(ent.MembershipActive) {
		t.Errorf("status = %q, want active", entry.Status)
	}

	if !slices.Equal(entry.Roles, []string{string(authz.RoleAdmin)}) {
		t.Errorf("roles = %v, want [admin] — joining first would have made this member", entry.Roles)
	}
}

// TestTheInvitationMailNamesTheAddress is what makes the address rule survivable.
//
// Accepting needs an account with the address the offer was issued to, so somebody
// reading the message in a forwarded mailbox has to be told which address that is,
// or the refusal is unexplainable.
func TestTheInvitationMailNamesTheAddress(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	const invitee = "pat@example.com"
	inviteBody(t, f, invitee, viewer)

	if f.mailer.inviteTo != invitee {
		t.Errorf("mail went to %q, want %q", f.mailer.inviteTo, invitee)
	}

	if f.mailer.inviteExpires.IsZero() {
		t.Error("the mail carries no expiry, so the invitee cannot tell how long they have")
	}

	if f.mailer.inviteExpires.Before(time.Now().UTC()) {
		t.Errorf("expiry %v is already past", f.mailer.inviteExpires)
	}

	// The default is long enough to survive a holiday and short enough that a
	// forwarded mailbox does not stay dangerous for ever.
	if got := time.Until(f.mailer.inviteExpires); got > orgs.InvitationTTL+time.Minute {
		t.Errorf("expiry is %v away, want no more than %v", got, orgs.InvitationTTL)
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

// myOrganization is one entry of /v1/me/organizations.
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

// TestWithdrawingAnInvitationNeedsTheInvitePermission pins where the third step of
// the invitation lifecycle sits.
//
// It used to need members.remove — the only step of the three that did — which made
// fixing a typo in an address cost more than making it, and gave one lifecycle two
// different permissions. members.remove now means one thing: take somebody's access
// away. Cancelling an offer takes nothing away, because nobody has anything yet.
//
// Both directions are asserted. A test that only checked the new permission would
// still pass if the rule kept accepting the old one as well.
func TestWithdrawingAnInvitationNeedsTheInvitePermission(t *testing.T) {
	cases := map[string]struct {
		permission authz.Permission
		want       int
	}{
		"members.invite may withdraw": {authz.PermMembersInvite, http.StatusNoContent},
		"members.remove may not":      {authz.PermMembersRemove, http.StatusForbidden},
		"members.read may not":        {authz.PermMembersRead, http.StatusForbidden},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAuthzFixture(t)

			role := f.repo.SeedRole(f.orgID, "probe_role", string(tc.permission))
			f.repo.SeedMemberRoles(f.membership, role)

			invitation := f.repo.SeedInvitation(f.orgID, "bo@example.com", "a-withdrawn-token",
				time.Now().UTC().Add(orgs.InvitationTTL))

			f.call(t, http.MethodDelete,
				f.orgPath("/invitations/"+invitation.String()), "").
				expect(t, tc.want)
		})
	}
}
