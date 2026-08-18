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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// wholePage is the limit a case passes when the page is not what it is testing.
// The listings are paged, and the repository does not clamp a non-positive limit
// into "everything" — see orgs.Repository — so a case that just wants the rows has
// to name a number.
const wholePage = 1000

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
	users user.Repository

	// audit reads the history back. Both implementations put it on the same type as
	// the organization repository; the field exists so a case does not have to know
	// that.
	audit audit.Reader

	platformAudit audit.PlatformReader

	newNamedAccount func(t *testing.T, name string) (uuid.UUID, string)
	registerEmail   func(t *testing.T, email string) error
	newOrgSlug      func(t *testing.T, slug string) error
	deleteAccount   func(t *testing.T, userID uuid.UUID)
	newOrg          func(t *testing.T) uuid.UUID

	// newProtectedOrg is the default organization's shape: one that refuses
	// deletion. The two implementations reach it differently — a seed helper on one
	// side, the column on the other — which is exactly what a fixture is for.
	newProtectedOrg func(t *testing.T) uuid.UUID
	deleteOrg       func(t *testing.T, orgID uuid.UUID)
	newRole         func(t *testing.T, orgID uuid.UUID, key string, permissions ...string) uuid.UUID

	// newShippedRole materialises one of the catalog's roles, which is the only way
	// a case can get its hands on a role with is_system set — and the role listing
	// orders by that column before it orders by key.
	newShippedRole func(t *testing.T, orgID uuid.UUID, key authz.RoleKey) uuid.UUID
}

// newAccount is the common case: an account whose name no case looks at.
func (b *backend) newAccount(t *testing.T) (uuid.UUID, string) {
	t.Helper()

	return b.newNamedAccount(t, "Ada")
}

