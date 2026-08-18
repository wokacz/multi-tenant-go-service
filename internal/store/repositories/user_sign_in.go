package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/device"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/loginevent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/predicate"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/twofactorchallenge"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// This file holds the sign-in side of user.Repository: devices, login history and the
// emailed second factor. It is the same type as user.go — the split is for reading, not
// for scope.

func (r *User) DeviceByFingerprint(ctx context.Context, userID uuid.UUID, fingerprint string) (*models.Device, error) {
	row, err := r.db.Ent().Device.Query().
		Where(
			device.UserID(userID),
			device.Fingerprint(fingerprint),
		).
		First(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: device by fingerprint: %w", err)
	}

	return deviceModel(row), nil
}

func (r *User) ActiveDevice(ctx context.Context, userID, deviceID uuid.UUID) (*models.Device, error) {
	row, err := r.db.Ent().Device.Query().
		Where(
			device.ID(deviceID),
			device.UserID(userID),
			device.RevokedAtIsNil(),
		).
		First(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: active device: %w", err)
	}

	return deviceModel(row), nil
}

func (r *User) CreateDevice(ctx context.Context, d *models.Device) error {
	created, err := r.db.Ent().Device.Create().
		SetUserID(d.UserID).
		SetFingerprint(d.Fingerprint).
		SetLabel(d.Label).
		SetUserAgent(d.UserAgent).
		SetNillableLastSeenAt(d.LastSeenAt).
		SetNillableLastIP(d.LastIP).
		SetNillableTrustedAt(d.TrustedAt).
		SetNillableRevokedAt(d.RevokedAt).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: create device: %w", err)
	}

	// The caller reads the generated id back off the struct it passed in, the way it
	// did when GORM filled it in.
	d.ID = created.ID
	d.CreatedAt = created.CreatedAt
	d.UpdatedAt = created.UpdatedAt

	return nil
}

// TouchDevice is a targeted UPDATE rather than a save of the loaded row. A save would
// write back every column the caller happened to be holding, including a revoked_at
// that another request had just set.
func (r *User) TouchDevice(ctx context.Context, deviceID uuid.UUID, seenAt time.Time, ip, userAgent string) error {
	err := r.db.Ent().Device.UpdateOneID(deviceID).
		SetLastSeenAt(seenAt).
		SetUserAgent(userAgent).
		// No ::inet cast, and that is a change from the GORM version rather than an
		// omission. The cast was there because GORM's map-based Updates sent the
		// parameter in a form Postgres would not coerce, and the comment said it broke
		// on the first non-loopback address. Through ent and pgx it coerces:
		// TestTouchDeviceWritesInet writes 192.0.2.10 and 2001:db8::1 and passes
		// without it, which is why the cast is gone instead of carried forward out of
		// superstition.
		SetLastIP(ip).
		Exec(ctx)
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("store: touch device: %w", err)
	}

	// A device that vanished between being read and being touched is not worth an
	// error: the request it belonged to is already failing on the next check.
	return nil
}

// TrustDevice and RevokeDevice both load the row FOR UPDATE and then apply the rules
// from models.Device, so "a revoked device cannot be trusted" and "revoking clears
// trust" have one definition rather than one in the model and a second written out in
// SQL here.
func (r *User) TrustDevice(ctx context.Context, deviceID uuid.UUID) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		row, err := lockDevice(ctx, tx, device.ID(deviceID))
		if err != nil {
			return err
		}

		d := deviceModel(row)
		if err := d.Trust(); err != nil {
			return err
		}

		return tx.Device.UpdateOneID(row.ID).SetNillableTrustedAt(d.TrustedAt).Exec(ctx)
	})

	return translateDeviceError("trust device", err)
}

func (r *User) RevokeDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		row, err := lockDevice(ctx, tx, device.ID(deviceID), device.UserID(userID))
		if err != nil {
			return err
		}

		d := deviceModel(row)
		if err := d.Revoke(); err != nil {
			return err
		}

		update := tx.Device.UpdateOneID(row.ID).SetNillableRevokedAt(d.RevokedAt)

		// Revoking clears trust, and clearing it means writing NULL — which a
		// SetNillable of a nil pointer does not do.
		if d.TrustedAt == nil {
			update = update.ClearTrustedAt()
		} else {
			update = update.SetTrustedAt(*d.TrustedAt)
		}

		return update.Exec(ctx)
	})

	// Revoking twice is not a failure. The caller asked for the device to be blocked
	// and it is blocked; answering 409 would only make clients write retry logic around
	// a state they already have.
	if errors.Is(err, models.ErrDeviceRevoked) {
		return nil
	}

	return translateDeviceError("revoke device", err)
}

