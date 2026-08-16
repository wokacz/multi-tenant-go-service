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

// MaxUserPage caps the installation-wide account listing. It is the same shape
// as MaxLoginEvents: the service clamps rather than trusting the caller, so a
// client asking for everything gets a page instead of the whole table.
const MaxUserPage = 100

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

// Option adjusts how a Service is built.
type Option func(*options)

type options struct {
	cost int
}

// WithBcryptCost overrides the hashing cost.
//
// Production should leave it alone: the default is what makes a stolen hash
// expensive. It exists because the cost is the one parameter that legitimately
// has to move — upwards as hardware gets faster, and downwards in tests, which
// would otherwise spend most of their time deriving keys nobody checks.
func WithBcryptCost(cost int) Option {
	return func(o *options) { o.cost = cost }
}

func NewService(repo Repository, pepper []byte, opts ...Option) *Service {
	resolved := options{cost: bcrypt.DefaultCost}
	for _, opt := range opts {
		opt(&resolved)
	}

	return newService(repo, resolved.cost, pepper)
}

func newService(repo Repository, cost int, pepper []byte) *Service {
	if len(pepper) < 32 {
		// Same floor as AUTH_RESET_SECRET: a short pepper makes HMAC of a
		// six-digit code cheaper to brute-force offline. This is not the
		// JWT signing secret — rotating session tokens must not rewrite
		// hashes of codes already sitting in someone's inbox.
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

// acquire takes one of the bcrypt slots, or gives up when the caller's context
// ends.
//
// The select on ctx.Done() is the point. bcrypt is slow on purpose and the
// semaphore is only two wide, so a burst queues; a plain channel send would
// keep every queued request parked even after its client hung up and its
// connection was closed, and those goroutines would still claim a slot when
// their turn finally came. The queue then grows faster than it drains.
func (s *Service) acquire(ctx context.Context) error {
	// Checked before the select, not only inside it. A select whose cases are
	// both ready picks at random, so an already-cancelled caller would still
	// start a hash about half the time — and the behaviour would be
	// unreproducible, which is worse than either outcome.
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case s.hashes <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) hashPassword(ctx context.Context, password []byte) ([]byte, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.hashes }()

	return bcrypt.GenerateFromPassword(password, s.cost)
}

// compareHash returns a non-nil error both for a wrong password and for a
// cancelled context. Callers that turn a mismatch into ErrInvalidCredentials
// must check ctx.Err() first — reporting "wrong password" to a client that
// merely disconnected would be a lie, and on the sign-in path it would also
// record a bad-password login event that never happened.
func (s *Service) compareHash(ctx context.Context, hash, password []byte) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
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

func (s *Service) hashedPassword(ctx context.Context, password string) (string, error) {
	hash, err := s.hashPassword(ctx, []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return "", ErrPasswordTooLong
		}

		return "", fmt.Errorf("user: hash password: %w", err)
	}

	return string(hash), nil
}

// Create registers a user and returns them with the generated id filled in.
//
// The locale is the language the account was created in. It is captured here
// rather than asked for, because the signup request already says it in
// Accept-Language, and a preference nobody was prompted for is one that is
// right far more often than a default. It is what mail is written in, which is
// the case no request header can answer.
func (s *Service) Create(ctx context.Context, name, email, password, confirm, locale string) (*models.User, error) {
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

	hash, err := s.hashedPassword(ctx, password)
	if err != nil {
		return nil, err
	}

	u := &models.User{
		Name:         name,
		Email:        NormalizeEmail(email),
		PasswordHash: hash,
		Locale:       locale,
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

// ByEmail resolves an address to an account, or ErrNotFound.
//
// Unlike Authenticate it does not run bcrypt on a miss, because it is not an
// authentication path: nothing here compares a secret, so there is no timing to
// equalise. It is also not reachable anonymously — the only caller is adding
// somebody to an organization, which needs members.invite — so it is not an
// oracle for whether an address is registered.
func (s *Service) ByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.repo.ByEmail(ctx, NormalizeEmail(email))
}

// All lists accounts for the installation-wide administration screens.
func (s *Service) All(ctx context.Context, limit, offset int) ([]models.User, error) {
	if limit <= 0 || limit > MaxUserPage {
		limit = MaxUserPage
	}

	if offset < 0 {
		offset = 0
	}

	return s.repo.All(ctx, limit, offset)
}

// Suspend blocks or unblocks an account.
func (s *Service) Suspend(ctx context.Context, userID uuid.UUID, suspended bool) error {
	var at *time.Time

	if suspended {
		now := time.Now().UTC()
		at = &now
	}

	return s.repo.SetSuspended(ctx, userID, at)
}

// Delete soft deletes an account.
func (s *Service) Delete(ctx context.Context, userID uuid.UUID) error {
	return s.repo.Delete(ctx, userID)
}

// Authenticate checks email and password. Missing users and wrong passwords
// both become ErrInvalidCredentials, and both paths run bcrypt, so neither
// the error nor the timing discloses whether the address is registered.
//
// It is the password half of sign-in only. SignIn wraps it with the device and
// second-factor rules; this stays exported because a non-HTTP caller — a CLI,
// a seeder — still needs a way to check a password without minting devices.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*models.User, error) {
	u, err := s.repo.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		_ = s.compareHash(ctx, s.dummyHash, []byte(password))

		return nil, ErrInvalidCredentials
	}

	if err := s.compareHash(ctx, []byte(u.PasswordHash), []byte(password)); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, ErrInvalidCredentials
	}

	// Checked after the password, not before: reporting "suspended" to somebody
	// who did not prove they own the account would turn this into an oracle for
	// which addresses are registered and blocked.
	if u.IsSuspended() {
		return nil, ErrSuspended
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
		// The counter moves in the store, in one statement. Incrementing the
		// loaded row here and writing it back would let concurrent guesses all
		// read the same value and store the same value — the cap that makes a
		// six-digit code safe would quietly stop counting, and a late writer
		// could even restore a consumed_at that another request had just set.
		if failErr := s.repo.FailPasswordReset(ctx, reset.ID, resetMaxAttempts, now); failErr != nil {
			return failErr
		}

		return ErrInvalidResetCode
	}

	hash, err := s.hashedPassword(ctx, password)
	if err != nil {
		return err
	}

	consumed := now
	reset.ConsumedAt = &consumed

	return s.repo.ConsumePasswordReset(ctx, reset, hash)
}

// Code purposes keep the two six-digit codes in this package from being
// interchangeable. Without the prefix, HMAC(pepper, userID||code) is the same
// value whether the code was emailed to reset a password or to finish a
// sign-in, and one could be spent as the other.
const (
	purposeReset     = "password-reset"
	purposeTwoFactor = "two-factor"
)

func (s *Service) hashResetCode(userID uuid.UUID, code string) string {
	return s.hashCode(purposeReset, userID, code)
}

func (s *Service) hashCode(purpose string, id uuid.UUID, code string) string {
	mac := hmac.New(sha256.New, s.pepper)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write(id[:])
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
