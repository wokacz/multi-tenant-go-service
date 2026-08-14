package user

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/go-example/internal/store/models"
)

// MinPasswordLength is enforced here rather than only at the API boundary, so a
// second caller — a CLI, a seeder — cannot create a weaker account than the
// HTTP layer allows.
const MinPasswordLength = 12

// ResetCodeLength is the number of decimal digits emailed (or logged) to the
// user. Six is short enough to type and, with the per-code attempt cap and the
// rate limiter, expensive enough to guess.
const ResetCodeLength = 6

const (
	resetTTL         = 15 * time.Minute
	resetMaxAttempts = 5
)

// Service carries the user rules that are not invariants of the model itself.
// Anything that must hold for every User regardless of who is asking belongs on
// models.User; anything that needs the database to decide belongs here.
type Service struct {
	repo      Repository
	cost      int
	pepper    []byte
	hashes    chan struct{}
	dummyHash []byte
}

func NewService(repo Repository, pepper []byte) *Service {
	return newService(repo, bcrypt.DefaultCost, pepper)
}

func newService(repo Repository, cost int, pepper []byte) *Service {
	if len(pepper) < 32 {
		// Same floor as AUTH_TOKEN_SECRET: a short pepper makes HMAC of a
		// six-digit code cheaper to brute-force offline.
		panic("user: reset-code pepper must be at least 32 bytes")
	}

	dummy, err := bcrypt.GenerateFromPassword([]byte("timing-orphan"), cost)
	if err != nil {
		// MinCost and DefaultCost never fail on this input; a panic here means
		// the cost itself is unusable and the process cannot authenticate.
		panic("user: dummy bcrypt hash: " + err.Error())
	}

	copied := make([]byte, len(pepper))
	copy(copied, pepper)

	return &Service{
		repo:      repo,
		cost:      cost,
		pepper:    copied,
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

func requireMatchingPassword(password, confirm string) error {
	if password != confirm {
		return ErrPasswordMismatch
	}

	if len(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}

	return nil
}

func (s *Service) hashedPassword(password string) (string, error) {
	hash, err := s.hashPassword([]byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return "", ErrPasswordTooLong
		}

		return "", fmt.Errorf("user: hash password: %w", err)
	}

	return string(hash), nil
}

// Create registers a user and returns them with the generated id filled in.
func (s *Service) Create(ctx context.Context, name, email, password, confirm string) (*models.User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNameEmpty
	}

	if utf8.RuneCountInString(name) > MaxNameLength {
		return nil, ErrNameTooLong
	}

	if err := requireMatchingPassword(password, confirm); err != nil {
		return nil, err
	}

	hash, err := s.hashedPassword(password)
	if err != nil {
		return nil, err
	}

	u := &models.User{
		Name:         name,
		Email:        NormalizeEmail(email),
		PasswordHash: hash,
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

// BeginPasswordReset issues a fresh code for a registered address. The
// plaintext is returned so the caller can deliver it — this package does not
// send mail. An unknown address returns an empty code and a nil error: the
// HTTP layer answers the same way either way, and nothing is logged here that
// would tell an operator which it was.
func (s *Service) BeginPasswordReset(ctx context.Context, email string) (string, error) {
	u, err := s.repo.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}

		return "", err
	}

	code, err := randomDigits(ResetCodeLength)
	if err != nil {
		return "", fmt.Errorf("user: generate reset code: %w", err)
	}

	now := time.Now().UTC()
	reset := &models.PasswordReset{
		UserID:    u.ID,
		CodeHash:  s.hashResetCode(u.ID, code),
		ExpiresAt: now.Add(resetTTL),
	}

	if err := s.repo.ReplacePasswordReset(ctx, reset); err != nil {
		return "", err
	}

	return code, nil
}

// CompletePasswordReset sets a new password when the code is valid. Unknown
// addresses, expired codes, wrong codes and spent codes share ErrInvalidResetCode.
func (s *Service) CompletePasswordReset(ctx context.Context, email, code, password, confirm string) error {
	if err := requireMatchingPassword(password, confirm); err != nil {
		return err
	}

	u, err := s.repo.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.hashResetCode(uuid.Nil, code)

			return ErrInvalidResetCode
		}

		return err
	}

	now := time.Now().UTC()
	reset, err := s.repo.ActivePasswordReset(ctx, u.ID, now)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.hashResetCode(u.ID, code)

			return ErrInvalidResetCode
		}

		return err
	}

	if !hmac.Equal([]byte(reset.CodeHash), []byte(s.hashResetCode(u.ID, code))) {
		reset.Attempts++
		if reset.Attempts >= resetMaxAttempts {
			consumed := now
			reset.ConsumedAt = &consumed
		}

		if saveErr := s.repo.SavePasswordReset(ctx, reset); saveErr != nil {
			return saveErr
		}

		return ErrInvalidResetCode
	}

	hash, err := s.hashedPassword(password)
	if err != nil {
		return err
	}

	consumed := now
	reset.ConsumedAt = &consumed

	return s.repo.ConsumePasswordReset(ctx, reset, hash)
}

func (s *Service) hashResetCode(userID uuid.UUID, code string) string {
	mac := hmac.New(sha256.New, s.pepper)
	_, _ = mac.Write(userID[:])
	_, _ = mac.Write([]byte(code))

	return hex.EncodeToString(mac.Sum(nil))
}

func randomDigits(n int) (string, error) {
	const digits = "0123456789"

	out := make([]byte, n)
	for i := range out {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}

		out[i] = digits[k.Int64()]
	}

	return string(out), nil
}

// NormalizeEmail lowercases and trims the address. The unique index is
// case-sensitive, so without this "A@example.com" and "a@example.com" would be
// two separate accounts that no user would ever tell apart.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