// accountFixture is shared because both implementations build an account through
// the same interface. They each had their own identical copy of this.
func accountFixture(users user.Repository) func(*testing.T, string) (uuid.UUID, string) {
	return func(t *testing.T, name string) (uuid.UUID, string) {
		t.Helper()

		u := &models.User{
			Name:         name,
			Email:        "ada+" + uuid.Must(uuid.NewV7()).String() + "@example.com",
			PasswordHash: "not-a-real-hash",
		}
		if err := users.Create(t.Context(), u); err != nil {
			t.Fatalf("create account: %v", err)
		}

		return u.ID, u.Email
	}
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
		users: users,
		audit: repo,

		platformAudit: repo,

		newNamedAccount: accountFixture(users),
		registerEmail: func(t *testing.T, email string) error {
			t.Helper()

			return users.Create(t.Context(), &models.User{
				Name: "Bob", Email: email, PasswordHash: "not-a-real-hash",
			})
		},
		newOrgSlug: func(t *testing.T, slug string) error {
			t.Helper()

			_, err := repo.CreateOrganization(t.Context(), &models.Organization{Slug: slug, Name: "Org"}, nil)

			return err
		},
		deleteAccount: func(t *testing.T, userID uuid.UUID) {
			t.Helper()

			// One call, like the SQL. It used to tell the authorization fake
			// separately, which meant the fixture was compensating for the fake not
			// noticing a deleted account — and every case here passed while anything
			// that could not reach for a fixture did not.
			if err := users.Delete(t.Context(), userID); err != nil {
				t.Fatalf("delete account: %v", err)
			}
		},
		newOrg: func(t *testing.T) uuid.UUID {
			t.Helper()

			return repo.SeedOrganization("org-"+uuid.Must(uuid.NewV7()).String(), "Org")
		},
		newProtectedOrg: func(t *testing.T) uuid.UUID {
			t.Helper()

			return repo.SeedProtectedOrganization("protected-"+uuid.Must(uuid.NewV7()).String(), "Protected")
		},
		deleteOrg: func(t *testing.T, orgID uuid.UUID) {
			t.Helper()

			repo.SeedSoftDeletedOrganization(orgID)
		},
		newRole: func(t *testing.T, orgID uuid.UUID, key string, permissions ...string) uuid.UUID {
			t.Helper()

			return repo.SeedRole(orgID, key, permissions...)
		},
		newShippedRole: func(t *testing.T, orgID uuid.UUID, key authz.RoleKey) uuid.UUID {
			t.Helper()

			return repo.SeedShippedRole(orgID, key)
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
		users: users,
		audit: repo,

		platformAudit: repo,

		newNamedAccount: accountFixture(users),
		registerEmail: func(t *testing.T, email string) error {
			t.Helper()

			return users.Create(t.Context(), &models.User{
				Name: "Bob", Email: email, PasswordHash: "not-a-real-hash",
			})
		},
		newOrgSlug: func(t *testing.T, slug string) error {
			t.Helper()

			_, err := repo.CreateOrganization(t.Context(), &models.Organization{Slug: slug, Name: "Org"}, nil)

			return err
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
		newProtectedOrg: func(t *testing.T) uuid.UUID {
			t.Helper()

			org := &models.Organization{
				Slug: "protected-" + uuid.Must(uuid.NewV7()).String(),
				Name: "Protected",
			}
			org.IsProtected = true

			created, err := repo.CreateOrganization(t.Context(), org, nil)
			if err != nil {
				t.Fatalf("create protected organization: %v", err)
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
		newShippedRole: func(t *testing.T, orgID uuid.UUID, key authz.RoleKey) uuid.UUID {
			t.Helper()

			def, ok := authz.LookupRole(key)
			if !ok {
				t.Fatalf("no shipped role named %q", key)
			}

			role, err := repo.CreateRole(t.Context(), orgID,
				&models.Role{Key: string(key), Name: def.Name, IsSystem: true}, def.Permissions)
			if err != nil {
				t.Fatalf("create shipped role: %v", err)
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

		members, err := b.repo.Members(t.Context(), orgID, wholePage, 0)
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
		if err := b.repo.RemoveMember(t.Context(), orgID, goneMember, models.ActionMemberRemoved, orgs.RefuseLastOwnerLoss(true)); err != nil {
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

			err := b.repo.RemoveMember(t.Context(), orgID, memberID, models.ActionMemberRemoved, func(s orgs.OwnerState) error {
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
		members, err := b.repo.Members(t.Context(), orgID, wholePage, 0)
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

		if _, err := b.repo.ReissueInvitation(t.Context(), orgID, invitation.ID,
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

		if err := b.repo.WithdrawInvitation(t.Context(), foreign, invitation.ID); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("withdrawing from another organization = %v, want ErrNotFound", err)
		}

		if _, err := b.repo.ReissueInvitation(t.Context(), foreign, invitation.ID,
			orgs.HashInvitationToken("other"), now.Add(orgs.InvitationTTL)); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("reissuing from another organization = %v, want ErrNotFound", err)
		}

		// And it is still there, so the refusals refused rather than half-acting.
		if err := b.repo.WithdrawInvitation(t.Context(), orgID, invitation.ID); err != nil {
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

// TestSystemRoleGrantsAgreeOnBothSides covers the installation-wide roles, which
// were written twice by hand — once in SQL, once in the fake — and had no test
// comparing them.
//
// Idempotence is the part worth pinning: the bootstrap command may run again, so a
// second grant must not fail, and it must not record a second event either. An entry
// for a grant that did not happen would be a second answer to "when did they get
// this".
func TestSystemRoleGrantsAgreeOnBothSides(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, email := b.newAccount(t)
		granter, _ := b.newAccount(t)

		if err := b.prov.GrantSystemRole(t.Context(), userID, authz.RolePlatformAdmin, granter); err != nil {
			t.Fatalf("GrantSystemRole() = %v", err)
		}

		holder := findHolder(t, b, userID)

		if holder.RoleKey != string(authz.RolePlatformAdmin) {
			t.Errorf("role_key = %q, want platform_admin", holder.RoleKey)
		}

		// The name and address are resolved, because "who administers this
		// installation" is a question about people rather than ids.
		if holder.Email != email {
			t.Errorf("email = %q, want %q", holder.Email, email)
		}

		if holder.GrantedBy == nil || *holder.GrantedBy != granter {
			t.Errorf("granted_by = %v, want %v", holder.GrantedBy, granter)
		}

		if holder.GrantedAt.IsZero() {
			t.Error("granted_at is zero; the listing cannot say when")
		}

		// Granting again is not an error and does not duplicate the row.
		if err := b.prov.GrantSystemRole(t.Context(), userID, authz.RolePlatformAdmin, granter); err != nil {
			t.Fatalf("GrantSystemRole() a second time = %v", err)
		}

		if got := countHolders(t, b, userID); got != 1 {
			t.Errorf("holders for one account = %d, want 1", got)
		}

		// A grant belonging to a deleted account confers nothing, the same rule
		// every membership lookup follows.
		b.deleteAccount(t, userID)

		if got := countHolders(t, b, userID); got != 0 {
			t.Errorf("a deleted account still holds %d installation roles", got)
		}
	})
}

// TestRevokingASystemRoleIsIdempotent pins the other half.
func TestRevokingASystemRoleIsIdempotent(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, _ := b.newAccount(t)

		if err := b.prov.GrantSystemRole(t.Context(), userID, authz.RolePlatformAdmin, uuid.Nil); err != nil {
			t.Fatalf("GrantSystemRole() = %v", err)
		}

		// Granted with no granter — the bootstrap path — so the column stays empty
		// rather than pointing at nobody.
		if holder := findHolder(t, b, userID); holder.GrantedBy != nil {
			t.Errorf("granted_by = %v, want it absent for a grant from the command line", holder.GrantedBy)
		}

		if err := b.prov.RevokeSystemRole(t.Context(), userID, authz.RolePlatformAdmin); err != nil {
			t.Fatalf("RevokeSystemRole() = %v", err)
		}

		if got := countHolders(t, b, userID); got != 0 {
			t.Errorf("the role is still held after revoking: %d", got)
		}

		// Revoking what is not held is not an error: the caller asked for a state
		// and that is the state they get.
		if err := b.prov.RevokeSystemRole(t.Context(), userID, authz.RolePlatformAdmin); err != nil {
			t.Errorf("RevokeSystemRole() a second time = %v, want nil", err)
		}
	})
}

func findHolder(t *testing.T, b *backend, userID uuid.UUID) orgs.SystemRoleHolder {
	t.Helper()

	holders, err := b.prov.SystemRoleHolders(t.Context())
	if err != nil {
		t.Fatalf("SystemRoleHolders() = _, %v", err)
	}

	for i := range holders {
		if holders[i].UserID == userID {
			return holders[i]
		}
	}

	t.Fatalf("account %v is not among the holders %+v", userID, holders)

	return orgs.SystemRoleHolder{}
}

func countHolders(t *testing.T, b *backend, userID uuid.UUID) int {
	t.Helper()

	holders, err := b.prov.SystemRoleHolders(t.Context())
	if err != nil {
		t.Fatalf("SystemRoleHolders() = _, %v", err)
	}

	count := 0

	for i := range holders {
		if holders[i].UserID == userID {
			count++
		}
	}

	return count
}

// TestAddMemberRefusesAnUnknownAccount is a divergence found while wiring the
// platform's "appoint an owner" endpoint, which takes an account id straight from
// the request.
//
// Postgres has a foreign key and refuses; the fake had nothing to check against and
// happily created a membership for an account that does not exist. Either way the
// caller deserves a not-found rather than a 500 from an untranslated driver error.
func TestAddMemberRefusesAnUnknownAccount(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		_, err := b.repo.AddMember(t.Context(), orgID, uuid.Must(uuid.NewV7()),
			nil, uuid.Nil, time.Now().UTC())
		if !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("AddMember() for an account that does not exist = %v, want ErrNotFound", err)
		}
	})
}

// TestADeletedAccountReleasesItsAddress is M9: the unique index is partial, so a
// soft delete stops occupying the address.
//
// A plain unique index held it for ever. Nobody could register it again, and because
// registration hides a duplicate behind 204 to avoid an enumeration oracle, the
// person trying was told it worked and could then never sign in — a dead end with no
// error anywhere to explain it.
func TestADeletedAccountReleasesItsAddress(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, email := b.newAccount(t)

		if err := b.registerEmail(t, email); err == nil {
			t.Fatal("registering a live account's address succeeded; the index is not unique")
		}

		b.deleteAccount(t, userID)

		if err := b.registerEmail(t, email); err != nil {
			t.Errorf("registering a deleted account's address = %v, want it free again", err)
		}
	})
}

// TestADeletedOrganizationReleasesItsSlug is the same rule for organizations, where
// the symptom was a 409 with nothing to explain it.
func TestADeletedOrganizationReleasesItsSlug(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		slug := "reuse-" + uuid.Must(uuid.NewV7()).String()

		if err := b.newOrgSlug(t, slug); err != nil {
			t.Fatalf("create organization: %v", err)
		}

		if err := b.newOrgSlug(t, slug); !errors.Is(err, orgs.ErrSlugTaken) {
			t.Fatalf("a second live organization with the same slug = %v, want ErrSlugTaken", err)
		}

		existing, err := b.prov.OrganizationBySlug(t.Context(), slug)
		if err != nil {
			t.Fatalf("OrganizationBySlug() = _, %v", err)
		}

		b.deleteOrg(t, existing.ID)

		if err := b.newOrgSlug(t, slug); err != nil {
			t.Errorf("reusing a deleted organization's slug = %v, want it free again", err)
		}
	})
}

