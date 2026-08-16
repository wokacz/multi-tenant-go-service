package authz_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// stubRepo answers from fixed maps. The shared in-memory fake lives with the
// other repositories; this package only needs enough of one to exercise the
// decision itself.
type stubRepo struct {
	orgKeys    map[uuid.UUID][]string
	systemKeys map[uuid.UUID][]string
	err        error
}

func (r *stubRepo) OrganizationPermissionKeys(_ context.Context, userID, orgID uuid.UUID) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}

	keys, ok := r.orgKeys[key(userID, orgID)]
	if !ok {
		return nil, authz.ErrNotMember
	}

	return keys, nil
}

func (r *stubRepo) PermissionKeysByOrganization(_ context.Context, userID uuid.UUID) (map[uuid.UUID][]string, error) {
	if r.err != nil {
		return nil, r.err
	}

	// The stub keys its map by the folded pair, so it cannot un-fold it back
	// into organizations. Nothing in this file exercises the snapshot; the
	// tests that do run against the shared in-memory repository.
	return map[uuid.UUID][]string{}, nil
}

func (r *stubRepo) SystemRoleKeys(_ context.Context, userID uuid.UUID) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}

	return r.systemKeys[userID], nil
}

// key folds a (user, organization) pair into one uuid so the stub can use a
// single map. XOR is fine here: the test controls both inputs.
func key(user, org uuid.UUID) uuid.UUID {
	var out uuid.UUID
	for i := range out {
		out[i] = user[i] ^ org[i]
	}

	return out
}

func ids(t *testing.T, n int) []uuid.UUID {
	t.Helper()

	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.Must(uuid.NewV7())
	}

	return out
}

// TestPermissionKeysFollowTheNamingScheme keeps the catalog readable as it
// grows. A key that does not parse is one a settings screen cannot group and
// nobody can guess from a neighbouring one.
func TestPermissionKeysFollowTheNamingScheme(t *testing.T) {
	for _, def := range authz.Catalog() {
		if !authz.ValidKey(def.Key) {
			t.Errorf("permission %q does not follow [platform.]<resource>[.<subresource>].<action>", def.Key)
		}

		if !def.Scope.Valid() {
			t.Errorf("permission %q has scope %q, which is not a scope", def.Key, def.Scope)
		}

		if def.Group == "" {
			t.Errorf("permission %q has no group, so the settings UI cannot file it", def.Key)
		}
	}
}

func TestCatalogHasNoDuplicateKeys(t *testing.T) {
	seen := map[authz.Permission]bool{}

	for _, def := range authz.Catalog() {
		if seen[def.Key] {
			t.Errorf("permission %q appears in the catalog twice", def.Key)
		}

		seen[def.Key] = true
	}
}

// TestPlatformPrefixMatchesScope ties the naming convention to the scope, so
// "platform.users.read" cannot quietly become an organization permission that
// every org admin holds.
func TestPlatformPrefixMatchesScope(t *testing.T) {
	for _, def := range authz.Catalog() {
		prefixed := len(def.Key) > 9 && def.Key[:9] == "platform."

		switch {
		case prefixed && def.Scope != authz.ScopeSystem:
			t.Errorf("%q is named as a platform permission but is scoped %q", def.Key, def.Scope)
		case !prefixed && def.Scope == authz.ScopeSystem:
			t.Errorf("%q is system-scoped but is not named platform.*", def.Key)
		}
	}
}

// TestOwnerCoversEveryOrganizationPermission is the guard on the one role that
// must be total.
//
// A permission nobody holds is a feature that fails for every user and reports
// no reason. The owner role is derived from the catalog precisely so this cannot
// happen; this fails if someone later replaces that derivation with a list.
func TestOwnerCoversEveryOrganizationPermission(t *testing.T) {
	owner, ok := authz.LookupRole(authz.RoleOwner)
	if !ok {
		t.Fatal("LookupRole(RoleOwner) = _, false")
	}

	for _, perm := range authz.InScope(authz.ScopeOrganization) {
		if !slices.Contains(owner.Permissions, perm) {
			t.Errorf("owner does not grant %q, so no role does and the feature is unreachable", perm)
		}
	}
}

// TestPlatformAdminCoversEverySystemPermission is the same rule for the other
// scope.
func TestPlatformAdminCoversEverySystemPermission(t *testing.T) {
	admin, ok := authz.LookupRole(authz.RolePlatformAdmin)
	if !ok {
		t.Fatal("LookupRole(RolePlatformAdmin) = _, false")
	}

	for _, perm := range authz.InScope(authz.ScopeSystem) {
		if !slices.Contains(admin.Permissions, perm) {
			t.Errorf("platform_admin does not grant %q", perm)
		}
	}
}

