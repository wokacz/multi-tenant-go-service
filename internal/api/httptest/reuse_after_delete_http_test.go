package httptest

import (
	"net/http"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// TestAnAddressWorksAgainAfterTheAccountIsDeleted is the dead end M9 closes.
//
// The unique index on users.email was plain, and accounts are soft deleted, so a
// deleted account held its address for ever. Registration hides a duplicate behind
// 204 — deliberately, so the status cannot be used to discover which addresses
// exist — which meant the person trying was told it had worked and then could never
// sign in. No error anywhere said why.
func TestAnAddressWorksAgainAfterTheAccountIsDeleted(t *testing.T) {
	f := NewAuthzFixture(t)
	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	const address = "pat@example.com"

	registerAccount(t, f, address)

	if got := signInStatus(t, f.Server, address, "twelve-chars"); got != http.StatusCreated {
		t.Fatalf("sign in after registering = %d, want 201", got)
	}

	patID := accountIDByEmail(t, f, address)
	f.call(t, http.MethodDelete, "/v1/platform/users/"+patID.String(), "").
		expect(t, http.StatusNoContent)

	// The deleted account cannot sign in, which is the point of deleting it.
	if got := signInStatus(t, f.Server, address, "twelve-chars"); got != http.StatusUnauthorized {
		t.Errorf("sign in with a deleted account = %d, want 401", got)
	}

	// And the address is free: registering it again produces a working account
	// rather than a silent 204 and a dead end.
	registerAccount(t, f, address)

	if got := signInStatus(t, f.Server, address, "twelve-chars"); got != http.StatusCreated {
		t.Errorf("sign in after re-registering = %d, want 201 — the address was still held", got)
	}
}

// TestASlugWorksAgainAfterTheOrganizationIsDeleted is the same rule for
// organizations, where the symptom was a 409 nobody could act on.
func TestASlugWorksAgainAfterTheOrganizationIsDeleted(t *testing.T) {
	f := NewAuthzFixture(t)
	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	const body = `{"slug":"globex","name":"Globex"}`

	var created struct {
		ID string `json:"id"`
	}
	f.call(t, http.MethodPost, "/v1/platform/organizations", body).
		expect(t, http.StatusCreated).decode(t, &created)

	// A second live one is still refused.
	f.call(t, http.MethodPost, "/v1/platform/organizations", body).
		expect(t, http.StatusConflict)

	f.call(t, http.MethodDelete, "/v1/platform/organizations/"+created.ID, "").
		expect(t, http.StatusNoContent)

	f.call(t, http.MethodPost, "/v1/platform/organizations", body).
		expect(t, http.StatusCreated)
}
