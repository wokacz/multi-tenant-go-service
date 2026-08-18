package httptest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

type snapshotBody struct {
	User struct {
		ID     uuid.UUID `json:"id"`
		Locale string    `json:"locale"`
	} `json:"user"`
	System struct {
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
	} `json:"system"`
	Organizations []struct {
		ID          uuid.UUID `json:"id"`
		Slug        string    `json:"slug"`
		Status      string    `json:"status"`
		Roles       []string  `json:"roles"`
		Permissions []string  `json:"permissions"`
	} `json:"organizations"`
}

func (f *AuthzFixture) snapshot(t *testing.T) snapshotBody {
	t.Helper()

	rec := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/me/permissions", "", f.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, want 200; body %s", rec.Code, rec.Body.Bytes())
	}

	var out snapshotBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode snapshot: %v (body %s)", err, rec.Body.Bytes())
	}

	return out
}

// probe describes how to call one gated operation. The path is rendered with
// the fixture's own identifiers.
type probe struct {
	method string
	path   string
	body   string
}

// probes covers every organization-scoped operation.
//
// TestTheSnapshotAgreesWithEnforcement fails when an entry is missing, so
// adding a gated operation forces it to be described here — which is what makes
// the matrix below exhaustive rather than "the ones somebody remembered".
func probesFor(f *AuthzFixture, roleID uuid.UUID) map[string]probe {
	org := "/v1/orgs/" + f.OrgID.String()
	member := org + "/members/" + f.Membership.String()
	role := org + "/roles/" + roleID.String()

	// An invitation id that does not exist is enough: this matrix is about which
	// permission the middleware demands, and that is decided before the handler
	// looks anything up.
	invitation := org + "/invitations/" + uuid.Must(uuid.NewV7()).String()

	return map[string]probe{
		"get-organization":    {http.MethodGet, org, ""},
		"update-organization": {http.MethodPatch, org, `{"name":"Renamed"}`},
		"delete-organization": {http.MethodDelete, org, ""},

		"list-members":         {http.MethodGet, org + "/members", ""},
		"add-member":           {http.MethodPost, org + "/members", `{"email":"nobody@example.com","role_ids":[]}`},
		"update-member-status": {http.MethodPatch, member, `{"status":"active"}`},
		"remove-member":        {http.MethodDelete, member, ""},
		"set-member-roles":     {http.MethodPut, member + "/roles", `{"role_ids":[]}`},

		"list-roles":           {http.MethodGet, org + "/roles", ""},
		"get-role":             {http.MethodGet, role, ""},
		"create-role":          {http.MethodPost, org + "/roles", `{"key":"probe","name":"Probe","permissions":[]}`},
		"update-role":          {http.MethodPatch, role, `{"name":"Probed"}`},
		"set-role-permissions": {http.MethodPut, role + "/permissions", `{"permissions":[]}`},
		"delete-role":          {http.MethodDelete, role, ""},

		"list-invitations":    {http.MethodGet, org + "/invitations", ""},
		"invite-members":      {http.MethodPost, org + "/invitations", `{"emails":["nobody@example.com"],"role_ids":[]}`},
		"reissue-invitation":  {http.MethodPost, invitation + "/reissue", ""},
		"withdraw-invitation": {http.MethodDelete, invitation, ""},

		"list-audit-events": {http.MethodGet, org + "/audit", ""},
	}
}

