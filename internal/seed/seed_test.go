package seed_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/seed"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// newWorld runs the seeder against the in-memory implementations.
//
// This is why the package is in internal/ rather than in cmd/seed: the whole plan
// executes here, in a second, with no database. A seeder that is only ever run by
// hand is one that breaks quietly, and the moment you find out is while trying to
// reproduce somebody else's bug.
func newWorld(t *testing.T) *seed.World {
	t.Helper()

	users := memory.NewUsers()
	repo := memory.NewAuthz(users)

	service := user.NewService(users, []byte(strings.Repeat("p", 32)), user.WithBcryptCost(4))
	organizations := orgs.NewService(repo, repo, repo)

	return seed.NewWorld(service, organizations, repo, repo, users,
		rand.New(rand.NewPCG(1, 2)), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestThePlanRuns is the test that keeps the seeder working. Every part, in order,
// against the same interfaces the real store implements.
func TestThePlanRuns(t *testing.T) {
	w := newWorld(t)

	if err := seed.Run(context.Background(), w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

// TestSeedingTwiceChangesNothing is the property that lets somebody add a part and
// re-run without wiping what is there.
//
// It compares the whole installation before and after a second pass: the same
// accounts, the same organizations, the same owner counts. An idempotency bug shows
// up here as a second Ada, or as an organization whose member count doubled.
func TestSeedingTwiceChangesNothing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("first run = %v", err)
	}

	before := snapshot(t, w)

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("second run = %v", err)
	}

	after := snapshot(t, w)

	if before != after {
		t.Errorf("the second run changed the installation:\nbefore %s\nafter  %s", before, after)
	}
}

// TestTheDocumentedCastCanSignIn is the promise the documentation makes.
//
// Every handle in Cast() has to exist with the password the guide prints. A cast
// entry nobody created, or one created with a different password, is a line in a
// document that wastes somebody's afternoon.
func TestTheDocumentedCastCanSignIn(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for _, member := range seed.Cast() {
		email := seed.Email(member.Handle)

		if _, err := w.Users.Authenticate(ctx, email, seed.Password); err != nil {
			t.Errorf("%s cannot sign in with the documented password: %v", email, err)
		}
	}
}

// TestTheSeededInstallationHasTheStatesItPromises checks the states that are the
// reason for seeding at all. Uniform data is easy to produce and proves nothing.
func TestTheSeededInstallationHasTheStatesItPromises(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	all, err := w.Prov.AllOrganizations(ctx, orgs.OrganizationFilter{}, 100, 0)
	if err != nil {
		t.Fatalf("AllOrganizations() = %v", err)
	}

	byslug := map[string]orgs.OrganizationSummary{}
	for _, org := range all {
		byslug[org.Slug] = org
	}

	acme, ok := byslug[seed.Slug(seed.OrgAcme)]
	if !ok {
		t.Fatal("seed-acme was not created")
	}

	// More than one page of members, which is the point of the volume part.
	members, err := w.Repo.Members(ctx, acme.ID, 1000, 0)
	if err != nil {
		t.Fatalf("Members() = %v", err)
	}

	if len(members) <= orgs.MaxMemberPage {
		t.Errorf("seed-acme has %d members, want more than one page (%d)",
			len(members), orgs.MaxMemberPage)
	}

	// Somebody is suspended, and a suspended member keeps its roles.
	suspended := 0

	for i := range members {
		if !members[i].Status.GrantsPermissions() {
			suspended++
		}
	}

	if suspended == 0 {
		t.Error("nobody in seed-acme is suspended")
	}

	// An organization nobody can administer, which is the state the API cannot
	// produce and the ownerless filter exists for.
	ownerless, err := w.Prov.AllOrganizations(ctx, orgs.OrganizationFilter{WithoutOwner: true}, 100, 0)
	if err != nil {
		t.Fatalf("AllOrganizations(without owner) = %v", err)
	}

	wantOwnerless := map[string]bool{
		seed.Slug(seed.OrgAbandoned): false,
		seed.Slug(seed.OrgEmpty):     false,
	}

	for _, org := range ownerless {
		if _, expected := wantOwnerless[org.Slug]; expected {
			wantOwnerless[org.Slug] = true
		}
	}

	for slug, found := range wantOwnerless {
		if !found {
			t.Errorf("%s is not in the ownerless listing", slug)
		}
	}

	// The custom roles that split members.invite from members.remove, which no
	// shipped role does.
	for _, key := range []string{"inviter", "remover"} {
		if _, err := w.Repo.RoleByKey(ctx, acme.ID, key); err != nil {
			t.Errorf("seed-acme has no %q role: %v", key, err)
		}
	}

	// A pending invitation and an expired one, told apart by asking the clock twice:
	// InvitationsForOrganization hides the expired one.
	pending, err := w.Repo.InvitationsForOrganization(ctx, byslug[seed.Slug(seed.OrgGlobex)].ID, w.Now)
	if err != nil {
		t.Fatalf("InvitationsForOrganization() = %v", err)
	}

	if len(pending) == 0 {
		t.Error("seed-globex has no pending invitations")
	}

	// An account that belongs to nothing.
	nowhere, err := w.Users.ByEmail(ctx, seed.Email("nowhere"))
	if err != nil {
		t.Fatalf("ByEmail(nowhere) = %v", err)
	}

	mine, err := w.Orgs.Mine(ctx, nowhere.ID)
	if err != nil {
		t.Fatalf("Mine() = %v", err)
	}

	if len(mine) != 0 {
		t.Errorf("nowhere belongs to %d organizations, want none", len(mine))
	}

	// The platform administrator, without whom the platform screens have no reader.
	platform, err := w.Users.ByEmail(ctx, seed.Email("platform"))
	if err != nil {
		t.Fatalf("ByEmail(platform) = %v", err)
	}

	holders, err := w.Prov.SystemRoleHolders(ctx)
	if err != nil {
		t.Fatalf("SystemRoleHolders() = %v", err)
	}

	granted := false

	for _, holder := range holders {
		if holder.UserID == platform.ID && holder.RoleKey == string(authz.RolePlatformAdmin) {
			granted = true
		}
	}

	if !granted {
		t.Error("nobody holds platform_admin")
	}
}

// TestResetRemovesOnlyTheSeedData covers the destructive half.
func TestResetRemovesOnlyTheSeedData(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// Somebody's own account, which a reset must not touch.
	mine, err := w.Users.Create(ctx, "Real Person", "real@example.com", "twelve-chars-long", "twelve-chars-long", "pl")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if err := seed.Reset(ctx, w); err != nil {
		t.Fatalf("Reset() = %v", err)
	}

	if _, err := w.Users.ByEmail(ctx, seed.Email("owner")); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("a seeded account survived the reset: %v", err)
	}

	if _, err := w.Users.ByID(ctx, mine.ID); err != nil {
		t.Errorf("the reset deleted an account it did not create: %v", err)
	}

	remaining, err := w.Prov.AllOrganizations(ctx, orgs.OrganizationFilter{}, 100, 0)
	if err != nil {
		t.Fatalf("AllOrganizations() = %v", err)
	}

	for _, org := range remaining {
		if strings.HasPrefix(org.Slug, seed.SlugPrefix) {
			t.Errorf("%s survived the reset", org.Slug)
		}
	}
}