// TestDefaultRolesReferenceKnownPermissions catches a role listing a permission
// that was renamed out from under it. The row would be written, grant nothing,
// and look correct in the settings screen.
func TestDefaultRolesReferenceKnownPermissions(t *testing.T) {
	all := append(authz.OrganizationRoles(), authz.SystemRoles()...)

	for _, role := range all {
		if !role.Scope.Valid() {
			t.Errorf("role %q has scope %q", role.Key, role.Scope)
		}

		if role.Name == "" {
			t.Errorf("role %q has no fallback name", role.Key)
		}

		for _, perm := range role.Permissions {
			if !authz.Known(perm) {
				t.Errorf("role %q grants %q, which is not in the catalog", role.Key, perm)
			}

			def, _ := authz.Lookup(perm)
			if def.Scope != role.Scope {
				t.Errorf("role %q is scoped %q but grants %q, which is scoped %q",
					role.Key, role.Scope, perm, def.Scope)
			}
		}
	}
}

// TestAccessorsDoNotShareStateWithTheCatalog is the encapsulation test. Every
// accessor hands out package state, and a caller writing through one of those
// slices would change what every later request is granted.
func TestAccessorsDoNotShareStateWithTheCatalog(t *testing.T) {
	first := authz.Catalog()
	first[0].Key = "tampered"

	if authz.Catalog()[0].Key == "tampered" {
		t.Error("Catalog() hands out the package's own slice")
	}

	roles := authz.OrganizationRoles()
	if len(roles) == 0 || len(roles[0].Permissions) == 0 {
		t.Fatal("OrganizationRoles() returned nothing to tamper with")
	}

	roles[0].Permissions[0] = "tampered"

	if authz.OrganizationRoles()[0].Permissions[0] == "tampered" {
		t.Error("OrganizationRoles() shares its Permissions backing array with package state")
	}
}

func TestSanitizeDropsUnknownAndDuplicateKeys(t *testing.T) {
	got := authz.Sanitize([]string{
		string(authz.PermMembersRead),
		"members.read",
		"notes.publish",
		"",
		string(authz.PermRolesRead),
	})

	want := []authz.Permission{authz.PermMembersRead, authz.PermRolesRead}
	if !slices.Equal(got, want) {
		t.Errorf("Sanitize() = %v, want %v", got, want)
	}
}

// TestSanitizeIsHowStaleRowsStopGranting pins the direction of the failure. A
// permission dropped from the catalog must stop conferring anything, even while
// its rows are still in role_permissions.
func TestSanitizeIsHowStaleRowsStopGranting(t *testing.T) {
	stale := []string{"organization.teleport"}

	if got := authz.Sanitize(stale); len(got) != 0 {
		t.Errorf("Sanitize(%v) = %v, want nothing", stale, got)
	}

	if got := authz.UnknownKeys(stale); !slices.Equal(got, stale) {
		t.Errorf("UnknownKeys(%v) = %v, want the drift reported", stale, got)
	}
}

func TestGrantAnswersNothingWhenNil(t *testing.T) {
	var g *authz.Grant

	if g.Has(authz.PermMembersRead) {
		t.Error("a nil Grant granted a permission")
	}

	if g.Covers([]authz.Permission{authz.PermMembersRead}) {
		t.Error("a nil Grant covered a permission")
	}

	if g.Actor() != uuid.Nil || g.OrganizationID() != uuid.Nil {
		t.Error("a nil Grant reported an identity")
	}
}

func TestGrantDropsUnknownPermissions(t *testing.T) {
	id := ids(t, 1)[0]

	g := authz.NewGrant(id, id, []authz.Permission{authz.PermRolesRead, "organization.teleport"})

	if g.Has("organization.teleport") {
		t.Error("NewGrant kept a permission that is not in the catalog")
	}

	if !g.Has(authz.PermRolesRead) {
		t.Error("NewGrant dropped a permission that is in the catalog")
	}
}