func (r *User) Devices(ctx context.Context, userID uuid.UUID) ([]models.Device, error) {
	// NULLS LAST so a device that has never been seen sorts after ones that have,
	// rather than to the top where Postgres puts NULL by default on DESC. ent's Desc
	// helper does not carry the null ordering, so the term is written out.
	rows, err := r.db.Ent().Device.Query().
		Where(device.UserID(userID)).
		Order(func(s *entsql.Selector) {
			s.OrderExpr(entsql.Expr(s.C(device.FieldLastSeenAt) + " DESC NULLS LAST"))
		}).
		Order(ent.Desc(device.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: devices: %w", err)
	}

	out := make([]models.Device, 0, len(rows))
	for _, row := range rows {
		out = append(out, *deviceModel(row))
	}

	return out, nil
}

func (r *User) RecordLoginEvent(ctx context.Context, event *models.LoginEvent) error {
	created, err := r.db.Ent().LoginEvent.Create().
		SetUserID(event.UserID).
		SetNillableDeviceID(event.DeviceID).
		SetIP(event.IP).
		SetOutcome(loginevent.Outcome(event.Outcome)).
		SetUserAgent(event.UserAgent).
		SetCountry(event.Country).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: record login event: %w", err)
	}

	event.ID = created.ID
	event.CreatedAt = created.CreatedAt

	return nil
}

func (r *User) LoginEvents(ctx context.Context, userID uuid.UUID, limit int) ([]models.LoginEvent, error) {
	// user_id then created_at is the column order of idx_login_user_time, so this reads
	// the index rather than sorting the user's whole history.
	rows, err := r.db.Ent().LoginEvent.Query().
		Where(loginevent.UserID(userID)).
		Order(ent.Desc(loginevent.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: login events: %w", err)
	}

	out := make([]models.LoginEvent, 0, len(rows))
	for _, row := range rows {
		event := models.LoginEvent{
			UserID:    row.UserID,
			DeviceID:  row.DeviceID,
			IP:        row.IP,
			UserAgent: row.UserAgent,
			Outcome:   models.LoginOutcome(row.Outcome),
			Country:   row.Country,
		}
		event.ID = row.ID
		event.CreatedAt = row.CreatedAt
		event.UpdatedAt = row.UpdatedAt

		out = append(out, event)
	}

	return out, nil
}

func (r *User) SetTwoFactorEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error {
	_, err := r.db.Ent().User.Update().
		Where(userIDIs(userID)).
		SetTwoFactorEnabled(enabled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: set two factor: %w", err)
	}

	return nil
}

func (r *User) ReplaceTwoFactorChallenge(ctx context.Context, challenge *models.TwoFactorChallenge) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		_, err := tx.TwoFactorChallenge.Delete().
			Where(
				twofactorchallenge.UserID(challenge.UserID),
				twofactorchallenge.ConsumedAtIsNil(),
			).Exec(ctx)
		if err != nil {
			return err
		}

		created, err := tx.TwoFactorChallenge.Create().
			SetUserID(challenge.UserID).
			SetDeviceID(challenge.DeviceID).
			SetCodeHash(challenge.CodeHash).
			SetExpiresAt(challenge.ExpiresAt).
			SetAttempts(challenge.Attempts).
			Save(ctx)
		if err != nil {
			return err
		}

		challenge.ID = created.ID
		challenge.CreatedAt = created.CreatedAt
		challenge.UpdatedAt = created.UpdatedAt

		return nil
	})
	if err != nil {
		return fmt.Errorf("store: replace two factor challenge: %w", err)
	}

	return nil
}

