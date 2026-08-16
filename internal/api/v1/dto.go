package v1

import (
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store/models"
)

// minPasswordLength mirrors the minLength tag on CreateUserRequest.Password. A
// struct tag cannot reference a constant, so the two are tied together by the
// compile-time assertion below instead.
const minPasswordLength = 12
const maxNameLength = 100
const resetCodeLength = 6
const twoFactorCodeLength = 6

// If the domain rule and the documented schema ever disagree, one of these
// subtractions underflows and the package stops compiling. The alternative is a
// spec that advertises a limit the service does not enforce.
const (
	_ = uint(user.MinPasswordLength - minPasswordLength)
	_ = uint(minPasswordLength - user.MinPasswordLength)
	_ = uint(user.MaxNameLength - maxNameLength)
	_ = uint(maxNameLength - user.MaxNameLength)
	_ = uint(user.ResetCodeLength - resetCodeLength)
	_ = uint(resetCodeLength - user.ResetCodeLength)
	_ = uint(user.TwoFactorCodeLength - twoFactorCodeLength)
	_ = uint(twoFactorCodeLength - user.TwoFactorCodeLength)
)

// UserResponse is the wire representation of a user.
//
// It exists so that models.User never reaches JSON. The model carries
// PasswordHash, IsProtected and DeletedAt; encoding it directly would leak the
// first and expose internal lifecycle state in the other two. More importantly
// it makes the API contract explicit — adding a column to the model cannot
// silently widen the response.
type UserResponse struct {
	ID               uuid.UUID `json:"id" format:"uuid" doc:"Unique identifier"`
	Name             string    `json:"name" doc:"Display name"`
	Email            string    `json:"email" format:"email" doc:"Email address, normalised to lower case"`
	TwoFactorEnabled bool      `json:"two_factor_enabled" doc:"Whether sign-in from an untrusted device needs an emailed code"`
	Locale           string    `json:"locale,omitempty" doc:"Preferred language, remembered from registration. Outranks Accept-Language."`
	CreatedAt        time.Time `json:"created_at" doc:"When the account was registered"`
}

// newUserResponse is the only place a model becomes a DTO. Keeping the
// conversion in one function means a new model field is invisible to clients
// until someone deliberately adds it here.
func newUserResponse(u *models.User) UserResponse {
	return UserResponse{
		ID:               u.ID,
		Name:             u.Name,
		Email:            u.Email,
		TwoFactorEnabled: u.TwoFactorEnabled,
		Locale:           u.Locale,
		CreatedAt:        u.CreatedAt,
	}
}

// DeviceResponse is a device as its owner sees it.
//
// Fingerprint is absent on purpose. It is the digest of the device token and
// showing it buys a client nothing, while putting the shape of the secret into
// every list response.
type DeviceResponse struct {
	ID        uuid.UUID  `json:"id" format:"uuid" doc:"Unique identifier"`
	Label     string     `json:"label,omitempty" doc:"Name the owner gave this device"`
	UserAgent string     `json:"user_agent,omitempty" doc:"User agent last seen using it"`
	Trusted   bool       `json:"trusted" doc:"Whether it may skip the second factor"`
	Revoked   bool       `json:"revoked" doc:"Whether it is blocked from signing in"`
	Current   bool       `json:"current" doc:"Whether this is the device making the request"`
	LastIP    string     `json:"last_ip,omitempty" doc:"Address last seen using it"`
	LastSeen  *time.Time `json:"last_seen_at,omitempty" doc:"When it was last used, absent if never"`
	CreatedAt time.Time  `json:"created_at" doc:"When it was first seen"`
}

func newDeviceResponse(d *models.Device, currentID uuid.UUID) DeviceResponse {
	out := DeviceResponse{
		ID:        d.ID,
		Label:     d.Label,
		UserAgent: d.UserAgent,
		Trusted:   d.IsTrusted(),
		Revoked:   d.IsRevoked(),
		Current:   d.ID == currentID,
		LastSeen:  d.LastSeenAt,
		CreatedAt: d.CreatedAt,
	}

	if d.LastIP != nil {
		out.LastIP = *d.LastIP
	}

	return out
}

// LoginEventResponse is one line of sign-in history.
type LoginEventResponse struct {
	ID        uuid.UUID  `json:"id" format:"uuid" doc:"Unique identifier"`
	DeviceID  *uuid.UUID `json:"device_id,omitempty" format:"uuid" doc:"Device involved, absent when the attempt failed before one was identified"`
	Outcome   string     `json:"outcome" enum:"success,bad_password,mfa_failed,locked" doc:"How the attempt ended"`
	IP        string     `json:"ip" doc:"Address the attempt came from"`
	UserAgent string     `json:"user_agent,omitempty" doc:"User agent that made the attempt"`
	CreatedAt time.Time  `json:"created_at" doc:"When it happened"`
}

