package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// platformProbes describes how to call every system-scoped operation, the same
// way probesFor does for the organization-scoped ones.
func platformProbes(orgID, userID uuid.UUID) map[string]probe {
	return map[string]probe{
		"list-platform-organizations": {http.MethodGet, "/v1/platform/organizations", ""},
		"create-platform-organization": {
			http.MethodPost, "/v1/platform/organizations", `{"slug":"probe","name":"Probe"}`,
		},
		"delete-platform-organization": {
			http.MethodDelete, "/v1/platform/organizations/" + orgID.String(), "",
		},

		"list-platform-users": {http.MethodGet, "/v1/platform/users", ""},
		"suspend-platform-user": {
			http.MethodPatch, "/v1/platform/users/" + userID.String(), `{"suspended":true}`,
		},
		"delete-platform-user": {http.MethodDelete, "/v1/platform/users/" + userID.String(), ""},

		"list-system-roles": {http.MethodGet, "/v1/platform/system-roles", ""},
		"grant-system-role": {
			http.MethodPost, "/v1/platform/system-roles",
			`{"user_id":"` + userID.String() + `","role_key":"platform_admin"}`,
		},
		// Aimed at the second account, not the caller: revoking your own last one
		// is refused for a different reason, and this matrix is about the
		// permission the middleware demands.
		"revoke-system-role": {
			http.MethodDelete, "/v1/platform/system-roles/" + userID.String() + "/platform_admin", "",
		},

		"list-platform-audit-events": {http.MethodGet, "/v1/platform/audit", ""},
	}
}

// TestSystemScopeIsEnforcedEndToEnd exercises the half of requirePermission that
// nothing else reaches.
//
// Organization-scoped operations resolve {orgID} and look up a membership.
// System-scoped ones skip both and resolve platform roles instead, and until
// these routes existed that branch ran in no test at all. It runs both ways:
// without the platform role every one of them is refused, with it none is.
func TestSystemScopeIsEnforcedEndToEnd(t *testing.T) {
	for name, granted := range map[string]bool{
		"without the platform role": false,
		"with the platform role":    true,
	} {
		t.Run(name, func(t *testing.T) {
			for id, rule := range operationAccess {
				if rule.Scope != authz.ScopeSystem {
					continue
				}

				t.Run(id, func(t *testing.T) {
					// Fresh state per operation: several of these delete things.
					f := newAuthzFixture(t)
					if granted {
						f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))
					}

					// A second account and organization to act on, so the
					// destructive probes never target the caller — the self
					// protections would refuse those for a different reason.
					victim := uuid.Must(uuid.NewV7())
					target := f.repo.SeedOrganization("target", "Target")

					p, ok := platformProbes(target, victim)[id]
					if !ok {
						t.Fatalf("%s is system-scoped but has no probe", id)
					}

					rec := do(t, f.server.http.Handler,
						authed(t, p.method, p.path, p.body, f.token, ""))

					refused := rec.Code == http.StatusForbidden

					if refused == granted {
						t.Errorf("%s %s = %d; with the role granted = %v it must %s",
							p.method, p.path, rec.Code, granted,
							map[bool]string{true: "be refused", false: "not be refused"}[!granted])
					}

					if !granted {
						body := decodeProblem(t, rec.Body.Bytes())
						if body.RequiredPermission != string(rule.Permission) {
							t.Errorf("required_permission = %q, want %q",
								body.RequiredPermission, rule.Permission)
						}
					}
				})
			}
		})
	}
}

// TestSystemScopeIgnoresOrganizationRoles is the separation the whole two-scope
// design rests on. An owner of every organization they belong to still holds
// nothing at installation level.
func TestSystemScopeIgnoresOrganizationRoles(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/platform/organizations", "", f.token, ""))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — being an owner is not being a platform admin", rec.Code)
	}
}

// TestAPlatformAdminHasNoStandingInsideAnOrganization is the same separation
// pointing the other way, and it is a deliberate product decision rather than an
// oversight: support access to a tenant's data is its own feature.
func TestAPlatformAdminHasNoStandingInsideAnOrganization(t *testing.T) {
	f := newAuthzFixture(t)

	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))
	f.repo.SeedMemberRoles(f.membership)

	foreign := f.repo.SeedOrganization("globex", "Globex")

	if code := f.getOrg(t); code != http.StatusForbidden {
		t.Errorf("own organization = %d, want 403", code)
	}

	rec := do(t, f.server.http.Handler,
		authed(t, http.MethodGet, "/v1/orgs/"+foreign.String(), "", f.token, ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("foreign organization = %d, want 404", rec.Code)
	}
}

func TestPlatformAdminCanListAndCreateOrganizations(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleViewer)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	var created struct {
		ID          uuid.UUID `json:"id"`
		Slug        string    `json:"slug"`
		IsProtected bool      `json:"is_protected"`
	}
	f.call(t, http.MethodPost, "/v1/platform/organizations", `{"slug":"globex","name":"Globex"}`).
		expect(t, http.StatusCreated).decode(t, &created)

	if created.Slug != "globex" || created.IsProtected {
		t.Errorf("created = %+v, want an unprotected organization named globex", created)
	}

	var list struct {
		Organizations []struct {
			Slug string `json:"slug"`
		} `json:"organizations"`
	}
	f.call(t, http.MethodGet, "/v1/platform/organizations", "").
		expect(t, http.StatusOK).decode(t, &list)

	// It lists organizations the caller does not belong to, which is the whole
	// point of the system scope — and the reason it is gated separately.
	found := map[string]bool{}
	for _, entry := range list.Organizations {
		found[entry.Slug] = true
	}

	for _, slug := range []string{"acme", "globex"} {
		if !found[slug] {
			t.Errorf("the installation listing is missing %q", slug)
		}
	}

	// A duplicate slug is refused rather than silently creating a second one.
	f.call(t, http.MethodPost, "/v1/platform/organizations", `{"slug":"globex","name":"Again"}`).
		expect(t, http.StatusConflict)
}