// TestTheSnapshotAgreesWithEnforcement is the test that keeps the client and
// the server from drifting apart.
//
// The snapshot exists so a UI can hide what the user cannot Do. If it ever
// listed a permission the server would refuse, the product would offer actions
// that fail; if it omitted one the server would allow, features would be
// invisible. Neither shows up in any other test, because each half is correct
// on its own.
//
// So: for every gated operation, ask the snapshot whether the caller holds the
// permission, then actually call it. A 403 must happen exactly when the
// snapshot said no.
func TestTheSnapshotAgreesWithEnforcement(t *testing.T) {
	// A member who holds one permission and not the rest, so both branches of
	// the equivalence are exercised by the same run.
	for name, granted := range map[string][]authz.Permission{
		"a member with a single permission": {authz.PermOrganizationRead},
		"an owner":                          nil, // filled in below from the catalog
	} {
		t.Run(name, func(t *testing.T) {
			f := NewAuthzFixture(t)

			permissions := granted
			if permissions == nil {
				permissions = authz.InScope(authz.ScopeOrganization)
			}

			keys := make([]string, 0, len(permissions))
			for _, perm := range permissions {
				keys = append(keys, string(perm))
			}

			role := f.Repo.SeedRole(f.OrgID, "probe_role", keys...)
			f.Repo.SeedMemberRoles(f.Membership, role)

			// A second role for the role-shaped probes to act on, so deleting it
			// does not remove the caller's own permissions mid-test.
			target := f.Repo.SeedRole(f.OrgID, "target_role")

			snapshot := f.snapshot(t)

			var held []string

			for _, entry := range snapshot.Organizations {
				if entry.ID == f.OrgID {
					held = entry.Permissions
				}
			}

			probes := probesFor(f, target)

			for id, rule := range api.OperationAccess() {
				if rule.Scope != authz.ScopeOrganization {
					continue
				}

				if _, ok := probes[id]; !ok {
					t.Errorf("%s is gated but has no probe; the matrix is not exhaustive", id)

					continue
				}

				t.Run(id, func(t *testing.T) {
					// Fresh state per operation: several of these mutate, and one
					// deleting the organization would make the next look refused
					// for the wrong reason.
					sub := NewAuthzFixture(t)
					subRole := sub.Repo.SeedRole(sub.OrgID, "probe_role", keys...)
					sub.Repo.SeedMemberRoles(sub.Membership, subRole)
					subTarget := sub.Repo.SeedRole(sub.OrgID, "target_role")

					sp := probesFor(sub, subTarget)[id]

					rec := Do(t, sub.Server.Handler(),
						Authed(t, sp.method, sp.path, sp.body, sub.Token, ""))

					wantAllowed := slices.Contains(held, string(rule.Permission))
					gotAllowed := rec.Code != http.StatusForbidden

					if gotAllowed != wantAllowed {
						t.Errorf("%s %s = %d; the snapshot %s %q but the server %s it",
							sp.method, sp.path, rec.Code,
							map[bool]string{true: "lists", false: "omits"}[wantAllowed],
							rule.Permission,
							map[bool]string{true: "allowed", false: "refused"}[gotAllowed])
					}
				})
			}

		})
	}
}

// TestTheSnapshotOmitsOrganizationsThatGrantNothing keeps a suspended membership
// from looking usable.
//
// It used to cover an invited one as well, listed with an empty permission set so
// it would not silently vanish from the UI. An invitation is not a membership any
// more, so it is not in the snapshot at all — GET /v1/me/invitations is where a
// client finds it, and that is a better answer than a membership that grants
// nothing.
func TestTheSnapshotOmitsOrganizationsThatGrantNothing(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	suspended := f.Repo.SeedOrganization("beta", "Beta")
	f.Repo.SeedMember(suspended, f.UserID, ent.MembershipSuspended,
		f.Repo.SeedShippedRole(suspended, authz.RoleOwner))

	snapshot := f.snapshot(t)

	for _, entry := range snapshot.Organizations {
		if entry.ID != suspended {
			continue
		}

		if entry.Status != string(ent.MembershipSuspended) {
			t.Errorf("status = %q, want suspended", entry.Status)
		}

		if len(entry.Permissions) != 0 {
			t.Errorf("a suspended membership lists permissions %v; it grants nothing",
				entry.Permissions)
		}

		return
	}

	t.Error("the suspension is missing from the snapshot entirely; it should be visible but empty")
}

