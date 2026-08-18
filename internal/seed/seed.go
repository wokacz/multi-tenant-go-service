// Package seed fills a development database with data worth testing against.
//
// It is not a fixture package for the test suite — the suite has its own, in the
// memory fake and the contract fixtures. This is for the database a person opens:
// enough accounts to page through, organizations in each of the shapes the rules
// care about, and a cast of accounts whose passwords are written down so somebody
// can sign in as "the last owner" instead of building one by hand.
//
// It lives in internal/ rather than in cmd/seed so that the plan can be run
// against the in-memory implementations in a test. A seeder nobody runs in CI is a
// seeder that stops working, and finding that out while trying to reproduce a bug
// is the worst moment for it.
//
// Nothing here knows about HTTP or SQL: like cmd/bootstrap, it drives the domain
// services, so seeded data is data the application itself could have produced. The
// exceptions are deliberate and marked — AddMember and CreateRole are the
// provisioning path, which is repository-level for the same reason bootstrap is.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

const (
	// Domain is where every seeded address lives. .test is reserved by RFC 6761
	// and can never be a real mailbox, which is what makes the guard in guard.go
	// able to tell seeded data from somebody's own.
	Domain = "seed.test"

	// SlugPrefix marks every seeded organization, so a reset knows what it may
	// delete and a person reading a list knows what they are looking at.
	SlugPrefix = "seed-"

	// Password is shared by every seeded account and written down in
	// docs/guides/009_seed_data.md. One password for all of them is the point:
	// the documentation stays one line, and there is nothing to look up.
	//
	// It is safe only because it can never be a production password — the guard
	// refuses to run outside development, and every one of these accounts is
	// reachable only through an address in a domain that does not resolve.
	Password = "seed-password"
)

// Part is one chunk of the plan.
//
// Adding data means adding a Part and one line to Plan. Parts run in order and
// read what earlier ones made through World, which is what keeps "create the
// organization" and "put somebody in it" in separate files without either
// searching for the other's rows.
type Part interface {
	// Name is what -only and -skip match, and what the log calls it.
	Name() string

	Run(ctx context.Context, w *World) error
}

// Plan is the seeder, in order.
//
// The order is a dependency order, not a preference: the cast has to exist before
// anything can own an organization, organizations before anybody can be invited to
// one, and the states at the end act on what the earlier parts built.
func Plan() []Part {
	return []Part{
		cast{},
		organizations{},
		volume{},
		invitations{},
		states{},
	}
}

// World is what a Part gets: the services, deterministic randomness, and a
// register of everything made so far.
type World struct {
	Users    *user.Service
	Orgs     *orgs.Service
	Repo     orgs.Repository
	Prov     orgs.Provisioner
	UserRepo user.Repository

	// Rand is seeded from a fixed value unless -seed says otherwise, so two runs
	// produce the same hundred people. A bug found by clicking around is then
	// reproducible, and screenshots keep matching the documentation.
	Rand *rand.Rand

	Log *slog.Logger

	// Now is captured once, so everything in one run agrees about what "expired"
	// and "joined last week" mean.
	Now time.Time

	accounts map[string]*ent.User
	orgs     map[string]*ent.Organization
}

// NewWorld wires a World. It does not touch the database.
func NewWorld(
	users *user.Service,
	service *orgs.Service,
	repo orgs.Repository,
	prov orgs.Provisioner,
	userRepo user.Repository,
	rng *rand.Rand,
	log *slog.Logger,
) *World {
	return &World{
		Users:    users,
		Orgs:     service,
		Repo:     repo,
		Prov:     prov,
		UserRepo: userRepo,
		Rand:     rng,
		Log:      log,
		Now:      time.Now().UTC(),
		accounts: map[string]*ent.User{},
		orgs:     map[string]*ent.Organization{},
	}
}

// Run executes the plan, skipping parts the caller did not ask for.
//
// only and skip are matched by Part.Name. An unknown name is an error rather than
// a no-op: a typo in -only that silently seeded everything would be discovered as
// a database full of data somebody did not want.
func Run(ctx context.Context, w *World, plan []Part, only, skip []string) error {
	if err := checkNames(plan, only, skip); err != nil {
		return err
	}

	for _, part := range plan {
		if len(only) > 0 && !contains(only, part.Name()) {
			continue
		}

		if contains(skip, part.Name()) {
			continue
		}

		started := time.Now()

		if err := part.Run(ctx, w); err != nil {
			return fmt.Errorf("seed: part %s: %w", part.Name(), err)
		}

		w.Log.Info("seeded", slog.String("part", part.Name()),
			slog.Duration("took", time.Since(started)))
	}

	return nil
}

func checkNames(plan []Part, lists ...[]string) error {
	known := map[string]bool{}
	for _, part := range plan {
		known[part.Name()] = true
	}

	for _, list := range lists {
		for _, name := range list {
			if !known[name] {
				return fmt.Errorf("seed: no part named %q", name)
			}
		}
	}

	return nil
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}

	return false
}

// Email is the address for a handle. Handles are what the documentation and the
// parts use to refer to each other; the address is derived so the two can never
// disagree.
func Email(handle string) string {
	return handle + "@" + Domain
}