// TestADeletedAccountIsNotFoundByEitherLookup pins a difference that only shows at
// this level.
//
// Postgres hides a soft-deleted account because GORM's scope does it, without anybody
// writing it down; the fake looked it up regardless. It is invisible through the API —
// deleting an account also revokes its devices, so requireBearer refuses on the
// device before the account lookup matters — which is exactly why it belongs here
// rather than in an HTTP test that would pass either way.
func TestADeletedAccountIsNotFoundByEitherLookup(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		userID, email := b.newAccount(t)

		if _, err := b.users.ByID(t.Context(), userID); err != nil {
			t.Fatalf("ByID() for a live account = %v", err)
		}

		b.deleteAccount(t, userID)

		if _, err := b.users.ByID(t.Context(), userID); !errors.Is(err, user.ErrNotFound) {
			t.Errorf("ByID() for a deleted account = %v, want ErrNotFound", err)
		}

		if _, err := b.users.ByEmail(t.Context(), email); !errors.Is(err, user.ErrNotFound) {
			t.Errorf("ByEmail() for a deleted account = %v, want ErrNotFound", err)
		}
	})
}

// TestMembersAreOrderedByNameAndPaged pins the ordering both implementations have
// to produce, because with a limit and an offset the order decides which rows a
// page contains. The fake sorted by name-or-email, a leftover from when an
// invitation was a membership with no account; Postgres sorts by name.
func TestMembersAreOrderedByNameAndPaged(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		// Inserted out of order, so neither insertion order nor id order can pass
		// for alphabetical by accident.
		for _, name := range []string{"Dana", "Ada", "Eve", "Bo", "Cy"} {
			userID, _ := b.newNamedAccount(t, name)
			if _, err := b.repo.AddMember(
				t.Context(), orgID, userID, nil, uuid.Nil, time.Now().UTC(),
			); err != nil {
				t.Fatalf("AddMember(%q) = _, %v", name, err)
			}
		}

		want := []string{"Ada", "Bo", "Cy", "Dana", "Eve"}

		if got := memberNames(t, b, orgID, wholePage, 0); !slices.Equal(got, want) {
			t.Errorf("Members(whole page) = %v, want %v", got, want)
		}

		if got := memberNames(t, b, orgID, 2, 0); !slices.Equal(got, want[:2]) {
			t.Errorf("Members(2, 0) = %v, want %v", got, want[:2])
		}

		if got := memberNames(t, b, orgID, 2, 2); !slices.Equal(got, want[2:4]) {
			t.Errorf("Members(2, 2) = %v, want %v", got, want[2:4])
		}

		// The last page is short rather than an error, which is how a client knows
		// it has reached the end.
		if got := memberNames(t, b, orgID, 2, 4); !slices.Equal(got, want[4:]) {
			t.Errorf("Members(2, 4) = %v, want %v", got, want[4:])
		}

		if got := memberNames(t, b, orgID, 2, 99); len(got) != 0 {
			t.Errorf("Members(2, 99) = %v, want nothing", got)
		}
	})
}