// TestTheSnapshotIsScopedToTheCaller is the tenancy check on the read path.
func TestTheSnapshotIsScopedToTheCaller(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	foreign := f.Repo.SeedOrganization("globex", "Globex")
	f.Repo.SeedMember(foreign, uuid.Must(uuid.NewV7()), ent.MembershipActive,
		f.Repo.SeedShippedRole(foreign, authz.RoleOwner))

	snapshot := f.snapshot(t)

	for _, entry := range snapshot.Organizations {
		if entry.Slug == "globex" {
			t.Fatal("the snapshot names an organization the caller does not belong to")
		}
	}
}

func TestTheSnapshotIsCacheableAndChangesWithPermissions(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	first := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/me/permissions", "", f.Token, ""))
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the snapshot")
	}

	t.Run("an unchanged snapshot answers 304", func(t *testing.T) {
		req := Authed(t, http.MethodGet, "/v1/me/permissions", "", f.Token, "")
		req.Header.Set("If-None-Match", etag)

		if rec := Do(t, f.Server.Handler(), req); rec.Code != http.StatusNotModified {
			t.Errorf("status = %d, want 304; body %s", rec.Code, rec.Body.Bytes())
		}
	})

	t.Run("granting a role changes the tag", func(t *testing.T) {
		f.Repo.SeedMemberRoles(f.Membership, f.Repo.SeedShippedRole(f.OrgID, authz.RoleOwner))

		req := Authed(t, http.MethodGet, "/v1/me/permissions", "", f.Token, "")
		req.Header.Set("If-None-Match", etag)

		rec := Do(t, f.Server.Handler(), req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the stale tag must not match", rec.Code)
		}

		if rec.Header().Get("ETag") == etag {
			t.Error("the ETag survived a permission change; a client would cache the old answer")
		}
	})
}

// TestTheSnapshotIsAvailableToAnAccountWithNothing is the lockout guard for the
// one endpoint a client cannot start without.
func TestTheSnapshotIsAvailableToAnAccountWithNothing(t *testing.T) {
	f := NewAuthzFixture(t)

	f.Repo.SeedMemberRoles(f.Membership)

	snapshot := f.snapshot(t)

	if snapshot.User.ID != f.UserID {
		t.Errorf("user id = %v, want %v", snapshot.User.ID, f.UserID)
	}

	if len(snapshot.System.Permissions) != 0 {
		t.Errorf("system permissions = %v, want none", snapshot.System.Permissions)
	}
}

// TestSystemRolesAppearInTheSnapshot covers the other scope.
func TestSystemRolesAppearInTheSnapshot(t *testing.T) {
	f := NewAuthzFixture(t)

	f.Repo.SeedSystemRole(f.UserID, string(authz.RolePlatformAdmin))

	snapshot := f.snapshot(t)

	if !slices.Contains(snapshot.System.Roles, string(authz.RolePlatformAdmin)) {
		t.Errorf("system roles = %v, want the platform role", snapshot.System.Roles)
	}

	if !slices.Contains(snapshot.System.Permissions, string(authz.PermPlatformUsersRead)) {
		t.Errorf("system permissions = %v, want the role's permissions expanded",
			snapshot.System.Permissions)
	}
}

// TestTheSnapshotIsWrittenInTheNegotiatedLanguage is a small thing that matters
// for the header contract: the body says which language it is in, so a client
// can tell whether its own cached labels still apply.
func TestTheSnapshotIsWrittenInTheNegotiatedLanguage(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleViewer)

	req := Authed(t, http.MethodGet, "/v1/me/permissions", "", f.Token, "")
	req.Header.Set("Accept-Language", "pl")

	rec := Do(t, f.Server.Handler(), req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), `"locale":"pl"`) {
		t.Errorf("body does not report the language it was written in: %s", rec.Body.Bytes())
	}

	if got := rec.Header().Get("Content-Language"); got != "pl" {
		t.Errorf("Content-Language = %q, want pl", got)
	}
}
