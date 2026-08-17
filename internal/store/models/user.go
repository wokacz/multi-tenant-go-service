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

	Name string `gorm:"size:100;not null"`
	// Unique only among live accounts. A plain unique index plus a soft delete
	// means a deleted account occupies its address for ever: nobody could register
	// it again, and because registration hides a duplicate behind 204 to avoid an
	// enumeration oracle, the person trying would be told it worked and then never
	// be able to sign in. The partial index frees the address while the old row —
	// and the address on it — stays, so the audit trail can still say who did what.
	Email        string `gorm:"size:255;not null;index:idx_users_email,unique,where:deleted_at IS NULL"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`

	// SessionEpoch is copied into the JWT at issue time and incremented when
	// the password changes. Tokens from before the change then fail even
	// though their signature and expiry are still valid.
	SessionEpoch int `gorm:"not null;default:0"`

	// TwoFactorEnabled turns on the emailed second factor. It gates sign-in
	// from devices the account has not trusted yet; a trusted device skips
	// the challenge, which is what keeps the flow usable day to day.
	TwoFactorEnabled bool `gorm:"not null;default:false"`

	// SuspendedAt blocks the account everywhere without deleting it.
	//
	// It is separate from the soft delete because the two mean different things
	// to an administrator and to the person: a suspension is reversible and the
	// account keeps its memberships and history, a deletion is not. The bearer
	// middleware checks it on every request, which is what makes suspending take
	// effect on tokens that were already handed out — the same promise device
	// revocation makes.
	SuspendedAt *time.Time

	// Locale is the account's preferred language as a BCP 47 tag, empty when
	// the user has not chosen one. It outranks Accept-Language for responses,
	// and it is the only language signal mail has: a message sent from a
	// background job has no request headers to negotiate from.
	Locale string `gorm:"size:10"`

	// OnDelete:CASCADE only fires on a hard delete (Unscoped). The ordinary
	// soft delete is handled by BeforeDelete below.
	Devices     []Device     `gorm:"constraint:OnDelete:CASCADE"`
	LoginEvents []LoginEvent `gorm:"constraint:OnDelete:CASCADE"`
}

// IsSuspended reports whether the account is blocked.
func (u *User) IsSuspended() bool {
	return u.SuspendedAt != nil
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
