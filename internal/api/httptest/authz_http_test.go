package httptest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// TestAGatedOperationRefusesAMemberWithoutThePermission is the core of the
// scheme: a member of the organization, signed in, with a role that does not
// carry organization.read.
func TestAGatedOperationRefusesAMemberWithoutThePermission(t *testing.T) {
	f := NewAuthzFixture(t)

	// A role holding something else entirely, so the membership is real but the
	// permission is not.
	role := f.Repo.SeedRole(f.OrgID, "auditors", string(authz.PermMembersRead))
	f.Repo.SeedMemberRoles(f.Membership, role)

	rec := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/orgs/"+f.OrgID.String(), "", f.Token, ""))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.Bytes())
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", ct)
	}

	body := DecodeProblem(t, rec.Body.Bytes())

	// The raw key goes in the structured field so a client can look it up in the
	// permission catalog; the prose gets the translated name, which is why the
	// assertion is not on Detail.
	if body.RequiredPermission != string(authz.PermOrganizationRead) {
		t.Errorf("required_permission = %q, want %q",
			body.RequiredPermission, authz.PermOrganizationRead)
	}

	if body.Code != "forbidden_requires" {
		t.Errorf("code = %q, want a stable machine-readable code", body.Code)
	}
}

func TestAGatedOperationAllowsAMemberWhoHoldsThePermission(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	rec := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/orgs/"+f.OrgID.String(), "", f.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.Bytes())
	}

	var org struct {
		ID   uuid.UUID `json:"id"`
		Slug string    `json:"slug"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &org); err != nil {
		t.Fatalf("decode organization: %v", err)
	}

	if org.ID != f.OrgID {
		t.Errorf("id = %v, want %v", org.ID, f.OrgID)
	}

	if org.Slug != "acme" {
		t.Errorf("slug = %q, want %q", org.Slug, "acme")
	}
}

// TestAForeignOrganizationIs404NotForbidden is the enumeration guard. A 403
// here would confirm the organization exists to anyone willing to try
// identifiers, which in a multi-tenant installation is a customer list.
func TestAForeignOrganizationIs404NotForbidden(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	// A real organization Ada has nothing to Do with, and one that never existed.
	foreign := f.Repo.SeedOrganization("globex", "Globex")

	for name, id := range map[string]uuid.UUID{
		"an organization the caller is not in": foreign,
		"an organization that does not exist":  uuid.Must(uuid.NewV7()),
	} {
		t.Run(name, func(t *testing.T) {
			rec := Do(t, f.Server.Handler(),
				Authed(t, http.MethodGet, "/v1/orgs/"+id.String(), "", f.Token, ""))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body %s", rec.Code, rec.Body.Bytes())
			}

			// The two answers must also be byte-identical, or the difference is
			// still readable.
			body := DecodeProblem(t, rec.Body.Bytes())
			if body.Detail != "not found" || body.Code != "not_found" {
				t.Errorf("body = %+v, want the same opaque answer both cases produce", body)
			}

			if body.RequiredPermission != "" {
				t.Errorf("required_permission = %q on a 404; naming it would confirm the "+
					"organization exists", body.RequiredPermission)
			}
		})
	}
}

// TestSuspendingAMemberTakesEffectOnTheNextRequest is why permissions are not
// carried in the token. Nothing is re-issued and no session is touched.
func TestSuspendingAMemberTakesEffectOnTheNextRequest(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	if rec := f.GetOrg(t); rec != http.StatusOK {
		t.Fatalf("status before suspension = %d, want 200", rec)
	}

	f.Repo.SeedMemberStatus(f.Membership, ent.MembershipSuspended)

	if rec := f.GetOrg(t); rec != http.StatusNotFound {
		t.Fatalf("status after suspension = %d, want 404", rec)
	}
}

// TestRevokingARoleTakesEffectOnTheNextRequest is the same guarantee for the
// lighter action, and lands on 403 rather than 404 because the membership is
// still there.
func TestRevokingARoleTakesEffectOnTheNextRequest(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	if rec := f.GetOrg(t); rec != http.StatusOK {
		t.Fatalf("status before revoking = %d, want 200", rec)
	}

	f.Repo.SeedMemberRoles(f.Membership)

	if rec := f.GetOrg(t); rec != http.StatusForbidden {
		t.Fatalf("status after revoking = %d, want 403", rec)
	}
}

// TestAGatedOperationWithoutASessionIs401 keeps the two refusals distinct. A
// 403 for a caller carrying no token would tell them their credentials were
// accepted, and leaves generic HTTP tooling with nothing to retry against.
func TestAGatedOperationWithoutASessionIs401(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	rec := Do(t, f.Server.Handler(),
		Request(t, http.MethodGet, "/v1/orgs/"+f.OrgID.String(), ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.Bytes())
	}

	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 has no WWW-Authenticate header")
	}
}

// TestAMalformedOrganizationIDIsRejectedBeforeAnyLookup covers the parse in the
// middleware. It runs before huma validates the handler's input, so this is the
// only thing standing between a junk path segment and the decision.
func TestAMalformedOrganizationIDIsRejectedBeforeAnyLookup(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	rec := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/orgs/not-a-uuid", "", f.Token, ""))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.Bytes())
	}
}

// TestSelfServiceSurvivesHavingNoRolesAtAll is the lockout guard, exercised end
// to end: an account with no membership and no roles anywhere must still be
// able to read its own profile, list its devices and sign itself out.
func TestSelfServiceSurvivesHavingNoRolesAtAll(t *testing.T) {
	mailer := &CapturingMailer{}
	s, _, _ := NewTestAPIConfig(t, mailer, memory.NewUsers(), nil)

	RegisterAda(t, s)
	session := SignInAda(t, s, "", http.StatusCreated)

	for _, path := range []string{
		"/v1/me",
		"/v1/me/devices",
		"/v1/me/login-events",
		"/v1/me/organizations",
	} {
		t.Run(path, func(t *testing.T) {
			rec := Do(t, s.Handler(), Authed(t, http.MethodGet, path, "", session.Token, ""))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200; body %s", rec.Code, rec.Body.Bytes())
			}
		})
	}
}

// TestMyOrganizationsShowsSuspensions checks that the list a client renders does
// not silently drop the state a person most needs to see.
//
// An invitation used to appear here too, with status "invited". It is not a
// membership any more and has its own listing at GET /v1/me/invitations — being in
// both would have meant two answers to "which organizations am I in".
func TestMyOrganizationsShowsSuspensions(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	suspended := f.Repo.SeedOrganization("gamma", "Gamma")
	f.Repo.SeedMember(suspended, f.UserID, ent.MembershipSuspended)

	rec := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/me/organizations", "", f.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.Bytes())
	}

	var body struct {
		Organizations []struct {
			Organization struct {
				Slug string `json:"slug"`
			} `json:"organization"`
			Status string   `json:"status"`
			Roles  []string `json:"roles"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.Bytes())
	}

	got := map[string]string{}
	for _, entry := range body.Organizations {
		got[entry.Organization.Slug] = entry.Status
	}

	want := map[string]string{
		"acme":  string(ent.MembershipActive),
		"gamma": string(ent.MembershipSuspended),
	}

	for slug, status := range want {
		if got[slug] != status {
			t.Errorf("organization %q has status %q, want %q", slug, got[slug], status)
		}
	}
}

// TestAnOrganizationTheCallerCannotReadIsNotListed is the other side: the list
// is scoped to memberships, so it cannot become a directory of every tenant.
func TestAnOrganizationTheCallerCannotReadIsNotListed(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	f.Repo.SeedOrganization("globex", "Globex")

	rec := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/me/organizations", "", f.Token, ""))

	if strings.Contains(rec.Body.String(), "globex") {
		t.Errorf("the caller's organization list names one they Do not belong to: %s", rec.Body.Bytes())
	}
}
