package user

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/go-example/internal/store/models"
)

// MinPasswordLength is enforced here rather than only at the API boundary, so a
// second caller — a CLI, a seeder — cannot create a weaker account than the
// HTTP layer allows.
const MinPasswordLength = 12

// Service carries the user rules that are not invariants of the model itself.
// Anything that must hold for every User regardless of who is asking belongs on
// models.User; anything that needs the database to decide belongs here.
type Service struct {
	repo Repository
	cost int
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, cost: bcrypt.DefaultCost}
}

// Create registers a user and returns them with the generated id filled in.
func (s *Service) Create(ctx context.Context, name, email, password string) (*models.User, error) {
	if len(password) < MinPasswordLength {
		return nil, ErrPasswordTooShort
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		// bcrypt refuses anything over 72 bytes. Older versions truncated
		// silently instead, which quietly made long passwords weaker, so this
		// is surfaced rather than swallowed.
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return nil, ErrPasswordTooLong
		}

		return nil, fmt.Errorf("user: hash password: %w", err)
	}

	u := &models.User{
		Name:         strings.TrimSpace(name),
		Email:        NormalizeEmail(email),
		PasswordHash: string(hash),
	}

	// No "does this email exist yet" query first. Two concurrent signups would
	// both pass that check and one would still fail on insert, so the unique
	// index is the only thing that can actually decide — the repository turns
	// its violation into ErrEmailTaken.
	if err := s.repo.Create(ctx, u); err != nil {
		return nil, err
	}

	return u, nil
}

func (s *Service) ByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	return s.repo.ByID(ctx, id)
}

// NormalizeEmail lowercases and trims the address. The unique index is
// case-sensitive, so without this "A@example.com" and "a@example.com" would be
// two separate accounts that no user would ever tell apart.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
