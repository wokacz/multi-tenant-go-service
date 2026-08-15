package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store"
	"github.com/wokacz/go-example/internal/store/models"
)

// User implements user.Repository. The interface it satisfies is declared in
// internal/domain/user, so the dependency points inwards: the domain does not
// know this type exists.
type User struct {
	db *store.DB
}

func NewUser(db *store.DB) *User {
	return &User{db: db}
}

// Compile-time check that this still satisfies the interface the domain
// declares. Without it, a drifting signature would only surface at the call
// site in main, far from either definition.
var _ user.Repository = (*User)(nil)

func (r *User) Create(ctx context.Context, u *models.User) error {
	err := r.db.WithContext(ctx).Create(u).Error
	if err == nil {
		return nil
	}

	// GORM errors stop here. Translating them into domain errors is what keeps
	// gorm out of internal/api — the transport maps user.ErrEmailTaken onto a
	// 409 without ever knowing which database produced it.
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return user.ErrEmailTaken
	}

	return fmt.Errorf("store: create user: %w", err)
}

func (r *User) ByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var u models.User

	// GORM applies the soft-delete scope on its own, so rows with deleted_at
	// set are already excluded here.
	err := r.db.WithContext(ctx).First(&u, "id = ?", id).Error
	if err == nil {
		return &u, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: user by id: %w", err)
}

func (r *User) ByEmail(ctx context.Context, email string) (*models.User, error) {
	var u models.User

	err := r.db.WithContext(ctx).First(&u, "email = ?", email).Error
	if err == nil {
		return &u, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: user by email: %w", err)
}

func (r *User) ReplacePasswordReset(ctx context.Context, reset *models.PasswordReset) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND consumed_at IS NULL", reset.UserID).
			Delete(&models.PasswordReset{}).Error; err != nil {
			return err
		}

		return tx.Create(reset).Error
	})
	if err != nil {
		return fmt.Errorf("store: replace password reset: %w", err)
	}

	return nil
}

func (r *User) ActivePasswordReset(ctx context.Context, userID uuid.UUID, now time.Time) (*models.PasswordReset, error) {
	var reset models.PasswordReset

	err := r.db.WithContext(ctx).
		Where("user_id = ? AND consumed_at IS NULL AND expires_at > ?", userID, now).
		Order("created_at DESC").
		First(&reset).Error
	if err == nil {
		return &reset, nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, user.ErrNotFound
	}

	return nil, fmt.Errorf("store: active password reset: %w", err)
}

// FailPasswordReset moves the attempt counter in a single statement.
//
// Reading the row, adding one and saving it back is the obvious shape and the
// wrong one: two guesses that overlap both read the same count and both write
// the same count, so five concurrent attempts leave the counter at one. Worse,
// a slow writer could put back a consumed_at that another request had just set
// and reopen a code that was already spent. Here the increment and the decision
// to spend the code are one UPDATE, and the WHERE keeps a spent code spent.
func (r *User) FailPasswordReset(ctx context.Context, resetID uuid.UUID, maxAttempts int, now time.Time) error {
	err := r.db.WithContext(ctx).
		Model(&models.PasswordReset{}).
		Where("id = ? AND consumed_at IS NULL", resetID).
		Updates(map[string]any{
			"attempts": gorm.Expr("attempts + 1"),
			// Every SET expression reads the pre-UPDATE row, so this sees the
			// old count and has to add the same one again.
			"consumed_at": gorm.Expr("CASE WHEN attempts + 1 >= ? THEN ?::timestamptz ELSE consumed_at END", maxAttempts, now),
		}).Error
	if err != nil {
		return fmt.Errorf("store: fail password reset: %w", err)
	}

	// No rows means the code was already spent, which is not an error: the
	// caller is about to return ErrInvalidResetCode either way.
	return nil
}

func (r *User) ConsumePasswordReset(ctx context.Context, reset *models.PasswordReset, passwordHash string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).
			Where("id = ?", reset.UserID).
			Updates(map[string]any{
				"password_hash": passwordHash,
				"session_epoch": gorm.Expr("session_epoch + 1"),
			}).Error; err != nil {
			return err
		}

		return tx.Model(reset).Updates(map[string]any{
			"consumed_at": reset.ConsumedAt,
			"attempts":    reset.Attempts,
		}).Error
	})
	if err != nil {
		return fmt.Errorf("store: consume password reset: %w", err)
	}

	return nil
}