// Slug is the slug for a name.
func Slug(name string) string {
	return SlugPrefix + name
}

// ensureAccount creates an account, or returns the one already there.
//
// This is where idempotency lives, and it lives here rather than in each Part so a
// new Part gets it without thinking about it. The address is the identity: a
// second run finds the same people and moves on, which is what makes it possible
// to add a Part and re-run without wiping what is already there.
func (w *World) ensureAccount(ctx context.Context, handle, name, locale string) (*ent.User, error) {
	if existing, ok := w.accounts[handle]; ok {
		return existing, nil
	}

	email := Email(handle)

	created, err := w.Users.Create(ctx, name, email, Password, Password, locale)
	if err == nil {
		w.accounts[handle] = created

		return created, nil
	}

	if !errors.Is(err, user.ErrEmailTaken) {
		return nil, err
	}

	existing, err := w.Users.ByEmail(ctx, email)
	if err != nil {
		return nil, err
	}

	w.accounts[handle] = existing

	return existing, nil
}

// castAccount resolves one of the documented accounts, creating it if an earlier
// part has not.
//
// The fallback is what makes -only=organizations work on an empty database: a part
// that needs Olga Owner says so, and does not have to care whether the cast part
// ran. The name and locale come from Cast() either way, so there is one definition
// of who these people are.
func (w *World) castAccount(ctx context.Context, handle string) (*ent.User, error) {
	if existing, ok := w.accounts[handle]; ok {
		return existing, nil
	}

	for _, member := range Cast() {
		if member.Handle == handle {
			return w.ensureAccount(ctx, member.Handle, member.Name, member.Locale)
		}
	}

	return nil, fmt.Errorf("seed: %q is not in the cast", handle)
}

// ensureOrganization creates an organization with the shipped roles, or returns
// the one already there.
func (w *World) ensureOrganization(ctx context.Context, name, display string) (*ent.Organization, error) {
	slug := Slug(name)

	if existing, ok := w.orgs[slug]; ok {
		return existing, nil
	}

	created, err := w.Orgs.CreateOrganization(ctx, slug, display)
	if err == nil {
		w.orgs[slug] = created

		return created, nil
	}

	if !errors.Is(err, orgs.ErrSlugTaken) {
		return nil, err
	}

	existing, err := w.Prov.OrganizationBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}

	w.orgs[slug] = existing

	return existing, nil
}

// ensureMember puts an account in an organization with the given roles, or leaves
// the membership that is already there alone.
//
// It uses the repository rather than a service method, which is the same choice
// cmd/bootstrap makes and for the same reason: adding somebody to an organization
// through the API needs a permission inside it, and provisioning is the path that
// exists precisely for the case where nobody holds one yet.
func (w *World) ensureMember(
	ctx context.Context,
	orgID uuid.UUID,
	account *ent.User,
	roleIDs ...uuid.UUID,
) error {
	_, err := w.Repo.MemberByUser(ctx, orgID, account.ID)
	if err == nil {
		return nil
	}

	if !errors.Is(err, orgs.ErrNotFound) {
		return err
	}

	// The new member is their own actor in the audit log, the same choice
	// registration makes: nothing here is a person acting on somebody else.
	_, err = w.Repo.AddMember(w.actingAs(ctx, account.ID), orgID, account.ID,
		roleIDs, uuid.Nil, w.Now)

	return err
}

// forget drops an account from the register.
//
// One part deletes an account on purpose, and a register that kept handing the
// deleted row back would make the next run promote a person who is gone. Forgetting
// is cheaper than checking every cached account is still alive on every read.
func (w *World) forget(handle string) {
	delete(w.accounts, handle)
}

// role resolves one of the shipped roles inside an organization.
func (w *World) role(ctx context.Context, orgID uuid.UUID, key authz.RoleKey) (uuid.UUID, error) {
	role, err := w.Repo.RoleByKey(ctx, orgID, string(key))
	if err != nil {
		return uuid.Nil, fmt.Errorf("role %s: %w", key, err)
	}

	return role.ID, nil
}

// ownerGrant is the grant the middleware would have resolved for an owner of this
// organization.
//
// Synthesising one is what lets the seeder call the service methods that take a
// grant — inviting, suspending, changing roles — so the rules those methods carry
// still run. It reads the permissions out of the shipped catalog rather than making
// a set up: a grant with invented permissions would let the seeder produce states
// the application refuses, which is the one thing seeded data must not be.
func (w *World) ownerGrant(orgID, actor uuid.UUID) *authz.Grant {
	def, ok := authz.LookupRole(authz.RoleOwner)
	if !ok {
		panic("seed: the catalog has no owner role")
	}

	return authz.NewGrant(actor, orgID, def.Permissions)
}

// actingAs puts an actor on the context so the audit log has somebody to name.
//
// Without it every seeded change is invisible in the history: the store writes no
// row when there is no actor, which is the behaviour registration had to work
// around too.
func (w *World) actingAs(ctx context.Context, userID uuid.UUID) context.Context {
	return audit.WithActor(ctx, audit.Actor{
		ID:        userID,
		IP:        "127.0.0.1",
		UserAgent: "seed",
	})
}
