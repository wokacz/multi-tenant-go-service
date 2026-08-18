package models

import (
	"time"

	"github.com/google/uuid"
)

// EmailChange is an address waiting to be proved.
//
// The account's own column is not touched until the code comes back, so an
// address nobody has answered on cannot become the one that receives password
// resets. That is the whole point of the table: changing the address without
// confirming it would turn "change my email" into a way to take over an account
// with nothing but a borrowed token.
//
// The shape deliberately matches PasswordReset — hashed code, expiry, attempt
// counter, consumed marker — because it is the same mechanism with a different
// purpose, and two one-time-code tables that drift apart are two sets of rules to
// remember. The purpose separation lives in the HMAC, not here: see the code
// purposes in internal/domain/user.
type EmailChange struct {
	Model

	UserID uuid.UUID
	User   *User `json:"-"`

	// NewEmail is where the code was sent. There is deliberately no unique index
	// on it: two accounts may have an outstanding change to the same address, and
	// refusing the second would say whether the first exists. Whoever confirms
	// first gets it, and the loser is refused at that point — by which time they
	// have proved they can read the mailbox.
	NewEmail string

	CodeHash   string
	ExpiresAt  time.Time
	Attempts   int
	ConsumedAt *time.Time
}
