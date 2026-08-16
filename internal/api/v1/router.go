// Package v1 holds version 1 of the HTTP API. Every operation registered here
// lives under /v1, and the package is versioned in the directory tree as well
// as in the path so that a future v2 is a new package beside this one rather
// than a rewrite of it.
package v1

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
)

// Prefix is the path every operation in this package lives under. Operational
// endpoints — health, the OpenAPI document — deliberately sit outside it: they
// are not part of the API contract, and a probe should not have to be
// reconfigured when the contract version changes.
const Prefix = "/v1"

// Deps are the domain services this version needs. A struct rather than a
// parameter list so adding a module later does not ripple through every caller.
type Deps struct {
	Users  *user.Service
	Tokens *auth.Signer
	Mail   mail.Sender
	Orgs   *orgs.Service
	Authz  authz.Snapshotter
	Audit  *audit.Service
	Log    *slog.Logger
}

// Register attaches every v1 operation to the API.
//
// Authentication is not decided here. internal/api/middleware.go holds an
// allow-list of the operations that may be reached anonymously, and everything
// registered below is behind a token unless it appears there.
func Register(api huma.API, deps Deps) {
	registerUsers(api, deps.Users, deps.Orgs)
	registerSessions(api, deps)
	registerPasswordResets(api, deps.Users, deps.Mail, deps.Log)
	registerTwoFactor(api, deps.Users)
	registerDevices(api, deps.Users)
	registerOrganizations(api, deps.Orgs)
	registerMembers(api, deps.Orgs)
	registerRoles(api, deps.Orgs)
	registerPermissions(api, deps.Orgs, deps.Authz)
	registerPlatform(api, deps.Orgs, deps.Users)
	registerAudit(api, deps.Audit)
}

// logger falls back to the default when no logger was wired in. It matters for
// tests and for Spec(), which builds a server with zero dependencies; losing a
// log line there is better than a nil dereference while rendering the contract.
func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}

	return slog.Default()
}

// bearer is the security block every protected operation carries. Spelled once
// rather than repeated, because the map literal is easy to mistype into
// something that still compiles and quietly documents no security at all.
func bearer() []map[string][]string {
	return []map[string][]string{{"bearer": {}}}
}

// orgErrors are the statuses every organization-scoped operation can produce
// before its handler runs: no token, a permission the caller lacks, and the 404
// a non-member receives instead of a 403.
//
// Listing them is not decoration. A status missing from Errors is missing from
// the OpenAPI document and from every generated client, so a client written
// against the spec would have no branch for being refused.
func orgErrors() []int {
	return []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
}

// grantFrom is how every organization-scoped handler gets its context.
//
// The organization and the caller's permissions come from the decision the
// middleware already made, never from the request. A handler that read the
// {orgID} path parameter instead could act on an organization other than the
// one that was authorized; TestHandlersDoNotReadTheOrgIDParameter enforces that
// none of them do.
//
// Reaching here without a grant means the middleware chain was rewired, so it
// refuses rather than falling back to the request — the exact substitution the
// grant exists to prevent.
func grantFrom(ctx context.Context) (*authz.Grant, error) {
	grant, ok := authz.GrantFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	return grant, nil
}