// TestMembersWithTheSameNamePageWithoutRepeating is why the order carries the
// membership id.
//
// Sorting by name alone leaves ties, and a sort with ties may return them in any
// order it likes from one query to the next — which with an offset means one row
// appears on two pages and another on none. Three people called Ada is not a
// contrived case; it is a company with three Kowalskis.
func TestMembersWithTheSameNamePageWithoutRepeating(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		want := make([]uuid.UUID, 0, 3)

		for range 3 {
			memberID, _ := addMember(t, b, orgID)
			want = append(want, memberID)
		}

		seen := make([]uuid.UUID, 0, 3)
		for offset := 0; offset < 4; offset += 2 {
			members, err := b.repo.Members(t.Context(), orgID, 2, offset)
			if err != nil {
				t.Fatalf("Members(2, %d) = _, %v", offset, err)
			}

			for _, member := range members {
				seen = append(seen, member.ID)
			}
		}

		slices.SortFunc(seen, compareIDs)
		slices.SortFunc(want, compareIDs)

		if !slices.Equal(seen, want) {
			t.Errorf("two pages of two saw %v, want each of %v exactly once", seen, want)
		}
	})
}

// TestRolesPutTheShippedOnesFirstAndPage pins the other listing's order. The fake
// sorted by key only and ignored is_system entirely, which nothing noticed while
// every role came back in one response.
func TestRolesPutTheShippedOnesFirstAndPage(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)

		b.newRole(t, orgID, "auditor", string(authz.PermOrganizationRead))
		b.newRole(t, orgID, "billing", string(authz.PermOrganizationRead))
		b.newShippedRole(t, orgID, authz.RoleOwner)

		// The shipped role first even though its key sorts last of the three.
		want := []string{string(authz.RoleOwner), "auditor", "billing"}

		if got := roleKeys(t, b, orgID, wholePage, 0); !slices.Equal(got, want) {
			t.Errorf("Roles(whole page) = %v, want %v", got, want)
		}

		if got := roleKeys(t, b, orgID, 1, 0); !slices.Equal(got, want[:1]) {
			t.Errorf("Roles(1, 0) = %v, want %v", got, want[:1])
		}

		if got := roleKeys(t, b, orgID, 2, 1); !slices.Equal(got, want[1:]) {
			t.Errorf("Roles(2, 1) = %v, want %v", got, want[1:])
		}
	})
}

