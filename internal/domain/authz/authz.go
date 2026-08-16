package authz

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

var (
	// ErrForbidden is "you are here, but you may not do this". It is only ever
	// returned to a caller whose membership has already been established, so it
	// discloses nothing that caller did not already know.
	ErrForbidden = errors.New("authz: permission denied")

	// ErrNotMember covers both "no such organization" and "not a member of it",
	// and the two must stay indistinguishable. The API turns it into a 404: a
	// 403 here would confirm that an organization exists to anyone willing to
	// try identifiers, which in a multi-tenant system is a list of customers.
	ErrNotMember = errors.New("authz: not a member of the organization")

	// ErrUnknownPermission means the key is not in this build's catalog. It is a
	// programming error on the request path and a stale row on the storage
	// path; Sanitize handles the second, this covers the first.
	ErrUnknownPermission = errors.New("authz: permission is not in the catalog")

	// ErrScopeMismatch means an organization-scoped permission was asked about
	// without an organization, or a system-scoped one with one. Always a bug in
	// the caller, never something a request can provoke on its own.
	ErrScopeMismatch = errors.New("authz: permission scope does not match the request")

	// ErrPrivilegeEscalation is the answer to "grant a role a permission you do
	// not hold yourself". Without it, roles.update is a permission to become an
	// administrator, which is not what anyone reading the role list would think
	// it meant.
	ErrPrivilegeEscalation = errors.New("authz: cannot grant a permission you do not hold")
)

// Request is a question about one caller, one permission and — for
// organization-scoped permissions — one organization.
type Request struct {
	Actor      uuid.UUID
	Org        uuid.UUID // uuid.Nil for system scope
	Permission Permission
}

// Grant is the answer: everything the actor may do in that scope, not merely
// whether the one permission asked about was held.
//
// Returning the whole set costs nothing extra — resolving it is one query
// either way — and it means a handler that needs a second check (may I also
// reassign roles, now that I am editing this member?) does not go back to the
// database to ask.
//
// The set is unexported and there is no method that adds to it. A Grant is the
// record of a decision that has already been made; code holding one must not be
// able to widen it.
type Grant struct {
	actor       uuid.UUID
	org         uuid.UUID
	permissions map[Permission]struct{}
}

// NewGrant builds a grant over the given permissions. Unknown keys are dropped
// rather than rejected — see Sanitize for why.
func NewGrant(actor, org uuid.UUID, permissions []Permission) *Grant {
	set := make(map[Permission]struct{}, len(permissions))
	for _, perm := range permissions {
		if Known(perm) {
			set[perm] = struct{}{}
		}
	}

	return &Grant{actor: actor, org: org, permissions: set}
}

// Actor is the user the decision was made for.
func (g *Grant) Actor() uuid.UUID {
	if g == nil {
		return uuid.Nil
	}

	return g.actor
}

// OrganizationID is the organization the decision was made in, or uuid.Nil for
// a system-scope decision.
//
// This is the only sanctioned source of the organization inside a handler. The
// {orgID} path parameter is what the middleware checked, and reading it a second
// time in the handler opens the door to acting on a different organization than
// the one that was authorized.
func (g *Grant) OrganizationID() uuid.UUID {
	if g == nil {
		return uuid.Nil
	}

	return g.org
}

// Has reports whether the permission was granted. A nil Grant holds nothing, so
// forgetting to check the ok from GrantFrom denies rather than allows.
func (g *Grant) Has(perm Permission) bool {
	if g == nil {
		return false
	}

	_, ok := g.permissions[perm]

	return ok
}

// Covers reports whether every one of perms was granted. It is the predicate
// behind the anti-escalation rule.
func (g *Grant) Covers(perms []Permission) bool {
	for _, perm := range perms {
		if !g.Has(perm) {
			return false
		}
	}

	return true
}

// Permissions lists what was granted, sorted, as a fresh slice. Sorted because
// this feeds both the API snapshot and test assertions, and map order would make
// both unstable.
func (g *Grant) Permissions() []Permission {
	if g == nil {
		return nil
	}

	out := make([]Permission, 0, len(g.permissions))
	for perm := range g.permissions {
		out = append(out, perm)
	}

	slices.Sort(out)

	return out
}