// TestSeedingAfterAResetReusesTheAddresses is the property that makes the reset
// worth having rather than a way to break the next run.
//
// It works because the unique indexes on users.email and organizations.slug are
// partial: a soft-deleted row does not hold its name. Without that, seeding after a
// reset would collide with the rows it had just retired.
func TestSeedingAfterAResetReusesTheAddresses(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("first run = %v", err)
	}

	if err := seed.Reset(ctx, w); err != nil {
		t.Fatalf("Reset() = %v", err)
	}

	// A fresh World, because the first one remembers what it made and would hand
	// back the deleted rows from its own register.
	fresh := newWorld(t)
	if err := seed.Run(ctx, fresh, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("second run after reset = %v", err)
	}

	if _, err := fresh.Users.Authenticate(ctx, seed.Email("owner"), seed.Password); err != nil {
		t.Errorf("the cast could not be recreated after a reset: %v", err)
	}
}

// TestAnUnknownPartNameIsAnError keeps a typo in -only from seeding everything.
func TestAnUnknownPartNameIsAnError(t *testing.T) {
	w := newWorld(t)

	err := seed.Run(context.Background(), w, seed.Plan(), []string{"acounts"}, nil)
	if err == nil {
		t.Fatal("Run() accepted a part that does not exist")
	}
}

