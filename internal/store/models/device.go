package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrDeviceRevoked = errors.New("models: device is revoked")

type Device struct {
	Model

	// UserID leads the composite unique index, so lookups filtered by user
	// alone still use it — no separate single-column index needed.
	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_device_user_fp,priority:1"`
	User   *User     `json:"-"`

	Fingerprint string `gorm:"size:64;not null;uniqueIndex:idx_device_user_fp,priority:2"`
	Label       string `gorm:"size:100"` // optional user-defined label for the device

	UserAgent string `gorm:"size:512"`
	// Pointers so "never seen" is NULL. A zero time.Time or an empty string
	// would be written as year 1 and as '', and Postgres rejects '' for inet.
	LastSeenAt *time.Time `gorm:"index"`
	LastIP     *string    `gorm:"type:inet"`
	TrustedAt  *time.Time // nullable, if set, device is trusted
	RevokedAt  *time.Time
}

func (d *Device) IsTrusted() bool {
	return d.TrustedAt != nil && d.RevokedAt == nil
}

func (d *Device) IsRevoked() bool {
	return d.RevokedAt != nil
}

// Trust marks the device as trusted. A revoked device has to be restored with
// Unrevoke first.
func (d *Device) Trust() error {
	if d.IsRevoked() {
		return ErrDeviceRevoked
	}
	now := time.Now().UTC()
	d.TrustedAt = &now

	return nil
}

// Revoke withdraws trust and blocks the device.
func (d *Device) Revoke() error {
	if d.IsRevoked() {
		return ErrDeviceRevoked
	}
	now := time.Now().UTC()
	d.RevokedAt = &now
	d.TrustedAt = nil

	return nil
}

// Unrevoke lifts the block. Trust is deliberately not restored — the user has
// to confirm the device again.
func (d *Device) Unrevoke() {
	d.RevokedAt = nil
}