// Authorizer answers Requests. The API middleware depends on this interface
// rather than on the concrete service, so a test can substitute a decision
// without a database.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (*Grant, error)
}

// Snapshotter produces the whole-account view a client renders from. It is a
// separate interface from Authorizer because the two have different callers and
// very different stakes: Authorizer decides, Snapshotter only describes.
type Snapshotter interface {
	Snapshot(ctx context.Context, userID uuid.UUID) (*Snapshot, error)
}

// Repository is the persistence this package needs, declared here because the
// consumer owns the interface — the store depends on the domain and never the
// other way round.
type Repository interface {
	// OrganizationPermissionKeys returns the raw permission keys the user holds
	// in the organization through their active membership's roles.
	//
	// It returns ErrNotMember when the organization does not exist, when the
	// user has no membership in it, or when that membership is not active. One
	// error for all four, because the API renders them identically and a
	// separate error would eventually leak through a log line or a status code.
	//
	// Keys are returned raw: this layer does not know which of them the current
	// build still defines, and filtering belongs where the catalog lives.
	OrganizationPermissionKeys(ctx context.Context, userID, orgID uuid.UUID) ([]string, error)

	// PermissionKeysByOrganization returns the same thing for every organization
	// the user is an active member of, keyed by organization.
	//
	// It exists so the permission snapshot is one query rather than one per
	// organization. Asking OrganizationPermissionKeys in a loop is the shape
	// that works fine for the developer who has two organizations and falls
	// over for the customer who has forty.
	PermissionKeysByOrganization(ctx context.Context, userID uuid.UUID) (map[uuid.UUID][]string, error)

	// SystemRoleKeys returns the installation-wide role keys assigned to the
	// user. An empty result is not an error — most users have none.
	SystemRoleKeys(ctx context.Context, userID uuid.UUID) ([]string, error)
}

// Snapshot is everything a caller may do, everywhere, at one moment.
//
// It exists for the client: a UI needs to know what to hide before the user
// clicks, and asking the server per button is not workable. It is emphatically
// not an enforcement mechanism — the server re-decides on every request, and a
// client that has a stale snapshot simply gets a 403 it has to handle.
type Snapshot struct {
	Actor uuid.UUID

	// SystemRoles and SystemPermissions are empty for almost everyone.
	SystemRoles       []RoleKey
	SystemPermissions []Permission

	// ByOrganization holds the permissions the caller has in each organization
	// where their membership is active. Organizations they are only invited to,
	// or suspended in, are absent: they confer nothing, and listing them with an
	// empty set would suggest the UI should render them as usable.
	ByOrganization map[uuid.UUID][]Permission
}

// Permissions returns what the caller holds in one organization.
func (s *Snapshot) Permissions(orgID uuid.UUID) []Permission {
	if s == nil {
		return nil
	}

	return s.ByOrganization[orgID]
}

// Snapshot resolves everything the caller may do, in two queries.
func (s *Service) Snapshot(ctx context.Context, userID uuid.UUID) (*Snapshot, error) {
	byOrg, err := s.repo.PermissionKeysByOrganization(ctx, userID)
	if err != nil {
		return nil, err
	}

	out := &Snapshot{Actor: userID, ByOrganization: make(map[uuid.UUID][]Permission, len(byOrg))}

	for orgID, keys := range byOrg {
		permissions := Sanitize(keys)
		slices.Sort(permissions)

		out.ByOrganization[orgID] = permissions
	}

	systemKeys, err := s.repo.SystemRoleKeys(ctx, userID)
	if err != nil {
		return nil, err
	}

	var systemPermissions []Permission

	for _, key := range systemKeys {
		def, ok := LookupRole(RoleKey(key))
		if !ok || def.Scope != ScopeSystem {
			continue
		}

		out.SystemRoles = append(out.SystemRoles, def.Key)

		for _, perm := range def.Permissions {
			if !slices.Contains(systemPermissions, perm) {
				systemPermissions = append(systemPermissions, perm)
			}
		}
	}

	slices.Sort(out.SystemRoles)
	slices.Sort(systemPermissions)

	out.SystemPermissions = systemPermissions

	return out, nil
}

// Service resolves permissions against storage.
type Service struct {
	repo Repository
}

