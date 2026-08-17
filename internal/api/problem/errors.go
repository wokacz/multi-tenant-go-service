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

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
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
//
// It takes a context for two reasons that look unrelated and are not: the
// logger, and the language. Both are properties of the request rather than of
// the error, and both are wrong if guessed.
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

	locale := i18n.LocaleFrom(ctx)

	switch {
	// Deliberately the same body as an organization the caller may not see. In
	// a multi-tenant system, "this exists but is not yours" is a fact worth
	// hiding, so both refusals are one indistinguishable answer.
	case errors.Is(err, user.ErrNotFound),
		errors.Is(err, orgs.ErrNotFound),
		errors.Is(err, authz.ErrNotMember):
		return newDocument(locale, http.StatusNotFound, CodeNotFound)

	case errors.Is(err, user.ErrUnauthorized):
		return newDocument(locale, http.StatusUnauthorized, CodeUnauthorized)

	case errors.Is(err, user.ErrInvalidCredentials):
		return newDocument(locale, http.StatusUnauthorized, CodeInvalidCredentials)

	case errors.Is(err, user.ErrPasswordTooShort):
		return newDocument(locale, http.StatusUnprocessableEntity, CodePasswordTooShort)

	case errors.Is(err, user.ErrPasswordTooLong):
		return newDocument(locale, http.StatusUnprocessableEntity, CodePasswordTooLong)

	case errors.Is(err, user.ErrNameEmpty):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeNameEmpty)

	case errors.Is(err, user.ErrNameTooLong):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeNameTooLong)

	case errors.Is(err, user.ErrPasswordMismatch):
		return newDocument(locale, http.StatusUnprocessableEntity, CodePasswordMismatch)

	case errors.Is(err, user.ErrInvalidResetCode):
		return newDocument(locale, http.StatusUnauthorized, CodeInvalidResetCode)

	case errors.Is(err, user.ErrInvalidTwoFactorCode):
		return newDocument(locale, http.StatusUnauthorized, CodeInvalidTwoFactorCode)

	// 403 rather than 401: the caller's credentials were accepted, so retrying
	// with a better token is not the fix. Naming the reason is the point — the
	// device was blocked on purpose, and only the account holder ever sees it.
	case errors.Is(err, user.ErrDeviceRevoked):
		return newDocument(locale, http.StatusForbidden, CodeDeviceRevoked)

	// 403 rather than 401 for the same reason as a revoked device: the password
	// was accepted, so retrying with better credentials is not the fix.
	case errors.Is(err, user.ErrSuspended):
		return newDocument(locale, http.StatusForbidden, CodeAccountSuspended)

	case errors.Is(err, user.ErrCannotSuspendSelf):
		return newDocument(locale, http.StatusConflict, CodeCannotSuspendSelf)

	case errors.Is(err, user.ErrCannotDeleteSelf):
		return newDocument(locale, http.StatusConflict, CodeCannotDeleteSelf)

	case errors.Is(err, authz.ErrForbidden):
		return newDocument(locale, http.StatusForbidden, CodeForbidden)

	// The caller's credentials were fine and so was their membership; what they
	// asked for was to hand out authority they do not hold. Naming it is the
	// point — a client showing a plain "forbidden" here would send the user
	// looking for a permission they already have.
	case errors.Is(err, authz.ErrPrivilegeEscalation):
		return newDocument(locale, http.StatusForbidden, CodePrivilegeEscalation)

	case errors.Is(err, authz.ErrUnknownPermission):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeUnknownPermission)

	// 422 like an unknown key, and for the same reason: no permission the caller
	// might acquire would make this request succeed. The code is separate because
	// the key is real and the caller read it, with its scope, from the catalog.
	case errors.Is(err, authz.ErrWrongScope):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeWrongScope)

	// 403 and not 404: the caller may read this member, so hiding them would be
	// both useless and confusing. The refusal is about rank, which is why it does
	// not reuse privilege_escalation — nothing is being granted here.
	case errors.Is(err, authz.ErrInsufficientRank):
		return newDocument(locale, http.StatusForbidden, CodeInsufficientRank)

	// 401, like the reset code it mirrors: the caller is holding something that
	// was supposed to prove a fact and it does not.
	case errors.Is(err, user.ErrInvalidEmailCode):
		return newDocument(locale, http.StatusUnauthorized, CodeInvalidEmailCode)

	case errors.Is(err, user.ErrSameEmail):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeSameEmail)

	// Only reachable from confirming an address change. Registration intercepts
	// ErrEmailTaken in the handler and answers 204, so that path cannot be used to
	// probe which addresses exist; here the caller has already read a code out of
	// the mailbox, so the answer costs nothing that was not already theirs.
	case errors.Is(err, user.ErrEmailTaken):
		return newDocument(locale, http.StatusConflict, CodeEmailTaken)

	case errors.Is(err, user.ErrEmailInvalid):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeInvalidEmail)

	// 403 rather than 422: the request is well formed and the role exists, the
	// caller simply may not edit this one, and no rewording of the body would
	// make it succeed.
	case errors.Is(err, orgs.ErrRoleProtected):
		return newDocument(locale, http.StatusForbidden, CodeRoleProtected)

	// 409s: the request is valid but the current state refuses it, and the
	// caller can act on that — reassign the role, promote another owner.
	case errors.Is(err, orgs.ErrLastOwner):
		return newDocument(locale, http.StatusConflict, CodeLastOwner)

	// 410 rather than 404: the token was real, it simply ran out. The holder can
	// act on that — ask for another invitation — and a 404 would send them looking
	// for a mistake they did not make.
	case errors.Is(err, orgs.ErrInvitationExpired):
		return newDocument(locale, http.StatusGone, CodeInvitationExpired)

	// 409, and it names the reason. The caller is holding the token, so the
	// invitation's existence is not a secret from them, and a bare 404 would leave
	// them with nothing to tell whoever invited them.
	case errors.Is(err, orgs.ErrInvitationAddressMismatch):
		return newDocument(locale, http.StatusConflict, CodeInvitationMismatch)

	case errors.Is(err, orgs.ErrRoleInUse):
		return newDocument(locale, http.StatusConflict, CodeRoleInUse)

	case errors.Is(err, orgs.ErrRoleKeyTaken):
		return newDocument(locale, http.StatusConflict, CodeRoleKeyTaken)

	case errors.Is(err, orgs.ErrSlugTaken):
		return newDocument(locale, http.StatusConflict, CodeSlugTaken)

	case errors.Is(err, orgs.ErrAlreadyMember):
		return newDocument(locale, http.StatusConflict, CodeAlreadyMember)

	case errors.Is(err, models.ErrProtected):
		return newDocument(locale, http.StatusConflict, CodeRecordProtected)

	case errors.Is(err, orgs.ErrInvalidName):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeInvalidName)

	case errors.Is(err, orgs.ErrInvalidEmail):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeInvalidEmail)

	case errors.Is(err, orgs.ErrInvalidStatus):
		return newDocument(locale, http.StatusUnprocessableEntity, CodeInvalidStatus)

	case errors.Is(err, context.Canceled):
		return newDocument(locale, statusClientClosedRequest, CodeClientClosed)

	case errors.Is(err, context.DeadlineExceeded):
		LoggerFrom(ctx).Warn("request timed out", "error", err)

		return newDocument(locale, http.StatusGatewayTimeout, CodeTimeout)
	}

	LoggerFrom(ctx).Error("unhandled error", "error", err)

	return newDocument(locale, http.StatusInternalServerError, CodeInternal)
}

// Write renders a problem document by hand for the responses chi produces
// before huma ever sees the request — an unmatched path, an unmatched method, a
// rate limit, a panic. Left to chi they come back as plain text, and would be
// the only non-JSON bodies the API ever emits.
//
// It takes the request rather than only the writer so these responses are
// translated too. A 404 in English on an otherwise Polish API is the kind of
// gap that is invisible in development and obvious to a user.
func Write(w http.ResponseWriter, r *http.Request, status int, code string, args ...any) {
	locale := i18n.Fallback
	if r != nil {
		locale = i18n.LocaleFrom(r.Context())
	}

	doc := newDocument(locale, status, code, nil)
	if len(args) > 0 {
		doc.Detail = i18n.Default().T(locale, "error."+code, args...)
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Content-Language", string(locale))

	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}

	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(doc)
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
