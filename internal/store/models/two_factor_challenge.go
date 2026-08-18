package models

import (
	"time"

	"github.com/google/uuid"
)

// TwoFactorChallenge is a single-use code standing between a correct password
// and a session token. It deliberately mirrors PasswordReset: the plaintext is
// never stored, only an HMAC under the same pepper (AUTH_RESET_SECRET), so a
// dump of this table does not let anyone finish someone else's sign-in.
//
// It is bound to a Device as well as a User. Without that binding a code
// emailed after a sign-in attempt from an attacker's machine could be spent
// from the victim's, or the reverse — the code alone would say "someone knows
// this mailbox", not "this browser is allowed in".
type TwoFactorChallenge struct {
	Model

	UserID uuid.UUID
	User   *User `json:"-"`

	// DeviceID is not nullable: a challenge that trusts no particular device
	// would have nothing to mark trusted once it is spent.
	DeviceID uuid.UUID
	Device   *Device `json:"-"`

	CodeHash   string
	ExpiresAt  time.Time
	Attempts   int
	ConsumedAt *time.Time
}
