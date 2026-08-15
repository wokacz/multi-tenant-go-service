// Package v1 holds version 1 of the HTTP API. Every operation registered here
// lives under /v1, and the package is versioned in the directory tree as well
// as in the path so that a future v2 is a new package beside this one rather
// than a rewrite of it.
package v1

import (
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/auth"
	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/mail"
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
	Log    *slog.Logger
}

// Register attaches every v1 operation to the API.
//
// Authentication is not decided here. internal/api/middleware.go holds an
// allow-list of the operations that may be reached anonymously, and everything
// registered below is behind a token unless it appears there.
func Register(api huma.API, deps Deps) {
	registerUsers(api, deps.Users)
	registerSessions(api, deps)
	registerPasswordResets(api, deps.Users, deps.Mail, deps.Log)
	registerTwoFactor(api, deps.Users)
	registerDevices(api, deps.Users)
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
