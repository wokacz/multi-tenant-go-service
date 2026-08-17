// Package contract_test runs one set of cases against every implementation of the
// repository interfaces.
//
// It exists because the in-memory fake reimplements the semantics of the SQL by
// hand, and the two drifted. A membership whose account had been deleted was a
// member to one and not to the other; the last-owner rule counted it differently in
// each; and because the API tests only ever ran against the fake, everything passed
// while Postgres behaved another way. Fixing those one at a time fixes instances.
// This fixes the class: a case written here has to hold in both, or the build breaks.
//
// The fake half runs everywhere. The Postgres half needs a database:
//
//	POSTGRES_TEST=1 POSTGRES_PORT=... go test ./internal/store/repositories/contract
package contract_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// backend is one implementation, plus the few fixtures a case cannot build through
// the interfaces themselves.
//
// The fixtures are the whole reason this needs an abstraction: creating an account
// or soft-deleting one is not something orgs.Repository can do, and the two
// implementations do it in completely different ways. Everything a case asserts on,
// it reaches through the interfaces — otherwise the cases would be testing the
// fixtures.
type backend struct {
	name string

	repo  orgs.Repository
	dir   orgs.Directory
	prov  orgs.Provisioner
	perms authz.Repository

	newAccount    func(t *testing.T) (uuid.UUID, string)
	deleteAccount func(t *testing.T, userID uuid.UUID)
	newOrg        func(t *testing.T) uuid.UUID
	deleteOrg     func(t *testing.T, orgID uuid.UUID)
	newRole       func(t *testing.T, orgID uuid.UUID, key string, permissions ...string) uuid.UUID
}

// eachBackend runs fn against every implementation available in this environment.
func eachBackend(t *testing.T, fn func(t *testing.T, b *backend)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		fn(t, newMemoryBackend(t))
	})

	t.Run("postgres", func(t *testing.T) {
		fn(t, newPostgresBackend(t))
	})
}

func newMemoryBackend(t *testing.T) *backend {
	t.Helper()

	users := memory.NewUsers()
	repo := memory.NewAuthz(users)

	return &backend{
		name:  "memory",
		repo:  repo,
		dir:   repo,
		prov:  repo,
		perms: repo,

		newAccount: func(t *testing.T) (uuid.UUID, string) {
			t.Helper()

			u := &models.User{
				Name:         "Ada",
				Email:        "ada+" + uuid.Must(uuid.NewV7()).String() + "@example.com",
				PasswordHash: "not-a-real-hash",
			}
			if err := users.Create(t.Context(), u); err != nil {
				t.Fatalf("create account: %v", err)
			}

			return u.ID, u.Email
		},
		deleteAccount: func(t *testing.T, userID uuid.UUID) {
			t.Helper()

			repo.SeedSoftDeletedUser(userID)
		},
		newOrg: func(t *testing.T) uuid.UUID {
			t.Helper()

			return repo.SeedOrganization("org-"+uuid.Must(uuid.NewV7()).String(), "Org")
		},
		deleteOrg: func(t *testing.T, orgID uuid.UUID) {
			t.Helper()

			repo.SeedSoftDeletedOrganization(orgID)
		},
		newRole: func(t *testing.T, orgID uuid.UUID, key string, permissions ...string) uuid.UUID {
			t.Helper()

			return repo.SeedRole(orgID, key, permissions...)
		},
	}
}

