package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
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
// TestARoleCannotCarryAPlatformPermission separates "you do not have this" from
// "this could never live here".
//
// An owner holds every organization permission, so escalation is not the reason
// the request fails — no role in an organization can carry an installation-wide
// key. Reporting a 403 escalation would send the caller looking for a permission
// to acquire, and the catalog endpoint hands them the key together with its
// scope, so "unknown permission" would be false as well.
func TestARoleCannotCarryAPlatformPermission(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	body := fmt.Sprintf(`{"key":"overreach","name":"Overreach","permissions":[%q]}`,
		authz.PermPlatformUsersDelete)

	res := f.call(t, http.MethodPost, f.orgPath("/roles"), body).
		expect(t, http.StatusUnprocessableEntity)

	var doc problemBody
	res.decode(t, &doc)

	if doc.Code != problem.CodeWrongScope {
		t.Errorf("code = %q, want %q", doc.Code, problem.CodeWrongScope)
	}

	// The same on the other edit path, which is the one that turns roles.update
	// into a permission to acquire everything if it is not checked.
	role := f.repo.SeedRole(f.orgID, "plain", string(authz.PermOrganizationRead))
	perms := fmt.Sprintf(`{"permissions":[%q]}`, authz.PermPlatformUsersDelete)

	f.call(t, http.MethodPut, f.orgPath("/roles/"+role.String()+"/permissions"), perms).
		expect(t, http.StatusUnprocessableEntity)
}

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

// TestAnAdministratorCannotReachAboveThemselves is the rank rule end to end.
//
// members.remove, members.suspend and members.roles.assign all belong to "admin",
// and an owner's membership is an ordinary row to each of them. Before this rule
// an administrator could remove the owner above them, suspend them into a 404, or
// replace their roles with "viewer" — the anti-escalation check only inspects the
// roles being assigned, and viewer is well inside an admin's own powers. The
// result was an inversion: the lesser role neutralising the greater one.
func TestAnAdministratorCannotReachAboveThemselves(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleAdmin)

	owner := f.repo.SeedShippedRole(f.orgID, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	target := f.repo.SeedMember(f.orgID, uuid.Must(uuid.NewV7()), models.MembershipActive, owner)

	// A second owner, so the last-owner rule cannot refuse these on its own.
	// Without it the organization is protected here by accident, and the hole this
	// test is about — an administrator cutting an owner out of an organization that
	// has more than one — stays invisible.
	f.repo.SeedMember(f.orgID, uuid.Must(uuid.NewV7()), models.MembershipActive, owner)

	member := f.orgPath("/members/" + target.String())

	for _, probe := range []struct {
		name, method, path, body string
	}{
		// remove goes last: if the rule ever stops working, the earlier probes
		// still act on a member who is there, so each failure names its own
		// problem instead of cascading into a 404.
		{"suspend", http.MethodPatch, member, `{"status":"suspended"}`},
		{"demote", http.MethodPut, member + "/roles", fmt.Sprintf(`{"role_ids":[%q]}`, viewer)},
		{"remove", http.MethodDelete, member, ""},
	} {
		t.Run(probe.name, func(t *testing.T) {
			res := f.call(t, probe.method, probe.path, probe.body).expect(t, http.StatusForbidden)

			var doc problemBody
			res.decode(t, &doc)

			if doc.Code != problem.CodeInsufficientRank {
				t.Errorf("code = %q, want %q", doc.Code, problem.CodeInsufficientRank)
			}
		})
	}
}

// TestAnOwnerCanStillActOnAnAdministrator proves the rank rule has a direction.
// A blanket ban on touching other people would be easy and useless.
func TestAnOwnerCanStillActOnAnAdministrator(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	admin := f.repo.SeedShippedRole(f.orgID, authz.RoleAdmin)
	target := f.repo.SeedMember(f.orgID, uuid.Must(uuid.NewV7()), models.MembershipActive, admin)

	f.call(t, http.MethodPatch, f.orgPath("/members/"+target.String()),
		`{"status":"suspended"}`).expect(t, http.StatusOK)

	f.call(t, http.MethodDelete, f.orgPath("/members/"+target.String()), "").
		expect(t, http.StatusNoContent)
}

// TestTheRankRuleDoesNotBlockLeaving keeps the one exit an organization has open.
// The caller's own membership carries exactly the permissions their grant does, so
// the comparison passes — this pins that it stays that way.
func TestTheRankRuleDoesNotBlockLeaving(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleAdmin)

	// A second, more powerful member, so nothing else about the organization makes
	// this refusable: the caller is not the last owner and holds no owner role.
	f.repo.SeedMember(f.orgID, uuid.Must(uuid.NewV7()), models.MembershipActive,
		f.repo.SeedShippedRole(f.orgID, authz.RoleOwner))

	f.call(t, http.MethodDelete, f.orgPath("/members/"+f.membership.String()), "").
		expect(t, http.StatusNoContent)
}

