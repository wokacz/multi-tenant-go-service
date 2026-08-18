package models

import (
	"time"

	"github.com/google/uuid"
)

// PasswordReset is a single-use code that lets the account holder set a new
// password without the old one. The plaintext code is never stored — only an
// HMAC, so a dump of this table is not enough to reset anyone without the
// reset-code pepper (AUTH_RESET_SECRET) as well. That pepper is independent
// of the JWT signing secret so rotating session tokens does not invalidate
// codes already emailed.
type PasswordReset struct {
	Model

	UserID uuid.UUID
	User   *User `json:"-"`

	CodeHash   string
	ExpiresAt  time.Time
	Attempts   int
	ConsumedAt *time.Time
}