func newPostgresBackend(t *testing.T) *backend {
	t.Helper()

	if os.Getenv("POSTGRES_TEST") == "" {
		t.Skip("set POSTGRES_TEST=1 to run the contract against the real store")
	}

	cfg := &config.Config{
		PostgresHost:         envOr("POSTGRES_HOST", "localhost"),
		PostgresPort:         envOrInt("POSTGRES_PORT", 5432),
		PostgresUser:         envOr("POSTGRES_USER", "postgres"),
		PostgresPassword:     envOr("POSTGRES_PASSWORD", "postgres"),
		PostgresDatabaseName: envOr("POSTGRES_DATABASE_NAME", "postgres"),
		PostgresSSLMode:      envOr("POSTGRES_SSL_MODE", "disable"),
		DBMaxOpenConns:       4,
		DBMaxIdleConns:       4,
		DBConnectTimeout:     5 * time.Second,
		DBSlowQueryThreshold: time.Second,
	}

	db, err := store.OpenPostgres(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("OpenPostgres() = %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	repo := repositories.NewOrgs(db)
	users := repositories.NewUser(db)

	return &backend{
		name:  "postgres",
		repo:  repo,
		dir:   repo,
		prov:  repo,
		perms: repositories.NewAuthz(db),

		newAccount: func(t *testing.T) (uuid.UUID, string) {
			t.Helper()

			u := &models.User{
				Name:         "Ada",
				Email:        "ada+" + uuid.Must(uuid.NewV7()).String() + "@example.com",
				PasswordHash: "not-a-real-hash",
			}
			if err := users.Create(t.Context(), u); err != nil {
				t.Fatalf("create account: %v", err)
			}

			return u.ID, u.Email
		},
		deleteAccount: func(t *testing.T, userID uuid.UUID) {
			t.Helper()

			if err := users.Delete(t.Context(), userID); err != nil {
				t.Fatalf("delete account: %v", err)
			}
		},
		newOrg: func(t *testing.T) uuid.UUID {
			t.Helper()

			org := &models.Organization{
				Slug: "org-" + uuid.Must(uuid.NewV7()).String(),
				Name: "Org",
			}

			created, err := repo.CreateOrganization(t.Context(), org, nil)
			if err != nil {
				t.Fatalf("create organization: %v", err)
			}

			return created.ID
		},
		deleteOrg: func(t *testing.T, orgID uuid.UUID) {
			t.Helper()

			if err := repo.DeleteOrganization(t.Context(), orgID); err != nil {
				t.Fatalf("delete organization: %v", err)
			}
		},
		newRole: func(t *testing.T, orgID uuid.UUID, key string, permissions ...string) uuid.UUID {
			t.Helper()

			perms := make([]authz.Permission, 0, len(permissions))
			for _, p := range permissions {
				perms = append(perms, authz.Permission(p))
			}

			role, err := repo.CreateRole(t.Context(), orgID,
				&models.Role{Key: key, Name: key}, perms)
			if err != nil {
				t.Fatalf("create role: %v", err)
			}

			return role.ID
		},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func envOrInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}

	return value
}

// addMember is the shorthand every case needs: an account, in an organization,
// holding roles.
func addMember(t *testing.T, b *backend, orgID uuid.UUID, roleIDs ...uuid.UUID) (memberID, userID uuid.UUID) {
	t.Helper()

	userID, _ = b.newAccount(t)

	member, err := b.repo.AddMember(t.Context(), orgID, userID, roleIDs, uuid.Nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("AddMember() = _, %v", err)
	}

	return member.ID, userID
}

// TestADeletedAccountIsNotAMember is the divergence that motivated this package.
//
// Soft deleting an account does not fire the foreign key cascade, so the membership
// row outlives its person. Postgres listed it — the condition sat in a LEFT JOIN
// where it filtered nothing — and the fake did not, so the API tests passed while
// the real store behaved differently.
func TestADeletedAccountIsNotAMember(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "viewer", string(authz.PermOrganizationRead))

		liveMember, _ := addMember(t, b, orgID, role)
		goneMember, goneUser := addMember(t, b, orgID, role)

		b.deleteAccount(t, goneUser)

		members, err := b.repo.Members(t.Context(), orgID)
		if err != nil {
			t.Fatalf("Members() = _, %v", err)
		}

		ids := make([]string, 0, len(members))
		for i := range members {
			ids = append(ids, members[i].ID.String())
		}

		slices.Sort(ids)

		if !slices.Equal(ids, []string{liveMember.String()}) {
			t.Errorf("Members() = %v, want only the live account's membership %v", ids, liveMember)
		}

		if _, err := b.repo.Member(t.Context(), orgID, goneMember); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("Member() for a deleted account = %v, want ErrNotFound", err)
		}

		if _, err := b.repo.MemberByUser(t.Context(), orgID, goneUser); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("MemberByUser() for a deleted account = %v, want ErrNotFound", err)
		}

		// Removal is the one thing that must still work on such a row: everything
		// else reports it as missing, so refusing here too would leave it in the
		// organization with no way to take it out.
		if err := b.repo.RemoveMember(t.Context(), orgID, goneMember, orgs.RefuseLastOwnerLoss(true)); err != nil {
			t.Errorf("RemoveMember() for a deleted account = %v, want it removed", err)
		}
	})
}

