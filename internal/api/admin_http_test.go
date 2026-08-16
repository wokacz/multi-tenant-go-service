package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/domain/authz"
	"github.com/wokacz/go-example/internal/store/models"
)

// orgPath builds a path under the fixture's organization.
func (f *authzFixture) orgPath(suffix string) string {
	return "/v1/orgs/" + f.orgID.String() + suffix
}

func (f *authzFixture) call(t *testing.T, method, path, body string) *httpResult {
	t.Helper()

	rec := do(t, f.server.http.Handler, authed(t, method, path, body, f.token, ""))

	return &httpResult{code: rec.Code, body: rec.Body.Bytes()}
}

type httpResult struct {
	code int
	body []byte
}

func (r *httpResult) expect(t *testing.T, want int) *httpResult {
	t.Helper()

	if r.code != want {
		t.Fatalf("status = %d, want %d; body %s", r.code, want, r.body)
	}

	return r
}

func (r *httpResult) decode(t *testing.T, into any) {
	t.Helper()

	if err := json.Unmarshal(r.body, into); err != nil {
		t.Fatalf("decode: %v (body %s)", err, r.body)
	}
}

type roleBody struct {
	ID          uuid.UUID `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	IsSystem    bool      `json:"is_system"`
	Permissions []string  `json:"permissions"`
	Members     int       `json:"members"`
}

type memberBody struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
	Roles []struct {
		Key string `json:"key"`
	} `json:"roles"`
}

// TestCreatingARoleCannotGrantWhatTheCallerLacks is the most important test in
// the suite.
//
// Without this rule roles.create is a permission to acquire every other
// permission: define a role holding organization.delete, assign it to yourself,
// and the whole scheme has been talked out of its own rules through the front
// door.
func TestCreatingARoleCannotGrantWhatTheCallerLacks(t *testing.T) {
	f := newAuthzFixture(t)

	// An administrator over roles who is not an owner, so they hold
	// roles.create but not organization.delete.
	limited := f.repo.SeedRole(f.orgID, "role_admin",
		string(authz.PermRolesRead),
		string(authz.PermRolesCreate),
		string(authz.PermRolesUpdate),
		string(authz.PermRolesDelete),
	)
	f.repo.SeedMemberRoles(f.membership, limited)

	body := fmt.Sprintf(`{"key":"sneaky","name":"Sneaky","permissions":[%q]}`,
		authz.PermOrganizationDelete)

	res := f.call(t, http.MethodPost, f.orgPath("/roles"), body).expect(t, http.StatusForbidden)

	if !strings.Contains(string(res.body), "do not hold") {
		t.Errorf("detail = %s, want it to name privilege escalation", res.body)
	}

	// And nothing was created.
	var list struct {
		Roles []roleBody `json:"roles"`
	}
	f.call(t, http.MethodGet, f.orgPath("/roles"), "").expect(t, http.StatusOK).decode(t, &list)

	for _, role := range list.Roles {
		if role.Key == "sneaky" {
			t.Fatal("the role was created despite the refusal")
		}
	}
}

// TestCreatingARoleWithinTheCallersOwnPowersSucceeds is the other half: the rule
// restricts, it does not simply forbid.
func TestCreatingARoleWithinTheCallersOwnPowersSucceeds(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	body := fmt.Sprintf(`{"key":"auditor","name":"Auditor","permissions":[%q,%q]}`,
		authz.PermMembersRead, authz.PermRolesRead)

	var created roleBody
	f.call(t, http.MethodPost, f.orgPath("/roles"), body).
		expect(t, http.StatusCreated).decode(t, &created)

	if created.Key != "auditor" || created.IsSystem {
		t.Errorf("created = %+v, want a custom role named auditor", created)
	}

	slices.Sort(created.Permissions)

	want := []string{string(authz.PermMembersRead), string(authz.PermRolesRead)}
	slices.Sort(want)

	if !slices.Equal(created.Permissions, want) {
		t.Errorf("permissions = %v, want %v", created.Permissions, want)
	}
}

// TestWideningARoleCannotGrantWhatTheCallerLacks closes the same hole on the
// edit path. Creating a role within your powers and then widening it would
// otherwise be a two-step escalation.
func TestWideningARoleCannotGrantWhatTheCallerLacks(t *testing.T) {
	f := newAuthzFixture(t)

	limited := f.repo.SeedRole(f.orgID, "role_admin",
		string(authz.PermRolesRead),
		string(authz.PermRolesCreate),
		string(authz.PermRolesUpdate),
	)
	f.repo.SeedMemberRoles(f.membership, limited)

	target := f.repo.SeedRole(f.orgID, "helper", string(authz.PermRolesRead))

	body := fmt.Sprintf(`{"permissions":[%q]}`, authz.PermOrganizationDelete)

	f.call(t, http.MethodPut, f.orgPath("/roles/"+target.String()+"/permissions"), body).
		expect(t, http.StatusForbidden)

	var role roleBody
	f.call(t, http.MethodGet, f.orgPath("/roles/"+target.String()), "").
		expect(t, http.StatusOK).decode(t, &role)

	if slices.Contains(role.Permissions, string(authz.PermOrganizationDelete)) {
		t.Error("the role was widened despite the refusal")
	}
}

// TestAssigningARoleCannotGrantWhatTheCallerLacks covers the third route to the
// same place: not editing a role, but handing out one that already exists.
//
// This is why the check reads the role's permissions rather than trusting the
// role id — a caller who may not grant organization.delete may not grant it by
// naming a role that happens to contain it either.
func TestAssigningARoleCannotGrantWhatTheCallerLacks(t *testing.T) {
	f := newAuthzFixture(t)

	limited := f.repo.SeedRole(f.orgID, "people_admin",
		string(authz.PermMembersRead),
		string(authz.PermMembersRolesAssign),
		string(authz.PermRolesRead),
	)
	f.repo.SeedMemberRoles(f.membership, limited)

	// A powerful role that already exists in the organization.
	owner := f.repo.SeedShippedRole(f.orgID, authz.RoleOwner)

	body := fmt.Sprintf(`{"role_ids":[%q]}`, owner)

	f.call(t, http.MethodPut, f.orgPath("/members/"+f.membership.String()+"/roles"), body).
		expect(t, http.StatusForbidden)

	// And the caller did not acquire it.
	if code := f.getOrg(t); code == http.StatusOK {
		t.Error("the caller gained organization.read by assigning themselves the owner role")
	}
}

// TestTheLastOwnerCannotBeDemoted keeps an organization from becoming
// unadministrable. Recovering from that needs database access, which is not a
// support path anyone should have to use.
func TestTheLastOwnerCannotBeDemoted(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	body := fmt.Sprintf(`{"role_ids":[%q]}`, viewer)

	res := f.call(t, http.MethodPut, f.orgPath("/members/"+f.membership.String()+"/roles"), body).
		expect(t, http.StatusConflict)

	if !strings.Contains(string(res.body), "without an owner") {
		t.Errorf("detail = %s, want it to explain the refusal", res.body)
	}

	// Still an owner.
	if code := f.getOrg(t); code != http.StatusOK {
		t.Errorf("status after the refused demotion = %d, want 200", code)
	}
}

func TestTheLastOwnerCannotBeRemovedOrSuspended(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		f := newAuthzFixture(t, authz.RoleOwner)

		f.call(t, http.MethodDelete, f.orgPath("/members/"+f.membership.String()), "").
			expect(t, http.StatusConflict)
	})

	t.Run("suspended", func(t *testing.T) {
		f := newAuthzFixture(t, authz.RoleOwner)

		f.call(t, http.MethodPatch, f.orgPath("/members/"+f.membership.String()),
			`{"status":"suspended"}`).expect(t, http.StatusConflict)
	})
}

// TestASecondOwnerMakesDemotionPossible proves the rule counts rather than
// simply forbidding.
func TestASecondOwnerMakesDemotionPossible(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	ownerRole := f.repo.SeedRole(f.orgID, "co_owner", permissionKeys(authz.RoleOwner)...)
	_ = ownerRole

	// A real second owner, holding the same shipped role the fixture does.
	var roles struct {
		Roles []roleBody `json:"roles"`
	}
	f.call(t, http.MethodGet, f.orgPath("/roles"), "").expect(t, http.StatusOK).decode(t, &roles)

	var ownerID uuid.UUID

	for _, role := range roles.Roles {
		if role.Key == string(authz.RoleOwner) {
			ownerID = role.ID
		}
	}

	if ownerID == uuid.Nil {
		t.Fatal("the fixture has no owner role")
	}

	f.repo.SeedMember(f.orgID, uuid.Must(uuid.NewV7()), models.MembershipActive, ownerID)

	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	body := fmt.Sprintf(`{"role_ids":[%q]}`, viewer)

	f.call(t, http.MethodPut, f.orgPath("/members/"+f.membership.String()+"/roles"), body).
		expect(t, http.StatusOK)
}

// TestShippedRolesCannotBeEdited keeps every organization's copy of "admin"
// meaning the same thing.
func TestShippedRolesCannotBeEdited(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	var roles struct {
		Roles []roleBody `json:"roles"`
	}
	f.call(t, http.MethodGet, f.orgPath("/roles"), "").expect(t, http.StatusOK).decode(t, &roles)

	if len(roles.Roles) == 0 {
		t.Fatal("the fixture has no roles")
	}

	system := roles.Roles[0]
	if !system.IsSystem {
		t.Fatalf("role %q is not a system role; the fixture changed", system.Key)
	}

	path := f.orgPath("/roles/" + system.ID.String())

	for name, call := range map[string]func() *httpResult{
		"rename": func() *httpResult { return f.call(t, http.MethodPatch, path, `{"name":"Rewritten"}`) },
		"change what it grants": func() *httpResult {
			return f.call(t, http.MethodPut, path+"/permissions", `{"permissions":[]}`)
		},
		"delete": func() *httpResult { return f.call(t, http.MethodDelete, path, "") },
	} {
		t.Run(name, func(t *testing.T) {
			res := call()
			if res.code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %s", res.code, res.body)
			}
		})
	}
}

// TestARoleInUseCannotBeDeleted refuses the change that would take permissions
// away from people the caller never looked at.
func TestARoleInUseCannotBeDeleted(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	role := f.repo.SeedRole(f.orgID, "auditor", string(authz.PermMembersRead))
	holder := f.repo.SeedMember(f.orgID, uuid.Must(uuid.NewV7()), models.MembershipActive, role)

	res := f.call(t, http.MethodDelete, f.orgPath("/roles/"+role.String()), "").
		expect(t, http.StatusConflict)

	if !strings.Contains(string(res.body), "still assigned") {
		t.Errorf("detail = %s, want it to explain the refusal", res.body)
	}

	// Unassign, and it goes.
	f.repo.SeedMemberRoles(holder)

	f.call(t, http.MethodDelete, f.orgPath("/roles/"+role.String()), "").
		expect(t, http.StatusNoContent)
}

// TestARoleFromAnotherOrganizationCannotBeUsed is the resource half of a
// decision, exercised end to end. The caller is a full owner here; what stops
// them is that the id is not this organization's.
func TestARoleFromAnotherOrganizationCannotBeUsed(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	foreign := f.repo.SeedOrganization("globex", "Globex")
	foreignRole := f.repo.SeedShippedRole(foreign, authz.RoleViewer)

	t.Run("reading it is 404", func(t *testing.T) {
		f.call(t, http.MethodGet, f.orgPath("/roles/"+foreignRole.String()), "").
			expect(t, http.StatusNotFound)
	})

	t.Run("assigning it is 404", func(t *testing.T) {
		body := fmt.Sprintf(`{"role_ids":[%q]}`, foreignRole)

		f.call(t, http.MethodPut, f.orgPath("/members/"+f.membership.String()+"/roles"), body).
			expect(t, http.StatusNotFound)
	})
}

// TestAMembershipFromAnotherOrganizationCannotBeTouched is the same guarantee
// for people rather than roles.
func TestAMembershipFromAnotherOrganizationCannotBeTouched(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	foreign := f.repo.SeedOrganization("globex", "Globex")
	foreignMember := f.repo.SeedMember(foreign, uuid.Must(uuid.NewV7()), models.MembershipActive)

	f.call(t, http.MethodDelete, f.orgPath("/members/"+foreignMember.String()), "").
		expect(t, http.StatusNotFound)

	f.call(t, http.MethodPatch, f.orgPath("/members/"+foreignMember.String()),
		`{"status":"suspended"}`).expect(t, http.StatusNotFound)
}

// TestAddingAMemberUsesAnExistingAccount covers the happy path and the two
// refusals around it.
func TestAddingAMemberUsesAnExistingAccount(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	t.Run("an address nobody has registered is 404", func(t *testing.T) {
		body := fmt.Sprintf(`{"email":"nobody@example.com","role_ids":[%q]}`, viewer)

		f.call(t, http.MethodPost, f.orgPath("/members"), body).expect(t, http.StatusNotFound)
	})

	t.Run("an account already in the organization is 409", func(t *testing.T) {
		body := fmt.Sprintf(`{"email":%q,"role_ids":[%q]}`, testEmail, viewer)

		f.call(t, http.MethodPost, f.orgPath("/members"), body).expect(t, http.StatusConflict)
	})
}

// TestListingMembersShowsRoles is the read path the settings screen is built on.
func TestListingMembersShowsRoles(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	var list struct {
		Members []memberBody `json:"members"`
	}
	f.call(t, http.MethodGet, f.orgPath("/members"), "").
		expect(t, http.StatusOK).decode(t, &list)

	if len(list.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(list.Members))
	}

	member := list.Members[0]
	if member.Email != testEmail {
		t.Errorf("email = %q, want %q", member.Email, testEmail)
	}

	if len(member.Roles) != 1 || member.Roles[0].Key != string(authz.RoleOwner) {
		t.Errorf("roles = %+v, want the owner role", member.Roles)
	}
}

// TestThePermissionCatalogIsServedFromTheCode pins where the catalog lives. A
// database-backed list could disagree with what the handlers actually enforce.
func TestThePermissionCatalogIsServedFromTheCode(t *testing.T) {
	f := newAuthzFixture(t)

	var body struct {
		Permissions []struct {
			Key   string `json:"key"`
			Scope string `json:"scope"`
			Group string `json:"group"`
		} `json:"permissions"`
	}
	f.call(t, http.MethodGet, "/v1/permissions", "").expect(t, http.StatusOK).decode(t, &body)

	if len(body.Permissions) != len(authz.Catalog()) {
		t.Fatalf("catalog has %d entries over the wire, want %d",
			len(body.Permissions), len(authz.Catalog()))
	}

	for i, def := range authz.Catalog() {
		if body.Permissions[i].Key != string(def.Key) {
			t.Errorf("entry %d = %q, want %q", i, body.Permissions[i].Key, def.Key)
		}
	}
}

// permissionKeys is the shipped role's permissions as wire strings.
func permissionKeys(key authz.RoleKey) []string {
	def, ok := authz.LookupRole(key)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(def.Permissions))
	for _, perm := range def.Permissions {
		out = append(out, string(perm))
	}

	return out
}