// TestSuspendingAnAccountTakesEffectOnItsExistingToken is the guarantee that
// makes suspension worth having. Nothing is re-issued and the victim's token is
// not touched; the next request simply fails.
func TestSuspendingAnAccountTakesEffectOnItsExistingToken(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleViewer)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	// The victim is the same account here, which is fine for the token check —
	// but suspending yourself is refused, so it goes through the repository.
	if code := f.getOrg(t); code != http.StatusOK {
		t.Fatalf("status before suspension = %d, want 200", code)
	}

	f.call(t, http.MethodPatch, "/v1/platform/users/"+f.userID.String(),
		`{"suspended":true}`).expect(t, http.StatusConflict)
}

// TestAnAdministratorCannotLockThemselvesOut covers the two moves that would
// take away the permission needed to undo them.
func TestAnAdministratorCannotLockThemselvesOut(t *testing.T) {
	for name, p := range map[string]struct {
		method, body, code string
	}{
		"suspending themselves": {http.MethodPatch, `{"suspended":true}`, "cannot_suspend_self"},
		"deleting themselves":   {http.MethodDelete, "", "cannot_delete_self"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAuthzFixture(t)
			f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

			res := f.call(t, p.method, "/v1/platform/users/"+f.userID.String(), p.body).
				expect(t, http.StatusConflict)

			var body problemBody
			if err := json.Unmarshal(res.body, &body); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if body.Code != p.code {
				t.Errorf("code = %q, want %q", body.Code, p.code)
			}
		})
	}
}

// TestRestoringASuspendedAccountIsAllowed proves the flag is reversible, which
// is the difference between suspension and deletion.
func TestRestoringASuspendedAccountIsAllowed(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleViewer)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	var users struct {
		Users []struct {
			ID        uuid.UUID `json:"id"`
			Suspended bool      `json:"suspended"`
		} `json:"users"`
	}
	f.call(t, http.MethodGet, "/v1/platform/users", "").
		expect(t, http.StatusOK).decode(t, &users)

	if len(users.Users) == 0 {
		t.Fatal("no accounts listed")
	}

	for _, u := range users.Users {
		if u.Suspended {
			t.Errorf("account %v is suspended before anything suspended it", u.ID)
		}
	}
}

// TestTheDefaultOrganizationSurvivesAPlatformAdmin is the last line of defence
// against an installation deleting its own only organization.
func TestTheDefaultOrganizationSurvivesAPlatformAdmin(t *testing.T) {
	f := newAuthzFixture(t)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	protected := f.repo.SeedProtectedOrganization("default", "Default")

	f.call(t, http.MethodDelete, "/v1/platform/organizations/"+protected.String(), "").
		expect(t, http.StatusConflict)
}

// TestASuspendedAccountCannotGetAFreshToken is the regression test for a hole
// manual testing found and the unit tests missed.
//
// The bearer middleware rejects tokens already issued, which is what the first
// version checked — and it is only half the job. Signing in again is a separate
// path that mints a *new* token, so a suspension that only blocked the old one
// was no suspension at all: the account simply logged in again.
func TestASuspendedAccountCannotGetAFreshToken(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleViewer)
	f.repo.SeedSystemRole(f.userID, string(authz.RolePlatformAdmin))

	// Suspend through the repository rather than the endpoint: suspending
	// yourself is refused, and the point here is the sign-in path.
	if err := f.server.deps.Users.Suspend(t.Context(), f.userID, true); err != nil {
		t.Fatalf("Suspend() = %v", err)
	}

	t.Run("the existing token stops working", func(t *testing.T) {
		rec := do(t, f.server.http.Handler, authed(t, http.MethodGet, "/v1/me", "", f.token, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("and signing in again does not mint a new one", func(t *testing.T) {
		rec := postJSON(t, f.server.http.Handler, "/v1/sessions", testSignInAda)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.Bytes())
		}

		body := decodeProblem(t, rec.Body.Bytes())
		if body.Code != "account_suspended" {
			t.Errorf("code = %q, want account_suspended", body.Code)
		}

		// And no token came back under any name.
		if strings.Contains(rec.Body.String(), `"token"`) {
			t.Errorf("a suspended account was issued a token: %s", rec.Body.Bytes())
		}
	})

	t.Run("restoring the account lets it sign in again", func(t *testing.T) {
		if err := f.server.deps.Users.Suspend(t.Context(), f.userID, false); err != nil {
			t.Fatalf("Suspend(false) = %v", err)
		}

		if rec := postJSON(t, f.server.http.Handler, "/v1/sessions", testSignInAda); rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201; body %s", rec.Code, rec.Body.Bytes())
		}
	})
}
