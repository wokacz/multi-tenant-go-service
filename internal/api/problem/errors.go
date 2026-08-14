// Package problem maps domain errors onto HTTP Problem Details
// (RFC 7807 / application/problem+json).
//
// It cannot live as internal/api/errors.go: package api imports v1 to
// register routes, so v1 cannot import api. A package named errors would
// shadow the standard library.
package problem

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store/models"
)

// statusClientClosedRequest is nginx's convention for "the client hung up
// before we answered". Nothing can be written to a closed connection anyway;
// the point is that these show up in logs as disconnects rather than as 500s.
const statusClientClosedRequest = 499

// Error translates a domain error into the response huma should send. Handlers
// return Error(ctx, err) instead of the raw error, so the mapping from a domain
// rule to an HTTP status is decided in exactly one place and the layers below
// never have to know what a status code is.
//
// Note what is absent: gorm. The repositories translate storage errors into
// domain errors, so this package maps only domain vocabulary. A driver error
// arriving here means a repository forgot to translate one, and it correctly
// becomes an opaque 500.
//
// Anything unmapped is treated as a bug: the real error goes to the log with
// the request id attached, and the client gets that opaque 500. Unmapped errors
// carry table names, query fragments and occasionally credentials, none of
// which belong in a response body.
func Error(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	// A handler that already picked a status — huma.Error400BadRequest for a
	// validation failure, say — keeps it. Re-wrapping would hide the intent.
	var statusErr huma.StatusError
	if errors.As(err, &statusErr) {
		return statusErr
	}

	switch {
	case errors.Is(err, user.ErrNotFound):
		return huma.Error404NotFound("user not found")

	case errors.Is(err, user.ErrUnauthorized):
		return huma.Error401Unauthorized("unauthorized")

	case errors.Is(err, user.ErrInvalidCredentials):
		return huma.Error401Unauthorized("invalid credentials")

	case errors.Is(err, user.ErrPasswordTooShort):
		return huma.Error422UnprocessableEntity("password is too short")

	case errors.Is(err, user.ErrPasswordTooLong):
		return huma.Error422UnprocessableEntity("password is too long")

	case errors.Is(err, user.ErrNameEmpty):
		return huma.Error422UnprocessableEntity("name is empty")

	case errors.Is(err, user.ErrNameTooLong):
		return huma.Error422UnprocessableEntity("name is too long")

	case errors.Is(err, models.ErrProtected):
		return huma.Error409Conflict("record is protected from deletion")

	case errors.Is(err, models.ErrDeviceRevoked):
		return huma.Error409Conflict("device is revoked")

	case errors.Is(err, context.Canceled):
		return huma.NewError(statusClientClosedRequest, http.StatusText(statusClientClosedRequest))

	case errors.Is(err, context.DeadlineExceeded):
		LoggerFrom(ctx).Warn("request timed out", "error", err)

		return huma.Error504GatewayTimeout("request timed out")
	}

	// models.ErrBatchDeleteUnsupported lands here on purpose: it means a caller
	// issued a delete without a primary key, which is a bug in our code rather
	// than something the client did wrong.
	LoggerFrom(ctx).Error("unhandled error", "error", err)

	return huma.Error500InternalServerError("internal server error")
}

// Write renders a huma.ErrorModel by hand for the responses chi produces
// before huma ever sees the request — an unmatched path and an unmatched
// method. Left to chi they come back as plain text, and would be the only
// non-JSON bodies the API ever emits.
func Write(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}

	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(huma.ErrorModel{
		Status: status,
		Title:  http.StatusText(status),
		Detail: detail,
	})
}

// ctxKey keeps the context key unexported so no other package can collide with
// it, which is the whole reason for the named type.
type ctxKey int

const loggerKey ctxKey = iota

// WithLogger attaches the request-scoped logger, so errors surfaced anywhere
// downstream are logged against the request they belong to.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, log)
}

// LoggerFrom returns the logger installed by WithLogger. The fallback matters
// for tests and for anything running outside a request, where losing the log
// line entirely would be worse than losing the request id.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return log
	}

	return slog.Default()
}