// TestTheOwnerStateBothSidesSee is the other half of the same divergence.
//
// The rule is domain code now, but the facts it decides from are assembled by each
// implementation. They disagreed about a deleted account once, which made the row
// impossible to remove: it counted as the owner being lost and never as an owner
// who exists.
func TestTheOwnerStateBothSidesSee(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		owner := b.newRole(t, orgID, string(authz.RoleOwner), string(authz.PermOrganizationDelete))
		viewer := b.newRole(t, orgID, string(authz.RoleViewer), string(authz.PermOrganizationRead))

		liveOwner, _ := addMember(t, b, orgID, owner)
		plain, _ := addMember(t, b, orgID, viewer)

		goneOwner, goneUser := addMember(t, b, orgID, owner)
		b.deleteAccount(t, goneUser)

		suspended, _ := addMember(t, b, orgID, owner)
		if err := b.repo.SetMemberStatus(t.Context(), orgID, suspended,
			models.MembershipSuspended, time.Now().UTC(), orgs.RefuseLastOwnerLoss(true)); err != nil {
			t.Fatalf("SetMemberStatus(suspended) = %v", err)
		}

		errStop := errors.New("stop")

		state := func(memberID uuid.UUID) orgs.OwnerState {
			t.Helper()

			var seen orgs.OwnerState

			err := b.repo.RemoveMember(t.Context(), orgID, memberID, func(s orgs.OwnerState) error {
				seen = s

				return errStop
			})
			if !errors.Is(err, errStop) {
				t.Fatalf("RemoveMember() = %v, want the guard's own error back", err)
			}

			return seen
		}

		// One owner: the live one. Neither the deleted account nor the suspended
		// membership counts.
		if got := state(liveOwner); got.Owners != 1 || !got.SubjectHoldsOwner {
			t.Errorf("state for the live owner = %+v, want {Owners:1 SubjectHoldsOwner:true}", got)
		}

		if got := state(plain); got.Owners != 1 || got.SubjectHoldsOwner {
			t.Errorf("state for a viewer = %+v, want {Owners:1 SubjectHoldsOwner:false}", got)
		}

		if got := state(goneOwner); got.SubjectHoldsOwner {
			t.Errorf("state for an owner whose account is deleted = %+v, want SubjectHoldsOwner:false", got)
		}
	})
}

// TestOnlyAnActiveMembershipResolves pins the answer the middleware turns into a
// 404. A suspended member and a stranger must be indistinguishable.
func TestOnlyAnActiveMembershipResolves(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "reader", string(authz.PermMembersRead))

		memberID, userID := addMember(t, b, orgID, role)

		keys, err := b.perms.OrganizationPermissionKeys(t.Context(), userID, orgID)
		if err != nil {
			t.Fatalf("OrganizationPermissionKeys() = _, %v", err)
		}

		if !slices.Contains(keys, string(authz.PermMembersRead)) {
			t.Errorf("keys = %v, want members.read", keys)
		}

		if err := b.repo.SetMemberStatus(t.Context(), orgID, memberID,
			models.MembershipSuspended, time.Now().UTC(), orgs.RefuseLastOwnerLoss(true)); err != nil {
			t.Fatalf("SetMemberStatus(suspended) = %v", err)
		}

		if _, err := b.perms.OrganizationPermissionKeys(t.Context(), userID, orgID); !errors.Is(err, authz.ErrNotMember) {
			t.Errorf("a suspended membership resolves to %v, want ErrNotMember", err)
		}

		// A member holding no roles is a member, and that has to stay distinct
		// from a stranger: an empty grant is a 403, ErrNotMember is a 404.
		bare, bareUser := addMember(t, b, orgID)
		_ = bare

		bareKeys, err := b.perms.OrganizationPermissionKeys(t.Context(), bareUser, orgID)
		if err != nil {
			t.Fatalf("a member with no roles resolves to %v, want an empty grant", err)
		}

		if len(bareKeys) != 0 {
			t.Errorf("keys = %v, want none", bareKeys)
		}
	})
}

// TestASoftDeletedOrganizationGrantsNothing keeps a deleted tenant from answering.
func TestASoftDeletedOrganizationGrantsNothing(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "reader", string(authz.PermMembersRead))
		_, userID := addMember(t, b, orgID, role)

		b.deleteOrg(t, orgID)

		if _, err := b.perms.OrganizationPermissionKeys(t.Context(), userID, orgID); !errors.Is(err, authz.ErrNotMember) {
			t.Errorf("a deleted organization resolves to %v, want ErrNotMember", err)
		}

		memberships, err := b.dir.MembershipsForUser(t.Context(), userID)
		if err != nil {
			t.Fatalf("MembershipsForUser() = _, %v", err)
		}

		for i := range memberships {
			if memberships[i].Organization.ID == orgID {
				t.Errorf("a deleted organization is still listed for the account")
			}
		}
	})
}

