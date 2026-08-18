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
	UserID uuid.UUID
	User   *User `json:"-"`

	// Fingerprint is the SHA-256 of the opaque device token the client holds,
	// hex-encoded — hence size 64. The token itself is never stored, so this
	// table cannot be replayed into someone else's trusted device.
	Fingerprint string
	Label       string // optional user-defined label for the device

	UserAgent string
	// Pointers so "never seen" is NULL. A zero time.Time or an empty string
	// would be written as year 1 and as '', and Postgres rejects '' for inet.
	LastSeenAt *time.Time
	LastIP     *string
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
