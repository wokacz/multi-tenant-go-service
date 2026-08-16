package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// This file holds the sign-in side of user.Repository: devices, login history
// and the emailed second factor. It is the same type as user.go — the split is
// for reading, not for scope.

func (r *User) DeviceByFingerprint(ctx context.Context, userID uuid.UUID, fingerprint string) (*models.Device, error) {
	var device models.Device

	err := r.db.WithContext(ctx).
		Where("user_id = ? AND fingerprint = ?", userID, fingerprint).
		First(&device).Error
	if err == nil {
		return &device, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: device by fingerprint: %w", err)
}

func (r *User) ActiveDevice(ctx context.Context, userID, deviceID uuid.UUID) (*models.Device, error) {
	var device models.Device

	err := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", deviceID, userID).
		First(&device).Error
	if err == nil {
		return &device, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: active device: %w", err)
}

func (r *User) CreateDevice(ctx context.Context, device *models.Device) error {
	if err := r.db.WithContext(ctx).Create(device).Error; err != nil {
		return fmt.Errorf("store: create device: %w", err)
	}

	return nil
}

// TouchDevice is a targeted UPDATE rather than a Save of the loaded row. A Save
// would write back every column the caller happened to be holding, including a
// revoked_at that another request had just set.
func (r *User) TouchDevice(ctx context.Context, deviceID uuid.UUID, seenAt time.Time, ip, userAgent string) error {
	err := r.db.WithContext(ctx).
		Model(&models.Device{}).
		Where("id = ?", deviceID).
		Updates(map[string]any{
			"last_seen_at": seenAt,
			// The cast is explicit: the column is inet, and leaving the driver
			// to infer the parameter type from a bare string is how this turns
			// into a runtime error on the first non-loopback address.
			"last_ip":    gorm.Expr("?::inet", ip),
			"user_agent": userAgent,
		}).Error
	if err != nil {
		return fmt.Errorf("store: touch device: %w", err)
	}

	return nil
}

// TrustDevice and RevokeDevice both load the row FOR UPDATE and then apply the
// rules from models.Device, so "a revoked device cannot be trusted" and
// "revoking clears trust" have one definition rather than one in the model and
// a second one written out in SQL here.
func (r *User) TrustDevice(ctx context.Context, deviceID uuid.UUID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		device, err := lockDevice(tx, "id = ?", deviceID)
		if err != nil {
			return err
		}

		if err := device.Trust(); err != nil {
			return err
		}

		return tx.Model(device).Update("trusted_at", device.TrustedAt).Error
	})

	return translateDeviceError("trust device", err)
}

func (r *User) RevokeDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		device, err := lockDevice(tx, "id = ? AND user_id = ?", deviceID, userID)
		if err != nil {
			return err
		}

		if err := device.Revoke(); err != nil {
			return err
		}

		return tx.Model(device).Updates(map[string]any{
			"revoked_at": device.RevokedAt,
			"trusted_at": device.TrustedAt,
		}).Error
	})

	// Revoking twice is not a failure. The caller asked for the device to be
	// blocked and it is blocked; answering 409 would only make clients write
	// retry logic around a state they already have.
	if errors.Is(err, models.ErrDeviceRevoked) {
		return nil
	}

	return translateDeviceError("revoke device", err)
}

func (r *User) Devices(ctx context.Context, userID uuid.UUID) ([]models.Device, error) {
	var devices []models.Device

	// NULLS LAST so a device that has never been seen sorts after ones that
	// have, rather than to the top where Postgres puts NULL by default on DESC.
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("last_seen_at DESC NULLS LAST").
		Order("created_at DESC").
		Find(&devices).Error
	if err != nil {
		return nil, fmt.Errorf("store: devices: %w", err)
	}

	return devices, nil
}

func (r *User) RecordLoginEvent(ctx context.Context, event *models.LoginEvent) error {
	if err := r.db.WithContext(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("store: record login event: %w", err)
	}

	return nil
}

func (r *User) LoginEvents(ctx context.Context, userID uuid.UUID, limit int) ([]models.LoginEvent, error) {
	var events []models.LoginEvent

	// user_id then created_at is the column order of idx_login_user_time, so
	// this reads the index rather than sorting the user's whole history.
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&events).Error
	if err != nil {
		return nil, fmt.Errorf("store: login events: %w", err)
	}

	return events, nil
}