// TestAnInvitationTravelsFromOfferToMembership covers the newest surface, and the
// one that has never had both implementations compared.
func TestAnInvitationTravelsFromOfferToMembership(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "viewer", string(authz.PermOrganizationRead))

		now := time.Now().UTC()
		token := "contract-" + uuid.Must(uuid.NewV7()).String()
		invitee := "invitee+" + uuid.Must(uuid.NewV7()).String() + "@example.com"

		invitation, err := b.repo.InviteMember(t.Context(), orgID, invitee,
			orgs.HashInvitationToken(token), []uuid.UUID{role}, uuid.Nil, now.Add(orgs.InvitationTTL), now)
		if err != nil {
			t.Fatalf("InviteMember() = _, %v", err)
		}

		if invitation.Email != invitee {
			t.Errorf("email = %q, want %q", invitation.Email, invitee)
		}

		if !slices.Equal(invitation.RoleKeys, []string{"viewer"}) {
			t.Errorf("roles = %v, want [viewer]", invitation.RoleKeys)
		}

		// Inviting is not joining.
		members, err := b.repo.Members(t.Context(), orgID)
		if err != nil {
			t.Fatalf("Members() = _, %v", err)
		}

		if len(members) != 0 {
			t.Errorf("Members() = %+v, want none — an offer is not a membership", members)
		}

		// Reachable by the token, and only by the token.
		found, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken(token), now)
		if err != nil {
			t.Fatalf("InvitationByToken() = _, %v", err)
		}

		if found.ID != invitation.ID {
			t.Errorf("InvitationByToken() = %v, want %v", found.ID, invitation.ID)
		}

		if _, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken("wrong"), now); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("a wrong token = %v, want ErrNotFound", err)
		}

		// Accepting creates the membership the offer promised, with the offer's
		// roles, and spends the invitation.
		userID, _ := b.newAccount(t)
		if err := b.dir.AcceptInvitation(t.Context(), invitation.ID, userID, now); err != nil {
			t.Fatalf("AcceptInvitation() = %v", err)
		}

		member, err := b.repo.MemberByUser(t.Context(), orgID, userID)
		if err != nil {
			t.Fatalf("MemberByUser() after accepting = _, %v", err)
		}

		if member.Status != models.MembershipActive {
			t.Errorf("status = %q, want active", member.Status)
		}

		keys := make([]string, 0, len(member.Roles))
		for _, r := range member.Roles {
			keys = append(keys, r.Key)
		}

		if !slices.Equal(keys, []string{"viewer"}) {
			t.Errorf("roles = %v, want [viewer] — they come from the invitation", keys)
		}

		if _, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken(token), now); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("an accepted invitation = %v, want ErrNotFound", err)
		}
	})
}

// TestAnExpiredInvitationIsHiddenByTheClock is what the service leans on to tell
// "expired" apart from "never existed": the lookup filters on the clock, and a zero
// time ignores it.
func TestAnExpiredInvitationIsHiddenByTheClock(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		now := time.Now().UTC()
		token := "expired-" + uuid.Must(uuid.NewV7()).String()
		invitee := "expired+" + uuid.Must(uuid.NewV7()).String() + "@example.com"

		if _, err := b.repo.InviteMember(t.Context(), orgID, invitee,
			orgs.HashInvitationToken(token), nil, uuid.Nil, now.Add(-time.Hour), now); err != nil {
			t.Fatalf("InviteMember() = _, %v", err)
		}

		if _, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken(token), now); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("an expired invitation = %v, want ErrNotFound", err)
		}

		// The same row is visible without the clock, which is how the domain
		// reports "expired" rather than "no such invitation".
		if _, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken(token), time.Time{}); err != nil {
			t.Errorf("InvitationByToken() ignoring the clock = %v, want the row", err)
		}
	})
}