var (
	_ Authorizer  = (*Service)(nil)
	_ Snapshotter = (*Service)(nil)
)

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Authorize decides whether the actor may exercise the permission, and returns
// everything else they may do in the same scope.
//
// Note what it does not do: consult a token. Permissions are resolved from
// storage on every call, so revoking a role takes effect on the next request
// rather than when the caller's token happens to expire. That is the same
// trade the bearer middleware already makes for device revocation, and it costs
// one query for the same reason.
func (s *Service) Authorize(ctx context.Context, req Request) (*Grant, error) {
	def, ok := Lookup(req.Permission)
	if !ok {
		return nil, ErrUnknownPermission
	}

	if def.Scope == ScopeSystem {
		if req.Org != uuid.Nil {
			return nil, ErrScopeMismatch
		}

		return s.authorizeSystem(ctx, req)
	}

	if req.Org == uuid.Nil {
		return nil, ErrScopeMismatch
	}

	return s.authorizeOrganization(ctx, req)
}

func (s *Service) authorizeOrganization(ctx context.Context, req Request) (*Grant, error) {
	keys, err := s.repo.OrganizationPermissionKeys(ctx, req.Actor, req.Org)
	if err != nil {
		return nil, err
	}

	grant := NewGrant(req.Actor, req.Org, Sanitize(keys))
	if !grant.Has(req.Permission) {
		// The grant is dropped on refusal rather than returned alongside the
		// error. A caller that reached for it after a non-nil error would be
		// holding a decision that said no.
		return nil, ErrForbidden
	}

	return grant, nil
}

// authorizeSystem resolves platform roles from the catalog rather than from
// role_permissions: a system role belongs to no organization, so it has no row
// to read permissions from.
//
// A platform administrator deliberately gets no implicit standing inside any
// organization. Support access to a tenant's data is a decision with its own
// audit trail and its own endpoints, not a side effect of holding a role whose
// description says "manages organizations".
func (s *Service) authorizeSystem(ctx context.Context, req Request) (*Grant, error) {
	keys, err := s.repo.SystemRoleKeys(ctx, req.Actor)
	if err != nil {
		return nil, err
	}

	var held []Permission

	for _, key := range keys {
		def, ok := LookupRole(RoleKey(key))
		if !ok || def.Scope != ScopeSystem {
			// A key the build no longer ships, or one that names an
			// organization role stored in the wrong table. Both grant nothing.
			continue
		}

		held = append(held, def.Permissions...)
	}

	grant := NewGrant(req.Actor, uuid.Nil, held)
	if !grant.Has(req.Permission) {
		// Not ErrNotMember: system scope has no membership to be outside of, and
		// the caller is a signed-in user either way.
		return nil, ErrForbidden
	}

	return grant, nil
}

// EnsureCanGrant is the anti-escalation rule: you may only put into a role a
// permission you hold yourself.
//
// Without it, roles.update is transitively every permission in the catalog —
// add platform.users.delete to a role, assign the role to yourself, and the
// authorization system has been talked out of its own rules. The same applies
// to assigning an existing role to someone else, which is why the check takes a
// permission set rather than a role id and both call sites share it.
func EnsureCanGrant(g *Grant, requested []Permission) error {
	for _, perm := range requested {
		if !Known(perm) {
			return ErrUnknownPermission
		}

		if !g.Has(perm) {
			return ErrPrivilegeEscalation
		}
	}

	return nil
}

// ctxKey keeps the context key unexported so no other package can collide with
// it, which is the whole reason for the named type.
type ctxKey int

const grantKey ctxKey = iota

// WithGrant attaches a decision to the context. The API middleware calls it
// after authorizing, so handlers read the outcome instead of re-deriving it.
func WithGrant(ctx context.Context, g *Grant) context.Context {
	return context.WithValue(ctx, grantKey, g)
}

// GrantFrom returns the decision the middleware made.
//
// The second result is false for a missing or empty grant, mirroring
// auth.SessionFrom: a handler that ignores it and uses the zero value gets a
// Grant that permits nothing rather than one that permits everything.
func GrantFrom(ctx context.Context) (*Grant, bool) {
	g, ok := ctx.Value(grantKey).(*Grant)
	if !ok || g == nil || g.actor == uuid.Nil {
		return nil, false
	}

	return g, true
}
