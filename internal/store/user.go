package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wokacz/go-example/internal/store/models"
	"github.com/wokacz/go-example/internal/user"
)

// UserRepository implements user.Repository. The interface it satisfies is
// declared in internal/user, so the dependency points inwards: the domain does
// not know this type exists.
type UserRepository struct {
	db *DB
}

func NewUserRepository(db *DB) *UserRepository {
	return &UserRepository{db: db}
}

// Compile-time check that this still satisfies the interface the domain
// declares. Without it, a drifting signature would only surface at the call
// site in main, far from either definition.
var _ user.Repository = (*UserRepository)(nil)

func (r *UserRepository) Create(ctx context.Context, u *models.User) error {
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

func (r *UserRepository) ByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
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
