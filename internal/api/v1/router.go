// Package v1 holds version 1 of the HTTP API. Every operation registered here
// lives under /v1, and the package is versioned in the directory tree as well
// as in the path so that a future v2 is a new package beside this one rather
// than a rewrite of it.
package v1

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/user"
)

// Prefix is the path every operation in this package lives under. Operational
// endpoints — health, the OpenAPI document — deliberately sit outside it: they
// are not part of the API contract, and a probe should not have to be
// reconfigured when the contract version changes.
const Prefix = "/v1"

// Deps are the domain services this version needs. A struct rather than a
// parameter list so adding a module later does not ripple through every caller.
type Deps struct {
	Users *user.Service
}

// Register attaches every v1 operation to the API.
func Register(api huma.API, deps Deps) {
	registerUsers(api, deps.Users)
}
