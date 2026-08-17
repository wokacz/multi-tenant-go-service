package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

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

func TestRegistrationAcceptsPendingInvitations(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	const pending = "pending@example.com"
	inviteBody(t, f, pending, viewer)

	body := fmt.Sprintf(
		`{"name":"Pat","email":%q,"password":"twelve-chars","password_confirm":"twelve-chars"}`,
		pending)
	if rec := postJSON(t, f.server.http.Handler, "/v1/users", body); rec.Code != http.StatusNoContent {
		t.Fatalf("register = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	token := signIn(t, f.server, pending, "twelve-chars")
	org := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", token, ""))
	if org.Code != http.StatusOK {
		t.Fatalf("get org after register = %d, want 200; body %s", org.Code, org.Body.Bytes())
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