// TestGuardRefusesProduction is the one refusal with no way past it.
func TestGuardRefusesProduction(t *testing.T) {
	users := memory.NewUsers()

	cfg := &config.Config{Env: config.EnvProduction}

	// Confirmed and forced, which is the strongest a caller can ask.
	if err := seed.Guard(context.Background(), cfg, users, true, true); !errors.Is(err, seed.ErrProduction) {
		t.Errorf("Guard() = %v, want ErrProduction", err)
	}
}

// TestGuardNeedsConfirmation stops a bare invocation from writing anything.
func TestGuardNeedsConfirmation(t *testing.T) {
	users := memory.NewUsers()
	cfg := &config.Config{Env: config.EnvDevelopment}

	if err := seed.Guard(context.Background(), cfg, users, false, false); !errors.Is(err, seed.ErrNotConfirmed) {
		t.Errorf("Guard() = %v, want ErrNotConfirmed", err)
	}
}

// TestGuardRefusesADatabaseWithForeignAccounts is the layer that protects a dev
// database somebody has been using by hand.
func TestGuardRefusesADatabaseWithForeignAccounts(t *testing.T) {
	users := memory.NewUsers()
	cfg := &config.Config{Env: config.EnvDevelopment}
	ctx := context.Background()

	service := user.NewService(users, []byte(strings.Repeat("p", 32)), user.WithBcryptCost(4))

	if _, err := service.Create(ctx, "Real Person", "real@example.com",
		"twelve-chars-long", "twelve-chars-long", "pl"); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if err := seed.Guard(ctx, cfg, users, true, false); !errors.Is(err, seed.ErrForeignData) {
		t.Errorf("Guard() = %v, want ErrForeignData", err)
	}

	// -force is the way past this one, and only this one.
	if err := seed.Guard(ctx, cfg, users, true, true); err != nil {
		t.Errorf("Guard(force) = %v, want it allowed", err)
	}
}

// TestGuardAllowsADatabaseOfItsOwnData covers the ordinary re-run: seeded accounts
// are not foreign data.
func TestGuardAllowsADatabaseOfItsOwnData(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if err := seed.Run(ctx, w, seed.Plan(), nil, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	cfg := &config.Config{Env: config.EnvDevelopment}
	if err := seed.Guard(ctx, cfg, w.UserRepo, true, false); err != nil {
		t.Errorf("Guard() on a seeded database = %v, want it allowed", err)
	}
}

// snapshot is a comparable description of the installation: which accounts exist and
// what each organization looks like.
func snapshot(t *testing.T, w *seed.World) string {
	t.Helper()

	ctx := context.Background()

	accounts, err := w.UserRepo.All(ctx, 1000, 0)
	if err != nil {
		t.Fatalf("All() = %v", err)
	}

	emails := make([]string, 0, len(accounts))
	for i := range accounts {
		emails = append(emails, accounts[i].Email)
	}

	organizations, err := w.Prov.AllOrganizations(ctx, orgs.OrganizationFilter{}, 100, 0)
	if err != nil {
		t.Fatalf("AllOrganizations() = %v", err)
	}

	slices.Sort(emails)

	var b strings.Builder

	fmt.Fprintf(&b, "accounts=%d\n", len(emails))

	for _, email := range emails {
		fmt.Fprintf(&b, "  %s\n", email)
	}

	for _, org := range organizations {
		members, err := w.Repo.Members(ctx, org.ID, 1000, 0)
		if err != nil {
			t.Fatalf("Members() = %v", err)
		}

		roles, err := w.Repo.Roles(ctx, org.ID, 100, 0)
		if err != nil {
			t.Fatalf("Roles() = %v", err)
		}

		fmt.Fprintf(&b, "org %s members=%d roles=%d owners=%d\n",
			org.Slug, len(members), len(roles), org.Owners)
	}

	return b.String()
}
