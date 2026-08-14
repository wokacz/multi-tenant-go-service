// Package user holds the user-facing domain logic. It knows nothing about HTTP
// or SQL: the transport lives in internal/api, and the persistence in
// internal/store/repositories.
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
	// rather than by a lookup here — see Service.Create. The HTTP layer
	// treats it as a successful registration so the status cannot be used
	// to probe whether an address is already in the system.
	ErrEmailTaken         = errors.New("user: email already registered")
	ErrPasswordTooLong    = errors.New("user: password is too long")
	ErrPasswordTooShort   = errors.New("user: password is too short")
	ErrNameEmpty          = errors.New("user: name is empty")
	ErrNameTooLong        = errors.New("user: name is too long")
	ErrInvalidCredentials = errors.New("user: invalid credentials")
	ErrUnauthorized       = errors.New("user: unauthorized")
)

// MaxNameLength is enforced here rather than only at the API boundary.
const MaxNameLength = 100

// maxConcurrentHashes caps in-flight bcrypt work. The algorithm is
// deliberately slow; without a cap, a burst of registrations or logins from
// many addresses saturates the process even when each address is rate-limited.
const maxConcurrentHashes = 2

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

	// ByEmail returns ErrNotFound when no live user has that address.
	ByEmail(ctx context.Context, email string) (*models.User, error)
}
