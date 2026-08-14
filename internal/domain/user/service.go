package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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
	repo      Repository
	cost      int
	hashes    chan struct{}
	dummyHash []byte
}

func NewService(repo Repository) *Service {
	return newService(repo, bcrypt.DefaultCost)
}

func newService(repo Repository, cost int) *Service {
	dummy, err := bcrypt.GenerateFromPassword([]byte("timing-orphan"), cost)
	if err != nil {
		// MinCost and DefaultCost never fail on this input; a panic here means
		// the cost itself is unusable and the process cannot authenticate.
		panic("user: dummy bcrypt hash: " + err.Error())
	}

	return &Service{
		repo:      repo,
		cost:      cost,
		hashes:    make(chan struct{}, maxConcurrentHashes),
		dummyHash: dummy,
	}
}

func (s *Service) hashPassword(password []byte) ([]byte, error) {
	s.hashes <- struct{}{}
	defer func() { <-s.hashes }()

	return bcrypt.GenerateFromPassword(password, s.cost)
}

func (s *Service) compareHash(hash, password []byte) error {
	s.hashes <- struct{}{}
	defer func() { <-s.hashes }()

	return bcrypt.CompareHashAndPassword(hash, password)
}

// Create registers a user and returns them with the generated id filled in.
func (s *Service) Create(ctx context.Context, name, email, password string) (*models.User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNameEmpty
	}

	if utf8.RuneCountInString(name) > MaxNameLength {
		return nil, ErrNameTooLong
	}

	if len(password) < MinPasswordLength {
		return nil, ErrPasswordTooShort
	}

	hash, err := s.hashPassword([]byte(password))
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
		Name:         name,
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

// Authenticate checks email and password. Missing users and wrong passwords
// both become ErrInvalidCredentials, and both paths run bcrypt, so neither
// the error nor the timing discloses whether the address is registered.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*models.User, error) {
	u, err := s.repo.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		_ = s.compareHash(s.dummyHash, []byte(password))

		return nil, ErrInvalidCredentials
	}

	if err := s.compareHash([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return u, nil
}

// NormalizeEmail lowercases and trims the address. The unique index is
// case-sensitive, so without this "A@example.com" and "a@example.com" would be
// two separate accounts that no user would ever tell apart.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
