package models

import (
	"time"

	"github.com/google/uuid"
)

// PasswordReset is a single-use code that lets the account holder set a new
// password without the old one. The plaintext code is never stored — only an
// HMAC, so a dump of this table is not enough to reset anyone without the
// process secret as well.
type PasswordReset struct {
	Model

	UserID uuid.UUID `gorm:"type:uuid;not null;index"`
	User   *User     `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	CodeHash   string    `gorm:"size:64;not null"`
	ExpiresAt  time.Time `gorm:"not null;index"`
	Attempts   int       `gorm:"not null"`
	ConsumedAt *time.Time
}