func newLoginEventResponse(e *models.LoginEvent) LoginEventResponse {
	return LoginEventResponse{
		ID:        e.ID,
		DeviceID:  e.DeviceID,
		Outcome:   string(e.Outcome),
		IP:        e.IP,
		UserAgent: e.UserAgent,
		CreatedAt: e.CreatedAt,
	}
}

// CreateUserRequest is the body of POST /v1/users. The constraints here are
// documented in the OpenAPI schema and rejected by huma before the handler
// runs; the domain enforces its own copy so a non-HTTP caller cannot bypass it.
type CreateUserRequest struct {
	Name  string `json:"name" minLength:"1" maxLength:"100" doc:"Display name"`
	Email string `json:"email" format:"email" maxLength:"255" doc:"Email address"`
	// maxLength counts characters while bcrypt's limit is 72 *bytes*, so a
	// multi-byte password can satisfy this and still be rejected by the domain.
	// That path returns 422 rather than a 500.
	Password        string `json:"password" minLength:"12" maxLength:"72" doc:"Plain-text password, hashed before storage"`
	PasswordConfirm string `json:"password_confirm" minLength:"12" maxLength:"72" doc:"Must match password"`
}

// CreateSessionRequest is the body of POST /v1/sessions. minLength is 1 rather
// than 12: repeating the registration policy here would tell a caller whether
// an address was registered under the current rules.
type CreateSessionRequest struct {
	Email    string `json:"email" format:"email" maxLength:"255" doc:"Email address"`
	Password string `json:"password" minLength:"1" maxLength:"72" doc:"Plain-text password"`
}

// RequestPasswordResetRequest starts the reset flow. The response is always
// 204, whether or not the address is registered and whether or not the
// code could be stored or delivered.
type RequestPasswordResetRequest struct {
	Email string `json:"email" format:"email" maxLength:"255" doc:"Email address"`
}

// ConfirmPasswordResetRequest spends a reset code and sets a new password.
type ConfirmPasswordResetRequest struct {
	Email           string `json:"email" format:"email" maxLength:"255" doc:"Email address"`
	Code            string `json:"code" minLength:"6" maxLength:"6" doc:"The code delivered after requesting a reset"`
	Password        string `json:"password" minLength:"12" maxLength:"72" doc:"New password"`
	PasswordConfirm string `json:"password_confirm" minLength:"12" maxLength:"72" doc:"Must match password"`
}

// VerifySessionRequest finishes a sign-in that asked for a second factor. The
// device token from the challenge response has to come back with it: the code
// authorises one device, not one mailbox.
type VerifySessionRequest struct {
	Email string `json:"email" format:"email" maxLength:"255" doc:"Email address"`
	Code  string `json:"code" minLength:"6" maxLength:"6" doc:"The code delivered after signing in"`
}

// SetTwoFactorRequest turns the emailed second factor on or off. The current
// password is required: a stolen token must not be enough to switch off the
// control that exists to contain a stolen token.
type SetTwoFactorRequest struct {
	Password string `json:"password" minLength:"1" maxLength:"72" doc:"Current password"`
	Enabled  bool   `json:"enabled" doc:"Whether sign-in from an untrusted device should need a code"`
}

// SessionResponse is returned by both sign-in steps, and covers two outcomes.
//
// two_factor_required is the discriminator. When it is false the sign-in is
// done and token, expires_at and user are present. When it is true the account
// has the second factor on and this device is not trusted: no token is issued,
// a code has been emailed, and the client finishes at POST /v1/sessions/verify.
//
// One body rather than two endpoints' worth of shapes, because a client has to
// handle both answers from the same call either way.
type SessionResponse struct {
	TwoFactorRequired bool `json:"two_factor_required" doc:"True when a code was emailed and no token was issued"`

	// DeviceToken is present only on the response that first saw this device.
	// It is the client's job to store it and send it back as X-Device-Token;
	// it is never recoverable afterwards, and a client that loses it simply
	// looks like a new device next time.
	DeviceToken string `json:"device_token,omitempty" doc:"Opaque secret identifying this device on later sign-ins"`

	Token     string        `json:"token,omitempty" doc:"Bearer token for subsequent requests"`
	ExpiresAt *time.Time    `json:"expires_at,omitempty" doc:"When the token stops being accepted"`
	User      *UserResponse `json:"user,omitempty"`
}
