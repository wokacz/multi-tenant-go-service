package httptest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// TestAnOrganizationCreatedByThePlatformCanBeGivenAnOwner is the H2 story end to
// end, and the gap it closes.
//
// create-platform-organization deliberately leaves the new organization empty, and
// adding a member needs a permission inside it — which a platform administrator does
// not have, by design. So an organization made through the API had nobody in it and
// no way to get anybody: the only way out was SQL.
func TestAnOrganizationCreatedByThePlatformCanBeGivenAnOwner(t *testing.T) {
	f := NewAuthzFixture(t)
	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	// Somebody to put in charge, who is not the caller.
	outsider := registerOutsider(t, f)
	outsiderID := accountIDByEmail(t, f, outsider)

	var created struct {
		ID uuid.UUID `json:"id"`
	}
	f.call(t, http.MethodPost, "/v1/platform/organizations", `{"slug":"globex","name":"Globex"}`).
		expect(t, http.StatusCreated).decode(t, &created)

	// Empty, and the platform admin has no standing inside it — that is the design,
	// and it is what made the organization unusable.
	if code := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/orgs/"+created.ID.String(), "", f.Token, "")).Code; code != http.StatusNotFound {
		t.Errorf("the platform admin reads the new organization = %d, want 404", code)
	}

	body := fmt.Sprintf(`{"user_id":%q}`, outsiderID)
	f.call(t, http.MethodPost, "/v1/platform/organizations/"+created.ID.String()+"/owners", body).
		expect(t, http.StatusNoContent)

	// The appointed owner can now administer it, which is the whole point.
	ownerToken := signIn(t, f.Server, outsider, "twelve-chars")

	if code := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/orgs/"+created.ID.String(), "", ownerToken, "")).Code; code != http.StatusOK {
		t.Fatalf("the appointed owner reads the organization = %d, want 200", code)
	}

	// Including inviting somebody, which is the permission that was unreachable.
	invite := Do(t, f.Server.Handler(), Authed(t, http.MethodPost,
		"/v1/orgs/"+created.ID.String()+"/members", `{"email":"someone@example.com","role_ids":[]}`,
		ownerToken, ""))
	if invite.Code != http.StatusCreated {
		t.Errorf("the owner invites somebody = %d, want 201; body %s", invite.Code, invite.Body.Bytes())
	}

	// Appointing does not hand out the installation-wide role: owning one
	// organization and administering the installation are separate authorizations.
	if code := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/platform/organizations", "", ownerToken, "")).Code; code != http.StatusForbidden {
		t.Errorf("the new owner reaches the platform listing = %d, want 403", code)
	}
}

// TestAppointingAnOwnerIsAudited puts the change in the organization's own log,
// where whoever runs that organization will look for it.
func TestAppointingAnOwnerIsAudited(t *testing.T) {
	f := NewAuthzFixture(t)
	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	target := f.Repo.SeedOrganization("globex", "Globex")
	f.Repo.SeedShippedRole(target, authz.RoleOwner)

	outsider := registerOutsider(t, f)
	outsiderID := accountIDByEmail(t, f, outsider)

	body := fmt.Sprintf(`{"user_id":%q}`, outsiderID)
	f.call(t, http.MethodPost, "/v1/platform/organizations/"+target.String()+"/owners", body).
		expect(t, http.StatusNoContent)

	// Read from the installation-wide log: the caller is not in that organization,
	// so they cannot read its own audit endpoint — which is the design working.
	var log struct {
		Events []auditEventBody `json:"events"`
	}
	f.call(t, http.MethodGet, "/v1/platform/audit", "").
		expect(t, http.StatusOK).decode(t, &log)

	if !hasEvent(log.Events, string(ent.ActionMemberJoined), f.UserID, outsiderID) {
		t.Errorf("no %s entry attributing the appointment to the caller; the log has %+v",
			ent.ActionMemberJoined, log.Events)
	}
}

// TestAppointingAnOwnerRefusesWhatDoesNotExist keeps the two ids from producing a
// 500 out of an untranslated driver error.
func TestAppointingAnOwnerRefusesWhatDoesNotExist(t *testing.T) {
	f := NewAuthzFixture(t)
	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	target := f.Repo.SeedOrganization("globex", "Globex")
	f.Repo.SeedShippedRole(target, authz.RoleOwner)

	// An account that does not exist.
	body := fmt.Sprintf(`{"user_id":%q}`, uuid.Must(uuid.NewV7()))
	f.call(t, http.MethodPost, "/v1/platform/organizations/"+target.String()+"/owners", body).
		expect(t, http.StatusNotFound)

	// An organization that does not exist.
	outsiderID := accountIDByEmail(t, f, registerOutsider(t, f))
	body = fmt.Sprintf(`{"user_id":%q}`, outsiderID)
	f.call(t, http.MethodPost, "/v1/platform/organizations/"+uuid.Must(uuid.NewV7()).String()+"/owners", body).
		expect(t, http.StatusNotFound)

	// And a deleted one: its roles outlive the soft delete, so without the explicit
	// check an owner could be appointed to an organization that is gone.
	f.Repo.SeedSoftDeletedOrganization(target)
	f.call(t, http.MethodPost, "/v1/platform/organizations/"+target.String()+"/owners", body).
		expect(t, http.StatusNotFound)
}

// accountIDByEmail finds an account through the platform listing, which is the only
// place a caller can see somebody else's id.
func accountIDByEmail(t *testing.T, f *AuthzFixture, email string) uuid.UUID {
	t.Helper()

	var out struct {
		Users []struct {
			ID    uuid.UUID `json:"id"`
			Email string    `json:"email"`
		} `json:"users"`
	}
	f.call(t, http.MethodGet, "/v1/platform/users", "").
		expect(t, http.StatusOK).decode(t, &out)

	for _, u := range out.Users {
		if u.Email == email {
			return u.ID
		}
	}

	t.Fatalf("account %q is missing from the platform listing: %+v", email, out.Users)

	return uuid.Nil
}