func (r *User) ActiveTwoFactorChallenge(
	ctx context.Context,
	userID uuid.UUID,
	now time.Time,
) (*models.TwoFactorChallenge, error) {
	row, err := r.db.Ent().TwoFactorChallenge.Query().
		Where(
			twofactorchallenge.UserID(userID),
			twofactorchallenge.ConsumedAtIsNil(),
			twofactorchallenge.ExpiresAtGT(now),
		).
		Order(ent.Desc(twofactorchallenge.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: active two factor challenge: %w", err)
	}

	challenge := &models.TwoFactorChallenge{
		UserID:     row.UserID,
		DeviceID:   row.DeviceID,
		CodeHash:   row.CodeHash,
		ExpiresAt:  row.ExpiresAt,
		Attempts:   row.Attempts,
		ConsumedAt: row.ConsumedAt,
	}
	challenge.ID = row.ID
	challenge.CreatedAt = row.CreatedAt
	challenge.UpdatedAt = row.UpdatedAt

	return challenge, nil
}

// FailTwoFactorChallenge is FailEmailChange for the sign-in code; see the comment there
// for why the counter cannot be read, incremented and written.
func (r *User) FailTwoFactorChallenge(ctx context.Context, challengeID uuid.UUID, maxAttempts int, now time.Time) error {
	err := r.db.Ent().TwoFactorChallenge.Update().
		Where(
			twofactorchallenge.ID(challengeID),
			twofactorchallenge.ConsumedAtIsNil(),
		).
		Modify(func(u *entsql.UpdateBuilder) {
			u.Set(twofactorchallenge.FieldAttempts, entsql.ExprFunc(func(b *entsql.Builder) {
				b.Ident(twofactorchallenge.FieldAttempts).WriteString(" + 1")
			}))

			u.Set(twofactorchallenge.FieldConsumedAt, entsql.ExprFunc(func(b *entsql.Builder) {
				b.WriteString("CASE WHEN ").
					Ident(twofactorchallenge.FieldAttempts).
					WriteString(" + 1 >= ").Arg(maxAttempts).
					WriteString(" THEN ").Arg(now).
					WriteString("::timestamptz ELSE ").
					Ident(twofactorchallenge.FieldConsumedAt).
					WriteString(" END")
			}))
		}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: fail two factor challenge: %w", err)
	}

	return nil
}

func (r *User) ConsumeTwoFactorChallenge(ctx context.Context, challengeID, deviceID uuid.UUID, at time.Time) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// Spending the code and trusting the device have to land together. A crash
		// between them would burn the code and leave the device asking for a new one on
		// every sign-in.
		spent, err := tx.TwoFactorChallenge.Update().
			Where(
				twofactorchallenge.ID(challengeID),
				twofactorchallenge.ConsumedAtIsNil(),
			).
			SetConsumedAt(at).
			Save(ctx)
		if err != nil {
			return err
		}

		// Zero rows means a concurrent request spent the same code first. Trusting the
		// device anyway would let one code be redeemed twice.
		if spent == 0 {
			return user.ErrInvalidTwoFactorCode
		}

		row, err := lockDevice(ctx, tx, device.ID(deviceID))
		if err != nil {
			return err
		}

		d := deviceModel(row)
		if err := d.Trust(); err != nil {
			return err
		}

		return tx.Device.UpdateOneID(row.ID).SetNillableTrustedAt(d.TrustedAt).Exec(ctx)
	})
	if err != nil {
		return translateDeviceError("consume two factor challenge", err)
	}

	return nil
}

// lockDevice reads a device FOR UPDATE so the rules applied to it afterwards cannot race
// a concurrent trust or revoke.
func lockDevice(ctx context.Context, tx *ent.Tx, predicates ...predicate.Device) (*ent.Device, error) {
	return tx.Device.Query().Where(predicates...).ForUpdate().Only(ctx)
}

// deviceModel maps the entity onto the struct the domain reads — and, more to the point,
// onto the type that owns the rules: Trust and Revoke are methods on models.Device, so
// the repository applies them rather than reimplementing them in SQL.
func deviceModel(row *ent.Device) *models.Device {
	out := &models.Device{
		UserID:      row.UserID,
		Fingerprint: row.Fingerprint,
		Label:       row.Label,
		UserAgent:   row.UserAgent,
		LastSeenAt:  row.LastSeenAt,
		LastIP:      row.LastIP,
		TrustedAt:   row.TrustedAt,
		RevokedAt:   row.RevokedAt,
	}

	out.ID = row.ID
	out.CreatedAt = row.CreatedAt
	out.UpdatedAt = row.UpdatedAt

	return out
}

// translateDeviceError turns the model's and ent's vocabulary into the domain's. This is
// the boundary the whole error chain depends on: nothing above internal/store is allowed
// to see ent's not-found or models.ErrDeviceRevoked.
func translateDeviceError(op string, err error) error {
	switch {
	case err == nil:
		return nil

	case isNotFound(err):
		return user.ErrNotFound

	case errors.Is(err, models.ErrDeviceRevoked):
		return user.ErrDeviceRevoked

	case errors.Is(err, user.ErrInvalidTwoFactorCode):
		return err

	default:
		return fmt.Errorf("store: %s: %w", op, err)
	}
}