// TestEnsureCanGrantStopsPrivilegeEscalation is the most important rule in the
// package. Without it, roles.update is a permission to acquire every other one.
func TestEnsureCanGrantStopsPrivilegeEscalation(t *testing.T) {
	id := ids(t, 1)[0]

	editor := authz.NewGrant(id, id, []authz.Permission{
		authz.PermRolesRead,
		authz.PermRolesUpdate,
		authz.PermMembersRead,
	})

	t.Run("a subset of what the editor holds is allowed", func(t *testing.T) {
		err := authz.EnsureCanGrant(editor, []authz.Permission{authz.PermMembersRead, authz.PermRolesRead})
		if err != nil {
			t.Fatalf("EnsureCanGrant() = %v, want nil", err)
		}
	})

	t.Run("a permission the editor lacks is refused", func(t *testing.T) {
		err := authz.EnsureCanGrant(editor, []authz.Permission{authz.PermMembersRemove})
		if !errors.Is(err, authz.ErrPrivilegeEscalation) {
			t.Fatalf("EnsureCanGrant() = %v, want ErrPrivilegeEscalation", err)
		}
	})

	t.Run("one held permission does not carry an unheld one along", func(t *testing.T) {
		err := authz.EnsureCanGrant(editor, []authz.Permission{
			authz.PermRolesRead,
			authz.PermOrganizationDelete,
		})
		if !errors.Is(err, authz.ErrPrivilegeEscalation) {
			t.Fatalf("EnsureCanGrant() = %v, want ErrPrivilegeEscalation", err)
		}
	})

	t.Run("an unknown permission is refused rather than ignored", func(t *testing.T) {
		err := authz.EnsureCanGrant(editor, []authz.Permission{"organization.teleport"})
		if !errors.Is(err, authz.ErrUnknownPermission) {
			t.Fatalf("EnsureCanGrant() = %v, want ErrUnknownPermission", err)
		}
	})

	t.Run("a nil grant may grant nothing", func(t *testing.T) {
		err := authz.EnsureCanGrant(nil, []authz.Permission{authz.PermRolesRead})
		if !errors.Is(err, authz.ErrPrivilegeEscalation) {
			t.Fatalf("EnsureCanGrant(nil, ...) = %v, want ErrPrivilegeEscalation", err)
		}
	})
}

func TestAuthorizeReturnsTheWholeGrant(t *testing.T) {
	got := ids(t, 2)
	user, org := got[0], got[1]

	svc := authz.NewService(&stubRepo{
		orgKeys: map[uuid.UUID][]string{
			key(user, org): {string(authz.PermMembersRead), string(authz.PermRolesRead)},
		},
	})

	grant, err := svc.Authorize(t.Context(), authz.Request{
		Actor:      user,
		Org:        org,
		Permission: authz.PermMembersRead,
	})
	if err != nil {
		t.Fatalf("Authorize() = _, %v", err)
	}

	if grant.OrganizationID() != org {
		t.Errorf("grant.OrganizationID() = %v, want %v", grant.OrganizationID(), org)
	}

	// The point of returning a Grant rather than a bool: the second question is
	// answered without a second query.
	if !grant.Has(authz.PermRolesRead) {
		t.Error("grant does not carry the permissions that were not asked about")
	}
}

func TestAuthorizeRefusesWhatIsNotGranted(t *testing.T) {
	got := ids(t, 2)
	user, org := got[0], got[1]

	svc := authz.NewService(&stubRepo{
		orgKeys: map[uuid.UUID][]string{key(user, org): {string(authz.PermMembersRead)}},
	})

	grant, err := svc.Authorize(t.Context(), authz.Request{
		Actor:      user,
		Org:        org,
		Permission: authz.PermOrganizationDelete,
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Authorize() = _, %v, want ErrForbidden", err)
	}

	if grant != nil {
		t.Error("Authorize returned a grant alongside a refusal")
	}
}

// TestAuthorizeReportsNonMembershipDistinctlyFromRefusal is what lets the API
// answer 404 for a stranger and 403 for a member, without the handler having to
// know the difference.
func TestAuthorizeReportsNonMembershipDistinctlyFromRefusal(t *testing.T) {
	got := ids(t, 3)
	user, org, other := got[0], got[1], got[2]

	svc := authz.NewService(&stubRepo{
		orgKeys: map[uuid.UUID][]string{key(user, org): {string(authz.PermMembersRead)}},
	})

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor:      user,
		Org:        other,
		Permission: authz.PermMembersRead,
	})
	if !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("Authorize() into a foreign organization = %v, want ErrNotMember", err)
	}
}

