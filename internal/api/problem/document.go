package problem

import (
	"errors"
	"net/http"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

// Codes are the stable, machine-readable names for every refusal this API can
// produce. They are the contract; the detail beside them is prose that changes
// with the reader's language and may be reworded at any time.
//
// A client keys its own messages off the code and never parses the detail.
const (
	CodeUnauthorized     = "unauthorized"
	CodeForbidden        = "forbidden"
	CodeForbiddenNeeds   = "forbidden_requires"
	CodeNotFound         = "not_found"
	CodeInternal         = "internal"
	CodeTimeout          = "timeout"
	CodeClientClosed     = "client_closed"
	CodeTooManyRequests  = "too_many_requests"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeNoOperation      = "no_operation"
	CodeValidationFailed = "validation_failed"

	CodeInvalidCredentials   = "invalid_credentials"
	CodeInvalidResetCode     = "invalid_reset_code"
	CodeInvalidTwoFactorCode = "invalid_two_factor_code"
	CodePasswordTooShort     = "password_too_short"
	CodePasswordTooLong      = "password_too_long"
	CodePasswordMismatch     = "password_mismatch"
	CodeNameEmpty            = "name_empty"
	CodeNameTooLong          = "name_too_long"
	CodeDeviceRevoked        = "device_revoked"
	CodeAccountSuspended     = "account_suspended"
	CodeCannotSuspendSelf    = "cannot_suspend_self"
	CodeCannotDeleteSelf     = "cannot_delete_self"

	CodePrivilegeEscalation = "privilege_escalation"
	CodeUnknownPermission   = "unknown_permission"
	CodeWrongScope          = "wrong_scope"
	CodeInsufficientRank    = "insufficient_rank"
	CodeInvalidEmailCode    = "invalid_email_code"
	CodeSameEmail           = "same_email"
	CodeUnsupportedLocale   = "unsupported_locale"
	CodeEmailTaken          = "email_taken"
	CodeInvitationExpired   = "invitation_expired"
	CodeInvitationMismatch  = "invitation_address_mismatch"
	CodeInvalidSystemRole   = "invalid_system_role"
	CodeLastSystemRole      = "last_system_role"
	CodeRoleProtected       = "role_protected"
	CodeLastOwner           = "last_owner"
	CodeRoleInUse           = "role_in_use"
	CodeRoleKeyTaken        = "role_key_taken"
	CodeSlugTaken           = "slug_taken"
	CodeAlreadyMember       = "already_member"
	CodeInvalidBatch        = "invalid_invitation_batch"
	CodeRecordProtected     = "record_protected"
	CodeInvalidName         = "invalid_name"
	CodeInvalidEmail        = "invalid_email"
	CodeInvalidStatus       = "invalid_status"
	CodeInvalidOrgID        = "invalid_organization_id"
)

// Document is the RFC 7807 body this API emits, with two fields of its own.
//
// It replaces huma.ErrorModel rather than wrapping it, because the OpenAPI
// schema for every error response is reflected off whatever huma.NewError
// returns — see Install. Without the replacement, code and required_permission
// would be in the responses and absent from the contract, and every generated
// client would be missing them.
type Document struct {
	Type     string              `json:"type,omitempty" format:"uri" doc:"A URI identifying the problem type"`
	Title    string              `json:"title,omitempty" doc:"Short, human-readable summary of the problem type"`
	Status   int                 `json:"status,omitempty" doc:"HTTP status code"`
	Detail   string              `json:"detail,omitempty" doc:"Human-readable explanation, in the negotiated language. Never parse this — use code."`
	Instance string              `json:"instance,omitempty" format:"uri" doc:"A URI identifying this occurrence"`
	Errors   []*huma.ErrorDetail `json:"errors,omitempty" doc:"Field-level problems, when the request failed validation"`

	// Code is stable across languages and releases. It is what a client
	// switches on, and what it uses to look up its own wording.
	Code string `json:"code,omitempty" doc:"Stable machine-readable identifier for this refusal"`

	// RequiredPermission is set on a 403 that a role change could fix. It is
	// the raw permission key, not the translated name, so a client can compare
	// it against the permission catalog.
	RequiredPermission string `json:"required_permission,omitempty" doc:"Permission key the caller was missing"`
}

func (d *Document) Error() string {
	return d.Detail
}

func (d *Document) GetStatus() int {
	return d.Status
}

// ContentType keeps every error body on application/problem+json. huma's own
// ErrorModel does the same; dropping it here would silently change the content
// type of every error the API has ever returned.
func (d *Document) ContentType(string) string {
	return "application/problem+json"
}

// Add appends a field-level problem. huma calls it when it aggregates
// validation failures.
func (d *Document) Add(err error) {
	if converted, ok := err.(huma.ErrorDetailer); ok {
		d.Errors = append(d.Errors, converted.ErrorDetail())

		return
	}

	if err != nil {
		d.Errors = append(d.Errors, &huma.ErrorDetail{Message: err.Error()})
	}
}

// requirement travels through huma's variadic error list to carry the missing
// permission from the middleware to the document.
//
// huma.WriteErr only takes a status and a message, and the alternative — a
// second global for the middleware to stash the value in — would be shared
// mutable state on a request path.
type requirement struct {
	permission string
}

func (r requirement) Error() string {
	return "requires " + r.permission
}

// Requires marks a refusal as fixable by granting a permission.
func Requires(permission string) error {
	return requirement{permission: permission}
}

// humaMessages maps huma's own internal messages onto codes, so the responses it
// writes without going through this package still carry one.
var humaMessages = map[string]string{
	"validation failed":         CodeValidationFailed,
	"unexpected error occurred": CodeInternal,
}

var installOnce sync.Once

// Install replaces huma's error constructors.
//
// Both are needed and they do different jobs. NewError has no context, so it
// cannot translate — it exists for the one call huma makes at registration time
// to reflect the schema, and for any caller outside a request.
// NewErrorWithContext is what runs for every error huma writes itself, and it
// has the request context, which is where the negotiated language lives.
//
// Replacing package-level variables is not something to do lightly. It is done
// once, guarded, and before any route is registered, because defineErrors reads
// NewError while building each operation's responses.
func Install() {
	installOnce.Do(func() {
		huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
			return newDocument(i18n.Fallback, status, msg, errs...)
		}

		huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
			locale := i18n.Fallback
			if ctx != nil {
				locale = i18n.LocaleFrom(ctx.Context())
			}

			return newDocument(locale, status, msg, errs...)
		}
	})
}