// TestAnOwnerWhoseAccountIsDeletedCanBeRemoved is the recovery path for a
// membership that outlived its person.
//
// Soft deleting an account does not fire the foreign key cascade, so the
// membership row stays, still holding owner. It must not count as the owner the
// organization would lose, because it counts nowhere else: the check that
// refuses the change saw it, and the count of owners that could overrule the
// refusal did not. The row was therefore impossible to remove however many live
// owners existed, and promoting another owner moved both numbers together.
func TestAnOwnerWhoseAccountIsDeletedCanBeRemoved(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	owner, err := f.repo.RoleByKey(t.Context(), f.orgID, string(authz.RoleOwner))
	if err != nil {
		t.Fatalf("owner role: %v", err)
	}

	ghostAccount := uuid.Must(uuid.NewV7())
	ghost := f.repo.SeedMember(f.orgID, ghostAccount, models.MembershipActive, owner.ID)
	f.repo.SeedSoftDeletedUser(ghostAccount)

	f.call(t, http.MethodDelete, f.orgPath("/members/"+ghost.String()), "").
		expect(t, http.StatusNoContent)

	// The rule itself is not switched off: Ada is the only live owner, so she
	// still cannot be removed.
	f.call(t, http.MethodDelete, f.orgPath("/members/"+f.membership.String()), "").
		expect(t, http.StatusConflict)
}

// TestAMembershipWhoseAccountIsDeletedIsNotListed pins the other half of the
// same rule on the side the fake can see. The store test covers the Postgres
// queries, where the condition sat in a LEFT JOIN and filtered nothing.
func TestAMembershipWhoseAccountIsDeletedIsNotListed(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	ghostAccount := uuid.Must(uuid.NewV7())
	f.repo.SeedMember(f.orgID, ghostAccount, models.MembershipActive, viewer)
	f.repo.SeedSoftDeletedUser(ghostAccount)

	var list struct {
		Members []memberDetail `json:"members"`
	}
	f.call(t, http.MethodGet, f.orgPath("/members"), "").
		expect(t, http.StatusOK).decode(t, &list)

	for _, member := range list.Members {
		if member.UserID != nil && *member.UserID == ghostAccount {
			t.Errorf("the membership of a deleted account is listed as %+v", member)
		}
	}
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

// TestInvitingAMemberDoesNotRevealWhetherTheAddressIsRegistered is the
// enumeration guard for this path. Unknown and known addresses produce the
// same 201 with status invited and no user_id, so an administrator cannot
// ask "is this person registered here" of the whole installation.
func TestInvitingAMemberDoesNotRevealWhetherTheAddressIsRegistered(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	outsider := registerOutsider(t, f)

	unknown := inviteBody(t, f, "nobody@example.com", viewer)
	known := inviteBody(t, f, outsider, viewer)

	if unknown.Status != string(models.MembershipInvited) || known.Status != string(models.MembershipInvited) {
		t.Errorf("status unknown=%q known=%q, want invited for both", unknown.Status, known.Status)
	}

	if unknown.UserID != nil || known.UserID != nil {
		t.Errorf("user_id unknown=%v known=%v, want omitted so the two responses match",
			unknown.UserID, known.UserID)
	}

	if unknown.Name != "" || known.Name != "" {
		t.Errorf("name unknown=%q known=%q, want empty until accept", unknown.Name, known.Name)
	}
}

func inviteBody(t *testing.T, f *authzFixture, email string, roleID uuid.UUID) memberDetail {
	t.Helper()

	body := fmt.Sprintf(`{"email":%q,"role_ids":[%q]}`, email, roleID)
	var out memberDetail
	f.call(t, http.MethodPost, f.orgPath("/members"), body).
		expect(t, http.StatusCreated).decode(t, &out)

	return out
}

type memberDetail struct {
	ID     uuid.UUID  `json:"id"`
	UserID *uuid.UUID `json:"user_id"`
	Name   string     `json:"name"`
	Email  string     `json:"email"`
	Status string     `json:"status"`
}

func TestInvitingAnAddressAlreadyInTheOrganizationIsConflict(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	body := fmt.Sprintf(`{"email":%q,"role_ids":[%q]}`, testEmail, viewer)

	f.call(t, http.MethodPost, f.orgPath("/members"), body).expect(t, http.StatusConflict)
}

func TestInvitingAMemberSendsMail(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)

	inviteBody(t, f, "new@example.com", viewer)

	if f.mailer.inviteTo != "new@example.com" {
		t.Errorf("invitation mail to = %q, want the invited address", f.mailer.inviteTo)
	}

	if f.mailer.inviteOrg != "Acme" {
		t.Errorf("invitation mail org = %q, want Acme", f.mailer.inviteOrg)
	}
}

func TestAFailedInvitationMailStillCreatesTheMembership(t *testing.T) {
	s, repo := newTestAPIConfig(t, failingMailer{}, memory.NewUsers(), nil)

	registerAda(t, s)
	session := signInAda(t, s, "", http.StatusCreated)

	orgID := repo.SeedOrganization("acme", "Acme")
	owner := repo.SeedShippedRole(orgID, authz.RoleOwner)
	repo.SeedMember(orgID, session.User.ID, models.MembershipActive, owner)
	viewer := repo.SeedShippedRole(orgID, authz.RoleViewer)

	body := fmt.Sprintf(`{"email":"nobody@example.com","role_ids":[%q]}`, viewer)
	rec := do(t, s.http.Handler,
		authed(t, http.MethodPost, "/v1/orgs/"+orgID.String()+"/members", body, session.Token, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite with mail down = %d, want 201; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestAnInvitationCannotBeActivatedByAnAdministrator(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)
	viewer := f.repo.SeedShippedRole(f.orgID, authz.RoleViewer)
	invited := inviteBody(t, f, "nobody@example.com", viewer)

	f.call(t, http.MethodPatch, f.orgPath("/members/"+invited.ID.String()),
		`{"status":"active"}`).expect(t, http.StatusUnprocessableEntity)
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
