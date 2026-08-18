package ent

import (
	"time"
)

func (d *Device) IsTrusted() bool {
	return d.TrustedAt != nil && d.RevokedAt == nil
}

func (d *Device) IsRevoked() bool {
	return d.RevokedAt != nil
}

func (d *Device) Trust() error {
	if d.IsRevoked() {
		return ErrDeviceRevoked
	}
	now := time.Now().UTC()
	d.TrustedAt = &now

	return nil
}

func (d *Device) Revoke() error {
	if d.IsRevoked() {
		return ErrDeviceRevoked
	}
	now := time.Now().UTC()
	d.RevokedAt = &now
	d.TrustedAt = nil

	return nil
}

func (d *Device) Unrevoke() {
	d.RevokedAt = nil
}
