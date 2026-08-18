// Package user holds the user-facing domain logic. It knows nothing about HTTP
// or SQL: the transport lives in internal/api, and the persistence in
// internal/store/repositories.
package user

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

var (
	ErrNotFound = errors.New("user: not found")
	// ErrEmailTaken is reported by the repository from the unique constraint
	// rather than by a lookup here — see Service.Create. The HTTP layer
	// treats it as a successful registration so the status cannot be used
	// to probe whether an address is already in the system.
	ErrEmailTaken         = errors.New("user: email already registered")
	ErrPasswordTooLong    = errors.New("user: password is too long")
	ErrPasswordTooShort   = errors.New("user: password is too short")
	ErrNameEmpty          = errors.New("user: name is empty")
	ErrNameTooLong        = errors.New("user: name is too long")
	ErrInvalidCredentials = errors.New("user: invalid credentials")
	ErrUnauthorized       = errors.New("user: unauthorized")
	ErrPasswordMismatch   = errors.New("user: passwords do not match")
	ErrInvalidResetCode   = errors.New("user: invalid reset code")
	// ErrInvalidEmailCode covers a wrong code, an expired one, a spent one and
	// no outstanding change at all — the same reason the reset code has one
	// error for all of them.
	ErrInvalidEmailCode = errors.New("user: invalid email confirmation code")
	// ErrEmailInvalid is an address that is empty or malformed. huma rejects
	// those at the edge, but the rule belongs where the change is made too:
	// nothing guarantees an HTTP request is the only caller.
	ErrEmailInvalid = errors.New("user: email is empty or malformed")
	// ErrLocaleUnsupported refuses a language this build cannot render. The
	// decision needs the catalog, which lives at the edge with the rest of i18n,
	// so the handler makes it and this is the vocabulary it reports it in —
	// storing an unknown tag would silently give somebody the fallback language
	// for good, which is worse than being told the language is not available.
	ErrLocaleUnsupported = errors.New("user: unsupported locale")

	// ErrSameEmail refuses a change to the address the account already has.
	// Sending a code to prove what is already proved is only a way to send
	// mail.
	ErrSameEmail = errors.New("user: the new address is the current one")
	// ErrDeviceRevoked is deliberately distinguishable from bad credentials:
	// it is only ever returned after the password has already been proved, so
	// it discloses nothing to an anonymous caller and telling the account
	// holder "this device was revoked" is the whole point of revoking it.
	ErrDeviceRevoked = errors.New("user: device is revoked")
	// ErrInvalidTwoFactorCode covers a wrong code, an expired one, a spent
	// one, an unknown address and a challenge raised for a different device.
	// VerifyTwoFactor is reachable without credentials, so every one of those
	// has to look the same from outside.
	ErrInvalidTwoFactorCode = errors.New("user: invalid two-factor code")

	// ErrSuspended is returned for an account an administrator has blocked. It
	// is distinguishable from bad credentials on purpose: it is only ever
	// reported to somebody who has already proved who they are, and "your
	// account was suspended" is the whole point of suspending it.
	ErrSuspended = errors.New("user: account is suspended")

	// ErrCannotSuspendSelf and ErrCannotDeleteSelf refuse the two moves that
	// would take away the permission needed to undo them. An administrator who
	// blocks their own account is the one person who could have unblocked it.
	ErrCannotSuspendSelf = errors.New("user: cannot suspend your own account")
	ErrCannotDeleteSelf  = errors.New("user: cannot delete your own account")
)

// MaxNameLength is enforced here rather than only at the API boundary.
const MaxNameLength = 100

// maxConcurrentHashes caps in-flight bcrypt work. The algorithm is
// deliberately slow; without a cap, a burst of registrations or logins from
// many addresses saturates the process even when each address is rate-limited.
const maxConcurrentHashes = 2