// TestReissuingAnInvitationInvalidatesTheOldToken is the property that makes
// "send it again" safe: a leaked link must not survive it.
func TestReissuingAnInvitationInvalidatesTheOldToken(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		now := time.Now().UTC()
		first := "first-" + uuid.Must(uuid.NewV7()).String()
		second := "second-" + uuid.Must(uuid.NewV7()).String()
		invitee := "reissue+" + uuid.Must(uuid.NewV7()).String() + "@example.com"

		invitation, err := b.repo.InviteMember(t.Context(), orgID, invitee,
			orgs.HashInvitationToken(first), nil, uuid.Nil, now.Add(orgs.InvitationTTL), now)
		if err != nil {
			t.Fatalf("InviteMember() = _, %v", err)
		}

		if _, err := b.dir.ReissueInvitation(t.Context(), orgID, invitation.ID,
			orgs.HashInvitationToken(second), now.Add(orgs.InvitationTTL)); err != nil {
			t.Fatalf("ReissueInvitation() = _, %v", err)
		}

		if _, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken(first), now); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("the old token still works: %v", err)
		}

		if _, err := b.dir.InvitationByToken(t.Context(), orgs.HashInvitationToken(second), now); err != nil {
			t.Errorf("the new token = %v, want the row", err)
		}
	})
}

// TestAnInvitationIsScopedToItsOrganization is the tenancy rule on the new table.
func TestAnInvitationIsScopedToItsOrganization(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		foreign := b.newOrg(t)

		now := time.Now().UTC()
		token := "scoped-" + uuid.Must(uuid.NewV7()).String()
		invitee := "scoped+" + uuid.Must(uuid.NewV7()).String() + "@example.com"

		invitation, err := b.repo.InviteMember(t.Context(), orgID, invitee,
			orgs.HashInvitationToken(token), nil, uuid.Nil, now.Add(orgs.InvitationTTL), now)
		if err != nil {
			t.Fatalf("InviteMember() = _, %v", err)
		}

		if err := b.dir.WithdrawInvitation(t.Context(), foreign, invitation.ID); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("withdrawing from another organization = %v, want ErrNotFound", err)
		}

		if _, err := b.dir.ReissueInvitation(t.Context(), foreign, invitation.ID,
			orgs.HashInvitationToken("other"), now.Add(orgs.InvitationTTL)); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("reissuing from another organization = %v, want ErrNotFound", err)
		}

		// And it is still there, so the refusals refused rather than half-acting.
		if err := b.dir.WithdrawInvitation(t.Context(), orgID, invitation.ID); err != nil {
			t.Errorf("WithdrawInvitation() from its own organization = %v", err)
		}
	})
}

// TestARoleFromAnotherOrganizationCannotBeAssigned is the scope check that a
// foreign key cannot make: it only says the role exists somewhere.
func TestARoleFromAnotherOrganizationCannotBeAssigned(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		foreign := b.newOrg(t)

		mine := b.newRole(t, orgID, "readers", string(authz.PermMembersRead))
		theirs := b.newRole(t, foreign, "readers", string(authz.PermMembersRead))

		memberID, _ := addMember(t, b, orgID)

		err := b.repo.ReplaceMemberRoles(t.Context(), orgID, memberID,
			[]uuid.UUID{mine, theirs}, orgs.RefuseLastOwnerLoss(true))
		if !errors.Is(err, orgs.ErrNotFound) {
			t.Fatalf("ReplaceMemberRoles() with a foreign role = %v, want ErrNotFound", err)
		}

		// Nothing landed: the legitimate half must not survive a refused call.
		member, err := b.repo.Member(t.Context(), orgID, memberID)
		if err != nil {
			t.Fatalf("Member() = _, %v", err)
		}

		if len(member.Roles) != 0 {
			t.Errorf("roles = %+v, want none", member.Roles)
		}
	})
}

// TestTheRoleGuardSeesTheHolderCount pins the number the domain's rule decides
// from, in both implementations.
func TestTheRoleGuardSeesTheHolderCount(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		role := b.newRole(t, orgID, "auditor", string(authz.PermMembersRead))

		addMember(t, b, orgID, role)
		addMember(t, b, orgID, role)

		var seen int

		errStop := errors.New("stop")

		err := b.repo.DeleteRole(t.Context(), orgID, role, func(holders int) error {
			seen = holders

			return errStop
		})
		if !errors.Is(err, errStop) {
			t.Fatalf("DeleteRole() = %v, want the guard's own error back", err)
		}

		if seen != 2 {
			t.Errorf("holders = %d, want 2", seen)
		}

		// Abandoning the guard takes the delete with it.
		if _, err := b.repo.Role(t.Context(), orgID, role); err != nil {
			t.Errorf("Role() after a refused delete = %v, want it still there", err)
		}

		if err := b.repo.DeleteRole(t.Context(), orgID, role, orgs.RefuseRoleInUse()); !errors.Is(err, orgs.ErrRoleInUse) {
			t.Errorf("DeleteRole() with the real rule = %v, want ErrRoleInUse", err)
		}
	})
}
