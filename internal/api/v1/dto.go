package v1

import (
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/store/models"
	"github.com/wokacz/go-example/internal/user"
)

// minPasswordLength mirrors the minLength tag on CreateUserRequest.Password. A
// struct tag cannot reference a constant, so the two are tied together by the
// compile-time assertion below instead.
const minPasswordLength = 12

// If the domain rule and the documented schema ever disagree, one of these
// subtractions underflows and the package stops compiling. The alternative is a
// spec that advertises a limit the service does not enforce.
const (
	_ = uint(user.MinPasswordLength - minPasswordLength)
	_ = uint(minPasswordLength - user.MinPasswordLength)
)

// UserResponse is the wire representation of a user.
//
// It exists so that models.User never reaches JSON. The model carries
// PasswordHash, IsProtected and DeletedAt; encoding it directly would leak the
// first and expose internal lifecycle state in the other two. More importantly
// it makes the API contract explicit — adding a column to the model cannot
// silently widen the response.
type UserResponse struct {
	ID        uuid.UUID `json:"id" format:"uuid" doc:"Unique identifier"`
	Name      string    `json:"name" doc:"Display name"`
	Email     string    `json:"email" format:"email" doc:"Email address, normalised to lower case"`
	CreatedAt time.Time `json:"created_at" doc:"When the account was registered"`
}

// newUserResponse is the only place a model becomes a DTO. Keeping the
// conversion in one function means a new model field is invisible to clients
// until someone deliberately adds it here.
func newUserResponse(u *models.User) UserResponse {
	return UserResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
	}
}

// CreateUserRequest is the body of POST /v1/users. The constraints here are
// documented in the OpenAPI schema and rejected by huma before the handler
// runs; the domain enforces its own copy so a non-HTTP caller cannot bypass it.
type CreateUserRequest struct {
	Name  string `json:"name" minLength:"1" maxLength:"100" doc:"Display name"`
	Email string `json:"email" format:"email" maxLength:"255" doc:"Email address"`
	// maxLength counts characters while bcrypt's limit is 72 *bytes*, so a
	// multi-byte password can satisfy this and still be rejected by the domain.
	// That path returns 422 rather than a 500.
	Password string `json:"password" minLength:"12" maxLength:"72" doc:"Plain-text password, hashed before storage"`
}
