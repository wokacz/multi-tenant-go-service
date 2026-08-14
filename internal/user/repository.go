// Package user holds the user-facing domain logic. It knows nothing about HTTP
// or SQL: the transport lives in internal/api, and the persistence in
// internal/store.
package user

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/store/models"
)

var (
	ErrNotFound = errors.New("user: not found")
	// ErrEmailTaken is reported by the repository from the unique constraint
	// rather than by a lookup here — see Service.Create.
	ErrEmailTaken       = errors.New("user: email already registered")
	ErrPasswordTooLong  = errors.New("user: password is too long")
	ErrPasswordTooShort = errors.New("user: password is too short")
)

// Repository is the persistence this package needs, declared here rather than
// in internal/store on purpose: the consumer owns the interface, so the store
// depends on the domain and not the other way round. It also keeps the
// interface honest — it lists what this package actually uses, not everything
// the store happens to be able to do.
//
// The store implements it. Nothing here knows that GORM exists.
type Repository interface {
	// Create persists u and fills in its generated fields. It returns
	// ErrEmailTaken when the address is already registered.
	Create(ctx context.Context, u *models.User) error

	// ByID returns ErrNotFound when no live user has that id.
	ByID(ctx context.Context, id uuid.UUID) (*models.User, error)
}