func memberNames(t *testing.T, b *backend, orgID uuid.UUID, limit, offset int) []string {
	t.Helper()

	members, err := b.repo.Members(t.Context(), orgID, limit, offset)
	if err != nil {
		t.Fatalf("Members(%d, %d) = _, %v", limit, offset, err)
	}

	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.Name)
	}

	return names
}

func roleKeys(t *testing.T, b *backend, orgID uuid.UUID, limit, offset int) []string {
	t.Helper()

	roles, err := b.repo.Roles(t.Context(), orgID, limit, offset)
	if err != nil {
		t.Fatalf("Roles(%d, %d) = _, %v", limit, offset, err)
	}

	keys := make([]string, 0, len(roles))
	for _, role := range roles {
		keys = append(keys, role.Key)
	}

	return keys
}

// compareIDs orders identifiers so two collections of them can be compared.
// uuid.UUID is not cmp.Ordered, so this goes through the string form.
func compareIDs(a, b uuid.UUID) int {
	return strings.Compare(a.String(), b.String())
}

// TestAnInvitationToADeletedOrganizationCannotBeAccepted closes a race rather than
// an open door.
//
// InvitationByToken filters deleted organizations, so the service path cannot reach
// this — but that lookup and the accept are two statements, and an organization
// deleted in between would leave an active membership in something that does not
// exist. The row is harmless today, because every read filters deleted
// organizations out again, which is exactly why it would have survived until
// something started counting rows instead.
//
// Only reachable at this level, since the repository is where the two statements
// meet.
func TestAnInvitationToADeletedOrganizationCannotBeAccepted(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		userID, email := b.newAccount(t)

		invitation, err := b.repo.InviteMember(t.Context(), orgID, email, freshToken("doomed-org"),
			nil, uuid.Nil, time.Now().UTC().Add(time.Hour), time.Now().UTC())
		if err != nil {
			t.Fatalf("InviteMember() = _, %v", err)
		}

		b.deleteOrg(t, orgID)

		err = b.dir.AcceptInvitation(t.Context(), invitation.ID, userID, time.Now().UTC())
		if !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("AcceptInvitation() into a deleted organization = %v, want ErrNotFound", err)
		}

		// And nothing was created on the way to refusing.
		if _, err := b.repo.MemberByUser(t.Context(), orgID, userID); !errors.Is(err, orgs.ErrNotFound) {
			t.Errorf("MemberByUser() = %v, want ErrNotFound; the refusal still left a membership", err)
		}
	})
}