// newDocument builds a localised problem document.
//
// The message is treated as a code first: if the catalog knows it, the response
// carries the code and the translated prose. If it does not — huma's own
// wording, or a message from a library — it is passed through verbatim with no
// code, which is better than emitting a code no client can look up.
func newDocument(locale i18n.Locale, status int, msg string, errs ...error) *Document {
	doc := &Document{Status: status, Title: http.StatusText(status)}

	var args []any

	for _, err := range errs {
		var req requirement
		if errors.As(err, &req) {
			doc.RequiredPermission = req.permission
			// The prose gets the permission's translated name; the field keeps
			// the raw key, so the message reads well and the client still has
			// something to compare against the catalog.
			args = append(args, permissionName(locale, req.permission))

			continue
		}

		doc.Add(err)
	}

	if code, ok := humaMessages[msg]; ok {
		msg = code
	}

	if msg == "" {
		return doc
	}

	key := "error." + msg

	catalog := i18n.Default()
	if catalog.Has(locale, key) || catalog.Has(i18n.Fallback, key) {
		doc.Code = msg
		doc.Detail = catalog.T(locale, key, args...)

		return doc
	}

	doc.Detail = msg

	return doc
}

// permissionName is the catalog's label for a permission, falling back to the
// key when there is no translation — which the completeness tests make
// unreachable, but a fallback that shows the key beats one that shows nothing.
func permissionName(locale i18n.Locale, permission string) string {
	return i18n.Default().T(locale, "permission."+permission+".name")
}