func (r *User) SetTwoFactorEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error {
	err := r.db.WithContext(ctx).
		Model(&models.User{}).
		Where("id = ?", userID).
		Update("two_factor_enabled", enabled).Error
	if err != nil {
		return fmt.Errorf("store: set two factor: %w", err)
	}

	return nil
}

func (r *User) ReplaceTwoFactorChallenge(ctx context.Context, challenge *models.TwoFactorChallenge) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND consumed_at IS NULL", challenge.UserID).
			Delete(&models.TwoFactorChallenge{}).Error; err != nil {
			return err
		}

		return tx.Create(challenge).Error
	})
	if err != nil {
		return fmt.Errorf("store: replace two factor challenge: %w", err)
	}

	return nil
}

func (r *User) ActiveTwoFactorChallenge(ctx context.Context, userID uuid.UUID, now time.Time) (*models.TwoFactorChallenge, error) {
	var challenge models.TwoFactorChallenge

	err := r.db.WithContext(ctx).
		Where("user_id = ? AND consumed_at IS NULL AND expires_at > ?", userID, now).
		Order("created_at DESC").
		First(&challenge).Error
	if err == nil {
		return &challenge, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: active two factor challenge: %w", err)
}

// FailTwoFactorChallenge is FailPasswordReset for the sign-in code; see the
// comment there for why the counter cannot be read, incremented and written.
func (r *User) FailTwoFactorChallenge(ctx context.Context, challengeID uuid.UUID, maxAttempts int, now time.Time) error {
	err := r.db.WithContext(ctx).
		Model(&models.TwoFactorChallenge{}).
		Where("id = ? AND consumed_at IS NULL", challengeID).
		Updates(map[string]any{
			"attempts":    gorm.Expr("attempts + 1"),
			"consumed_at": gorm.Expr("CASE WHEN attempts + 1 >= ? THEN ?::timestamptz ELSE consumed_at END", maxAttempts, now),
		}).Error
	if err != nil {
		return fmt.Errorf("store: fail two factor challenge: %w", err)
	}

	return nil
}

func (r *User) ConsumeTwoFactorChallenge(ctx context.Context, challengeID, deviceID uuid.UUID, at time.Time) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Spending the code and trusting the device have to land together. A
		// crash between them would burn the code and leave the device asking
		// for a new one on every sign-in.
		res := tx.Model(&models.TwoFactorChallenge{}).
			Where("id = ? AND consumed_at IS NULL", challengeID).
			Update("consumed_at", at)
		if res.Error != nil {
			return res.Error
		}

		// Zero rows means a concurrent request spent the same code first.
		// Trusting the device anyway would let one code be redeemed twice.
		if res.RowsAffected == 0 {
			return user.ErrInvalidTwoFactorCode
		}

		device, err := lockDevice(tx, "id = ?", deviceID)
		if err != nil {
			return err
		}

		if err := device.Trust(); err != nil {
			return err
		}

		return tx.Model(device).Update("trusted_at", device.TrustedAt).Error
	})
	if err != nil {
		return translateDeviceError("consume two factor challenge", err)
	}

	return nil
}

// lockDevice reads a device FOR UPDATE so the rules applied to it afterwards
// cannot race a concurrent trust or revoke.
func lockDevice(tx *gorm.DB, query string, args ...any) (*models.Device, error) {
	var device models.Device

	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(query, args...).
		First(&device).Error; err != nil {
		return nil, err
	}

	return &device, nil
}

// translateDeviceError turns the model's and GORM's vocabulary into the
// domain's. This is the boundary the whole error chain depends on: nothing
// above internal/store is allowed to see gorm.ErrRecordNotFound or
// models.ErrDeviceRevoked.
func translateDeviceError(op string, err error) error {
	switch {
	case err == nil:
		return nil

	case errors.Is(err, gorm.ErrRecordNotFound):
		return user.ErrNotFound

	case errors.Is(err, models.ErrDeviceRevoked):
		return user.ErrDeviceRevoked

	case errors.Is(err, user.ErrInvalidTwoFactorCode):
		return err

	default:
		return fmt.Errorf("store: %s: %w", op, err)
	}
}