// TestTheOwnerCountAgreesWithTheOwnerRule is the case this listing exists for, and
// the one that could quietly stop being true.
//
// The installation-wide listing counts owners in SQL; the last-owner rule counts
// them again inside a locked transaction. Two answers to "does this organization
// have an owner" is one too many: an administrator would be shown an owner for an
// organization the guard treats as having none, or sent to fix one that is fine.
//
// The interesting input is a membership whose account has been deleted. The row
// survives, still holding owner, and every rule that matters stopped counting it —
// which is exactly how an organization ends up with nobody able to administer it.
func TestTheOwnerCountAgreesWithTheOwnerRule(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		orgID := b.newOrg(t)
		owner := b.newShippedRole(t, orgID, authz.RoleOwner)

		memberID, userID := addMember(t, b, orgID, owner)

		if got := ownerCount(t, b, orgID); got != 1 {
			t.Fatalf("owners = %d with one live owner, want 1", got)
		}

		if listed := ownerlessIDs(t, b); slices.Contains(listed, orgID) {
			t.Error("an organization with an owner is listed as ownerless")
		}

		b.deleteAccount(t, userID)

		// Read the listing before touching the membership: removing it would take the
		// count to zero for a different reason and this case would prove nothing.
		fromListing := ownerCount(t, b, orgID)

		// What the rule sees, read from the guard itself rather than assumed.
		var fromGuard int

		err := b.repo.RemoveMember(t.Context(), orgID, memberID, models.ActionMemberRemoved,
			func(state orgs.OwnerState) error {
				fromGuard = state.Owners

				return nil
			})
		if err != nil {
			t.Fatalf("RemoveMember() = %v", err)
		}

		if fromGuard != 0 {
			t.Errorf("the rule counted %d owners for a deleted account, want 0", fromGuard)
		}

		if fromListing != fromGuard {
			t.Errorf("the listing says %d owners and the rule says %d; one of them is "+
				"about to send an administrator to the wrong place", fromListing, fromGuard)
		}
	})
}

// TestTheOwnerlessFilterFindsTheOrganizationNobodyCanAdminister pins the filter
// itself, which is the half a platform administrator actually calls.
func TestTheOwnerlessFilterFindsTheOrganizationNobodyCanAdminister(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *backend) {
		withOwner := b.newOrg(t)
		addMember(t, b, withOwner, b.newShippedRole(t, withOwner, authz.RoleOwner))

		// Empty from the start, which is what creating one through the platform
		// endpoint produces before anybody is appointed.
		empty := b.newOrg(t)

		// An owner whose account is gone: a member list that is not empty, and still
		// nobody who can administer it.
		abandoned := b.newOrg(t)
		_, userID := addMember(t, b, abandoned, b.newShippedRole(t, abandoned, authz.RoleOwner))
		b.deleteAccount(t, userID)

		listed := ownerlessIDs(t, b)

		if slices.Contains(listed, withOwner) {
			t.Error("an organization with a live owner is in the ownerless listing")
		}

		for _, want := range []uuid.UUID{empty, abandoned} {
			if !slices.Contains(listed, want) {
				t.Errorf("%v is not in the ownerless listing", want)
			}
		}
	})
}

func ownerCount(t *testing.T, b *backend, orgID uuid.UUID) int {
	t.Helper()

	list, err := b.prov.AllOrganizations(t.Context(), orgs.OrganizationFilter{}, wholePage, 0)
	if err != nil {
		t.Fatalf("AllOrganizations() = _, %v", err)
	}

	for _, summary := range list {
		if summary.ID == orgID {
			return summary.Owners
		}
	}

	t.Fatalf("%v is not in the listing at all", orgID)

	return -1
}

func ownerlessIDs(t *testing.T, b *backend) []uuid.UUID {
	t.Helper()

	list, err := b.prov.AllOrganizations(t.Context(),
		orgs.OrganizationFilter{WithoutOwner: true}, wholePage, 0)
	if err != nil {
		t.Fatalf("AllOrganizations(without owner) = _, %v", err)
	}

	ids := make([]uuid.UUID, 0, len(list))
	for _, summary := range list {
		if summary.Owners != 0 {
			t.Errorf("%v has %d owners but is in the ownerless listing", summary.ID, summary.Owners)
		}

		ids = append(ids, summary.ID)
	}

	return ids
}
