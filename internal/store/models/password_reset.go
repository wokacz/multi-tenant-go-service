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

	UserID uuid.UUID `gorm:"type:uuid;not null;index"`
	User   *User     `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	CodeHash   string    `gorm:"size:64;not null"`
	ExpiresAt  time.Time `gorm:"not null;index"`
	Attempts   int       `gorm:"not null"`
	ConsumedAt *time.Time
}