func TestAuthorizeRejectsAScopeMismatch(t *testing.T) {
	got := ids(t, 2)
	user, org := got[0], got[1]

	svc := authz.NewService(&stubRepo{})

	t.Run("organization permission without an organization", func(t *testing.T) {
		_, err := svc.Authorize(t.Context(), authz.Request{
			Actor:      user,
			Permission: authz.PermMembersRead,
		})
		if !errors.Is(err, authz.ErrScopeMismatch) {
			t.Fatalf("Authorize() = %v, want ErrScopeMismatch", err)
		}
	})

	t.Run("system permission inside an organization", func(t *testing.T) {
		_, err := svc.Authorize(t.Context(), authz.Request{
			Actor:      user,
			Org:        org,
			Permission: authz.PermPlatformUsersRead,
		})
		if !errors.Is(err, authz.ErrScopeMismatch) {
			t.Fatalf("Authorize() = %v, want ErrScopeMismatch", err)
		}
	})

	t.Run("a permission that does not exist", func(t *testing.T) {
		_, err := svc.Authorize(t.Context(), authz.Request{
			Actor:      user,
			Org:        org,
			Permission: "organization.teleport",
		})
		if !errors.Is(err, authz.ErrUnknownPermission) {
			t.Fatalf("Authorize() = %v, want ErrUnknownPermission", err)
		}
	})
}

func TestAuthorizeResolvesSystemRolesFromTheCatalog(t *testing.T) {
	user := ids(t, 1)[0]

	svc := authz.NewService(&stubRepo{
		systemKeys: map[uuid.UUID][]string{
			user: {string(authz.RolePlatformAdmin)},
		},
	})

	grant, err := svc.Authorize(t.Context(), authz.Request{
		Actor:      user,
		Permission: authz.PermPlatformUsersRead,
	})
	if err != nil {
		t.Fatalf("Authorize() = _, %v", err)
	}

	if grant.OrganizationID() != uuid.Nil {
		t.Errorf("system grant carries organization %v", grant.OrganizationID())
	}
}

// TestPlatformAdminHasNoStandingInsideAnOrganization pins a decision that is
// easy to reverse by accident. Support access to a tenant is its own feature
// with its own audit trail, not a side effect of a role named "platform admin".
func TestPlatformAdminHasNoStandingInsideAnOrganization(t *testing.T) {
	got := ids(t, 2)
	user, org := got[0], got[1]

	svc := authz.NewService(&stubRepo{
		systemKeys: map[uuid.UUID][]string{user: {string(authz.RolePlatformAdmin)}},
	})

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor:      user,
		Org:        org,
		Permission: authz.PermMembersRead,
	})
	if !errors.Is(err, authz.ErrNotMember) {
		t.Fatalf("Authorize() = %v, want ErrNotMember", err)
	}
}

func TestAuthorizeIgnoresSystemRoleKeysItDoesNotShip(t *testing.T) {
	user := ids(t, 1)[0]

	svc := authz.NewService(&stubRepo{
		systemKeys: map[uuid.UUID][]string{
			// A role removed from the build, and an organization role stored in
			// the wrong table. Neither may grant anything.
			user: {"legacy_superuser", string(authz.RoleOwner)},
		},
	})

	_, err := svc.Authorize(t.Context(), authz.Request{
		Actor:      user,
		Permission: authz.PermPlatformUsersRead,
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Authorize() = %v, want ErrForbidden", err)
	}
}

func TestGrantTravelsOnTheContext(t *testing.T) {
	id := ids(t, 1)[0]
	grant := authz.NewGrant(id, id, []authz.Permission{authz.PermMembersRead})

	got, ok := authz.GrantFrom(authz.WithGrant(t.Context(), grant))
	if !ok {
		t.Fatal("GrantFrom() = _, false after WithGrant")
	}

	if !got.Has(authz.PermMembersRead) {
		t.Error("the grant lost its permissions in transit")
	}
}

// TestGrantFromRefusesAnEmptyContext is the fail-closed half. A handler that
// ignores the second result must end up with a Grant that permits nothing.
func TestGrantFromRefusesAnEmptyContext(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no grant":    t.Context(),
		"nil grant":   authz.WithGrant(t.Context(), nil),
		"empty grant": authz.WithGrant(t.Context(), authz.NewGrant(uuid.Nil, uuid.Nil, nil)),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := authz.GrantFrom(ctx)
			if ok {
				t.Fatalf("GrantFrom() = _, true, want false")
			}

			if got.Has(authz.PermMembersRead) {
				t.Error("the zero grant permits something")
			}
		})
	}
}
