package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrBatchDeleteUnsupported = errors.New("models: deleting a user requires a primary key so its devices can be revoked")

type User struct {
	Model
	SoftDelete

	Name         string `gorm:"size:100;not null"`
	Email        string `gorm:"size:255;not null;uniqueIndex"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`

	// SessionEpoch is copied into the JWT at issue time and incremented when
	// the password changes. Tokens from before the change then fail even
	// though their signature and expiry are still valid.
	SessionEpoch int `gorm:"not null;default:0"`

	// TwoFactorEnabled turns on the emailed second factor. It gates sign-in
	// from devices the account has not trusted yet; a trusted device skips
	// the challenge, which is what keeps the flow usable day to day.
	TwoFactorEnabled bool `gorm:"not null;default:false"`

	// OnDelete:CASCADE only fires on a hard delete (Unscoped). The ordinary
	// soft delete is handled by BeforeDelete below.
	Devices     []Device     `gorm:"constraint:OnDelete:CASCADE"`
	LoginEvents []LoginEvent `gorm:"constraint:OnDelete:CASCADE"`
}

// BeforeDelete revokes the user's devices. Deleting a User is a soft delete, so
// the FK cascade never runs and the devices would otherwise stay trusted and
// usable after the account is gone.
func (u *User) BeforeDelete(tx *gorm.DB) error {
	if err := u.SoftDelete.BeforeDelete(tx); err != nil {
		return err
	}

	// A batch delete hands the hook a zero-valued receiver, leaving no way to
	// tell whose devices to revoke. Fail loudly rather than skip them.
	if u.ID == uuid.Nil {
		return ErrBatchDeleteUnsupported
	}

	return tx.Model(&Device{}).
		Where("user_id = ? AND revoked_at IS NULL", u.ID).
		Update("revoked_at", time.Now().UTC()).Error
}
