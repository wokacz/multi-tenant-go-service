package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// The email-change code is the password-reset code with a different target
// column, so these four methods deliberately mirror their reset counterparts —
// including the conditional UPDATE that counts a failed attempt. Two one-time-code
// mechanisms that drift apart are two sets of rules to remember.

func (r *User) ReplaceEmailChange(ctx context.Context, change *models.EmailChange) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND consumed_at IS NULL", change.UserID).
			Delete(&models.EmailChange{}).Error; err != nil {
			return err
		}

		return tx.Create(change).Error
	})
	if err != nil {
		return fmt.Errorf("store: replace email change: %w", err)
	}

	return nil
}

func (r *User) ActiveEmailChange(ctx context.Context, userID uuid.UUID, now time.Time) (*models.EmailChange, error) {
	var change models.EmailChange

	err := r.db.WithContext(ctx).
		Where("user_id = ? AND consumed_at IS NULL AND expires_at > ?", userID, now).
		Order("created_at DESC").
		First(&change).Error
	if err == nil {
		return &change, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: active email change: %w", err)
}

func (r *User) FailEmailChange(ctx context.Context, changeID uuid.UUID, maxAttempts int, now time.Time) error {
	err := r.db.WithContext(ctx).
		Model(&models.EmailChange{}).
		Where("id = ? AND consumed_at IS NULL", changeID).
		Updates(map[string]any{
			"attempts": gorm.Expr("attempts + 1"),
			// Every SET expression reads the pre-UPDATE row, so this sees the old
			// count and has to add the same one again.
			"consumed_at": gorm.Expr("CASE WHEN attempts + 1 >= ? THEN ?::timestamptz ELSE consumed_at END", maxAttempts, now),
		}).Error
	if err != nil {
		return fmt.Errorf("store: fail email change: %w", err)
	}

	// No rows means the code was already spent, which is not an error: the caller
	// returns ErrInvalidEmailCode either way.
	return nil
}

func (r *User) ConsumeEmailChange(ctx context.Context, change *models.EmailChange, email string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Session(&gorm.Session{SkipHooks: true}).
			Model(&models.User{}).
			Where("id = ?", change.UserID).
			Update("email", email)
		if res.Error != nil {
			return res.Error
		}

		if res.RowsAffected == 0 {
			return user.ErrNotFound
		}

		return tx.Model(change).Updates(map[string]any{
			"consumed_at": change.ConsumedAt,
			"attempts":    change.Attempts,
		}).Error
	})

	// The unique index on users.email is what decides, not a lookup: between the
	// code being sent and the code coming back, somebody else may have taken the
	// address. A soft-deleted account still occupies it, which is its own problem
	// and tracked separately.
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return user.ErrEmailTaken
	}

	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return err
		}

		return fmt.Errorf("store: consume email change: %w", err)
	}

	return nil
}
