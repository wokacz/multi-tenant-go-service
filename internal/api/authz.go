package api

import (
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// orgIDParam is the path parameter carrying the organization. It is read by the
// middleware and must match the {orgID} written into every organization-scoped
// route; TestOrgScopedRulesLiveOnOrgScopedPaths keeps the two in step.
const orgIDParam = "orgID"

// accessRule is what an operation needs before it may run.
type accessRule struct {
	Permission authz.Permission
	Scope      authz.Scope
}

// Every operation belongs to exactly one of three sets, and the split is the
// whole design:
//
//   - publicOperations (in middleware.go) needs no token at all;
//   - selfServiceOperations needs a token and nothing more, because the caller's
//     identity *is* the authorization — these act on the caller's own account;
//   - operationAccess needs a permission, resolved in a scope.
//
// An operation in none of them is refused. That is the same default-deny choice
// requireBearer makes and it fails the same way: forget to classify a route and
// it stops working immediately and loudly, rather than becoming quietly open.
//
// TestEveryOperationHasExactlyOneAuthorizationRule fails the build when an
// operation is missing from all three, or appears in more than one.

// selfServiceOperations act on the caller's own account.
//
// Nothing may ever gate these on a permission. A role configuration that could
// take away "read my own profile" or "revoke my own device" would lock a user
// out of their account with no way back in, and the person who could fix it is
// the one who is locked out.
var selfServiceOperations = map[string]bool{
	"get-me":                true,
	"get-user":              true,
	"set-two-factor":        true,
	"list-devices":          true,
	"revoke-device":         true,
	"list-login-events":     true,
	"list-my-organizations": true,
	"accept-invitation":     true,
	"decline-invitation":    true,

	// The permission catalog describes what the product can do, not what any
	// organization's data is. Gating it would leave a caller who may edit roles
	// unable to see the list of permissions to put in them.
	"list-permission-catalog": true,

	// The caller's own snapshot of what they may do. Gating it would be circular,
	// and a client that cannot read it cannot render anything at all.
	"get-my-permissions": true,
}

// operationAccess maps an operation to the permission it needs.
//
// Keys are huma.Operation.OperationID — the same identifier the OpenAPI document
// and every generated client use — so renaming an operation cannot leave an
// entry here pointing at nothing without the contract changing too.
var operationAccess = map[string]accessRule{
	"get-organization":    {authz.PermOrganizationRead, authz.ScopeOrganization},
	"update-organization": {authz.PermOrganizationUpdate, authz.ScopeOrganization},
	"delete-organization": {authz.PermOrganizationDelete, authz.ScopeOrganization},

	"list-members":         {authz.PermMembersRead, authz.ScopeOrganization},
	"add-member":           {authz.PermMembersInvite, authz.ScopeOrganization},
	"update-member-status": {authz.PermMembersSuspend, authz.ScopeOrganization},
	"remove-member":        {authz.PermMembersRemove, authz.ScopeOrganization},
	"set-member-roles":     {authz.PermMembersRolesAssign, authz.ScopeOrganization},

	"list-roles":  {authz.PermRolesRead, authz.ScopeOrganization},
	"get-role":    {authz.PermRolesRead, authz.ScopeOrganization},
	"create-role": {authz.PermRolesCreate, authz.ScopeOrganization},
	"update-role": {authz.PermRolesUpdate, authz.ScopeOrganization},
	// Changing what a role grants is a heavier act than renaming it, but it is
	// the same permission: a caller who can rename "admin" and cannot change it
	// gains nothing from the distinction, and the anti-escalation rule is what
	// actually contains this operation.
	"set-role-permissions": {authz.PermRolesUpdate, authz.ScopeOrganization},
	"delete-role":          {authz.PermRolesDelete, authz.ScopeOrganization},

	"list-audit-events": {authz.PermAuditRead, authz.ScopeOrganization},

	// System scope. No {orgID} in these paths, so the middleware skips the
	// organization lookup entirely and resolves the caller's platform roles
	// instead.
	"list-platform-organizations":  {authz.PermPlatformOrganizationsRead, authz.ScopeSystem},
	"create-platform-organization": {authz.PermPlatformOrganizationsCreate, authz.ScopeSystem},
	"delete-platform-organization": {authz.PermPlatformOrganizationsDelete, authz.ScopeSystem},

	"list-platform-users":   {authz.PermPlatformUsersRead, authz.ScopeSystem},
	"suspend-platform-user": {authz.PermPlatformUsersSuspend, authz.ScopeSystem},
	"delete-platform-user":  {authz.PermPlatformUsersDelete, authz.ScopeSystem},

	"list-platform-audit-events": {authz.PermPlatformAuditRead, authz.ScopeSystem},
}

// requirePermission authorizes every operation that needs a permission.
//
// It runs after requireBearer, so a session is already on the context for
// anything that is not public. The decision it makes is put back on the context
// as an authz.Grant: handlers read the organization and the caller's other
// permissions from there rather than from the request, which is what stops a
// handler acting on an organization other than the one that was checked.
func (s *Server) requirePermission(ctx huma.Context, next func(huma.Context)) {
	op := ctx.Operation()
	if op == nil {
		// Not a route this middleware can classify, so it does not get the
		// benefit of the doubt — the same call requireBearer makes.
		s.forbidden(ctx, "")

		return
	}

	id := op.OperationID

	if publicOperations[id] || selfServiceOperations[id] {
		next(ctx)

		return
	}

	rule, ok := operationAccess[id]
	if !ok {
		// A build that reaches this line has an unclassified operation, which
		// the tests are supposed to have caught. Logged at Error because it is
		// a deployment defect, refused because guessing would be worse.
		problem.LoggerFrom(ctx.Context()).Error("operation has no authorization rule",
			"operation", id, "method", op.Method, "path", op.Path)
		s.forbidden(ctx, "")

		return
	}

	sess, ok := auth.SessionFrom(ctx.Context())
	if !ok {
		s.unauthorized(ctx)

		return
	}

	orgID := uuid.Nil

	if rule.Scope == authz.ScopeOrganization {
		parsed, err := uuid.Parse(ctx.Param(orgIDParam))
		if err != nil {
			_ = huma.WriteErr(s.api, ctx, http.StatusBadRequest, problem.CodeInvalidOrgID)

			return
		}

		orgID = parsed
	}

	if s.deps.Authz == nil {
		s.forbidden(ctx, rule.Permission)

		return
	}

	grant, err := s.deps.Authz.Authorize(ctx.Context(), authz.Request{
		Actor:      sess.UserID,
		Org:        orgID,
		Permission: rule.Permission,
	})
	if err != nil {
		s.denyAuthorization(ctx, rule, err)

		return
	}

	next(huma.WithContext(ctx, authz.WithGrant(ctx.Context(), grant)))
}

// denyAuthorization turns a refusal into the right status.
//
// The 403/404 split is the important part. A caller who is a member of the
// organization already knows it exists, so 403 tells them something true and
// useful. A caller who is not gets 404, identical to the answer for an
// organization that never existed — otherwise trying identifiers reveals which
// tenants an installation has.
func (s *Server) denyAuthorization(ctx huma.Context, rule accessRule, err error) {
	switch {
	case errors.Is(err, authz.ErrNotMember):
		_ = huma.WriteErr(s.api, ctx, http.StatusNotFound, problem.CodeNotFound)

	case errors.Is(err, authz.ErrForbidden):
		s.forbidden(ctx, rule.Permission)

	case errors.Is(err, authz.ErrScopeMismatch), errors.Is(err, authz.ErrUnknownPermission):
		// The rule and the route disagree, or name a permission this build does
		// not define. A configuration defect, so it is logged as one and the
		// caller is simply refused.
		problem.LoggerFrom(ctx.Context()).Error("authorization rule is not usable",
			"permission", rule.Permission, "scope", rule.Scope, "error", err)
		s.forbidden(ctx, rule.Permission)

	default:
		problem.LoggerFrom(ctx.Context()).Error("authorization failed", "error", err)
		_ = huma.WriteErr(s.api, ctx, http.StatusInternalServerError, problem.CodeInternal)
	}
}

// forbidden answers 403 and names the permission that was missing.
//
// Naming it is deliberate. The catalog is readable by any signed-in caller, so
// the name discloses nothing, and without it a client cannot tell "you need a
// role" apart from "this is broken" — which is the difference between a useful
// message and a support ticket.
func (s *Server) forbidden(ctx huma.Context, required authz.Permission) {
	if required == "" {
		_ = huma.WriteErr(s.api, ctx, http.StatusForbidden, problem.CodeForbidden)

		return
	}

	// The permission travels as an error in the variadic list, which is the only
	// channel huma.WriteErr offers beyond a status and a message. The document
	// puts the raw key in required_permission and its translated name in the
	// prose.
	_ = huma.WriteErr(s.api, ctx, http.StatusForbidden, problem.CodeForbiddenNeeds,
		problem.Requires(string(required)))
}