// Repository is the persistence this package needs, declared here rather than
// in internal/store on purpose: the consumer owns the interface, so the store
// depends on the domain and not the other way round. It also keeps the
// interface honest — it lists what this package actually uses, not everything
// the store happens to be able to do.
//
// The store implements it. Nothing here knows that GORM exists.
type Repository interface {
	// Create persists u and fills in its generated fields. It returns
	// ErrEmailTaken when the address is already registered.
	Create(ctx context.Context, u *models.User) error

	// ByID returns ErrNotFound when no live user has that id.
	ByID(ctx context.Context, id uuid.UUID) (*models.User, error)

	// ByEmail returns ErrNotFound when no live user has that address.
	ByEmail(ctx context.Context, email string) (*models.User, error)

	// All lists live accounts, newest first, for the installation-wide
	// administration screens. It is not scoped to an organization because
	// nothing about it is: it is the only listing that crosses tenants, and it
	// sits behind a system-scope permission for exactly that reason.
	All(ctx context.Context, limit, offset int) ([]models.User, error)

	// Delete soft deletes an account. The model's hook revokes its devices,
	// which the foreign key cascade does not do for a soft delete.
	Delete(ctx context.Context, userID uuid.UUID) error

	// UpdateProfile writes the account's display name and language preference.
	//
	// Both are always written: the service reads the account first and applies
	// whatever the request left out, so there is no partial-update mode here to
	// get wrong. locale may be empty, which means "no preference" and puts the
	// account back to negotiating per request.
	UpdateProfile(ctx context.Context, userID uuid.UUID, name, locale string) error

	// SetPassword writes a new hash and bumps the session epoch in one statement,
	// so there is no instant where the new password is in force and tokens issued
	// under the old one still work.
	SetPassword(ctx context.Context, userID uuid.UUID, passwordHash string) error

	// BumpSessionEpoch invalidates every token already issued for the account.
	//
	// It is the whole of "sign out everywhere": sessions are JWTs, so there is no
	// list of them to walk — the epoch in the token is compared against the column
	// on every authenticated request, and moving the column is what makes tokens
	// already handed out stop working.
	BumpSessionEpoch(ctx context.Context, userID uuid.UUID) error

	// SetSuspended blocks or unblocks an account. Suspending also bumps the
	// session epoch, so tokens already issued stop working on the next request
	// rather than at expiry.
	SetSuspended(ctx context.Context, userID uuid.UUID, at *time.Time) error

	// ReplacePasswordReset drops unused codes for the user and stores the new
	// one. A user may only have one live reset at a time.
	ReplacePasswordReset(ctx context.Context, reset *models.PasswordReset) error

	// ActivePasswordReset is the unused, unexpired code for userID, or
	// ErrNotFound.
	ActivePasswordReset(ctx context.Context, userID uuid.UUID, now time.Time) (*models.PasswordReset, error)

	// FailPasswordReset records one wrong guess against resetID and spends the
	// code once maxAttempts is reached.
	//
	// It takes an id and a limit rather than a loaded row because the counter
	// has to move in a single conditional UPDATE. Reading the row, adding one
	// and writing it back lets concurrent guesses all read the same value and
	// store the same value, which is how a five-attempt cap stops capping.
	FailPasswordReset(ctx context.Context, resetID uuid.UUID, maxAttempts int, now time.Time) error

	// ConsumePasswordReset writes the new password hash, increments the session
	// epoch so tokens issued under the old password stop working, and marks
	// the code used — all in one transaction, so a crash cannot leave a
	// consumed code with the old password still in force.
	ConsumePasswordReset(ctx context.Context, reset *models.PasswordReset, passwordHash string) error

	// ReplaceEmailChange drops any unused change for the user and stores the new
	// one, so asking again supersedes rather than accumulating codes that all
	// still work.
	ReplaceEmailChange(ctx context.Context, change *models.EmailChange) error

	// ActiveEmailChange is the unused, unexpired change for userID, or
	// ErrNotFound.
	ActiveEmailChange(ctx context.Context, userID uuid.UUID, now time.Time) (*models.EmailChange, error)

	// FailEmailChange records one wrong guess and spends the code once the cap is
	// reached, in one conditional UPDATE. Same reasoning as FailPasswordReset: a
	// read-modify-write would let concurrent guesses share an attempt.
	FailEmailChange(ctx context.Context, changeID uuid.UUID, maxAttempts int, now time.Time) error

	// ConsumeEmailChange writes the new address and marks the code spent in one
	// transaction. It returns ErrEmailTaken when the address was claimed in the
	// meantime — by then the caller has proved they can read that mailbox, which
	// is why this is the one place the answer is given.
	ConsumeEmailChange(ctx context.Context, change *models.EmailChange, email string) error

	// DeviceByFingerprint returns the caller's device with that fingerprint, or
	// ErrNotFound. It is scoped by user, so one account's device token can
	// never resolve to another account's device.
	DeviceByFingerprint(ctx context.Context, userID uuid.UUID, fingerprint string) (*models.Device, error)

	// CreateDevice persists a newly seen device.
	CreateDevice(ctx context.Context, device *models.Device) error

	// TouchDevice records that the device was just used. It is a targeted
	// UPDATE rather than a Save of a loaded row so that a concurrent revoke is
	// not silently written back to NULL.
	TouchDevice(ctx context.Context, deviceID uuid.UUID, seenAt time.Time, ip, userAgent string) error

	// TrustDevice marks the device as having passed a second factor. The
	// timestamp comes from models.Device rather than from a parameter,
	// because that type owns what trusting a device means.
	TrustDevice(ctx context.Context, deviceID uuid.UUID) error

	// Devices lists the caller's devices, most recently seen first.
	Devices(ctx context.Context, userID uuid.UUID) ([]models.Device, error)

	// RevokeDevice withdraws trust and blocks the device. It returns
	// ErrNotFound when the device is not the caller's, and ErrDeviceRevoked
	// when it was already revoked.
	RevokeDevice(ctx context.Context, userID, deviceID uuid.UUID) error

	// ActiveDevice returns the device only when it belongs to userID and has
	// not been revoked. The bearer middleware calls it on every authenticated
	// request, which is what makes revocation take effect on tokens that were
	// already handed out.
	ActiveDevice(ctx context.Context, userID, deviceID uuid.UUID) (*models.Device, error)

	// RecordLoginEvent appends to the login history.
	RecordLoginEvent(ctx context.Context, event *models.LoginEvent) error

	// LoginEvents returns the caller's most recent login history, newest first.
	LoginEvents(ctx context.Context, userID uuid.UUID, limit int) ([]models.LoginEvent, error)

	// SetTwoFactorEnabled flips the account's second-factor flag.
	SetTwoFactorEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error

	// ReplaceTwoFactorChallenge drops the user's unspent challenges and stores
	// the new one, so a sign-in attempt always invalidates the previous code.
	ReplaceTwoFactorChallenge(ctx context.Context, challenge *models.TwoFactorChallenge) error

	// ActiveTwoFactorChallenge is the unspent, unexpired challenge for userID,
	// or ErrNotFound.
	ActiveTwoFactorChallenge(ctx context.Context, userID uuid.UUID, now time.Time) (*models.TwoFactorChallenge, error)

	// FailTwoFactorChallenge is FailPasswordReset for the sign-in code, and
	// atomic for the same reason.
	FailTwoFactorChallenge(ctx context.Context, challengeID uuid.UUID, maxAttempts int, now time.Time) error

	// ConsumeTwoFactorChallenge spends the challenge and trusts the device in
	// one transaction, so a crash cannot leave a code spent without the device
	// it was meant to authorise ever becoming trusted.
	ConsumeTwoFactorChallenge(ctx context.Context, challengeID, deviceID uuid.UUID, at time.Time) error
}
