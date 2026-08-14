package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/go-example/internal/store/models"
)

var testPepper = []byte("0123456789abcdef0123456789abcdef")

type memRepo struct {
	byID    map[uuid.UUID]*models.User
	byEmail map[string]*models.User
	resets  map[uuid.UUID]*models.PasswordReset
}

func newMemRepo() *memRepo {
	return &memRepo{
		byID:    map[uuid.UUID]*models.User{},
		byEmail: map[string]*models.User{},
		resets:  map[uuid.UUID]*models.PasswordReset{},
	}
}

func (m *memRepo) Create(_ context.Context, u *models.User) error {
	if _, ok := m.byEmail[u.Email]; ok {
		return ErrEmailTaken
	}

	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}

	m.byID[u.ID] = u
	m.byEmail[u.Email] = u

	return nil
}

func (m *memRepo) ByID(_ context.Context, id uuid.UUID) (*models.User, error) {
	u, ok := m.byID[id]
	if !ok {
		return nil, ErrNotFound
	}

	return u, nil
}

func (m *memRepo) ByEmail(_ context.Context, email string) (*models.User, error) {
	u, ok := m.byEmail[email]
	if !ok {
		return nil, ErrNotFound
	}

	return u, nil
}

func (m *memRepo) ReplacePasswordReset(_ context.Context, reset *models.PasswordReset) error {
	if reset.ID == uuid.Nil {
		reset.ID = uuid.Must(uuid.NewV7())
	}

	m.resets[reset.UserID] = reset

	return nil
}

func (m *memRepo) ActivePasswordReset(_ context.Context, userID uuid.UUID, now time.Time) (*models.PasswordReset, error) {
	reset, ok := m.resets[userID]
	if !ok || reset.ConsumedAt != nil || !reset.ExpiresAt.After(now) {
		return nil, ErrNotFound
	}

	return reset, nil
}

func (m *memRepo) SavePasswordReset(_ context.Context, reset *models.PasswordReset) error {
	m.resets[reset.UserID] = reset

	return nil
}

func (m *memRepo) ConsumePasswordReset(_ context.Context, reset *models.PasswordReset, passwordHash string) error {
	u, ok := m.byID[reset.UserID]
	if !ok {
		return ErrNotFound
	}

	u.PasswordHash = passwordHash
	m.resets[reset.UserID] = reset

	return nil
}

func testService(t *testing.T) (*Service, *memRepo) {
	t.Helper()

	repo := newMemRepo()

	return newService(repo, bcrypt.MinCost, testPepper), repo
}

func TestCreateTrimsAndRejectsBlankName(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "   ", "a@example.com", "twelve-chars", "twelve-chars"); !errors.Is(err, ErrNameEmpty) {
		t.Fatalf("Create(blank name) = %v, want ErrNameEmpty", err)
	}

	u, err := s.Create(ctx, "  Ada  ", "a@example.com", "twelve-chars", "twelve-chars")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if u.Name != "Ada" {
		t.Errorf("Name = %q, want Ada", u.Name)
	}
}

func TestCreateRejectsPasswordMismatch(t *testing.T) {
	s, _ := testService(t)

	if _, err := s.Create(context.Background(), "Ada", "a@example.com", "twelve-chars", "twelve-charZ"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("Create(mismatch) = %v, want ErrPasswordMismatch", err)
	}
}

func TestCreateRejectsLongName(t *testing.T) {
	s, _ := testService(t)
	name := strings.Repeat("n", MaxNameLength+1)

	if _, err := s.Create(context.Background(), name, "a@example.com", "twelve-chars", "twelve-chars"); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("Create(long name) = %v, want ErrNameTooLong", err)
	}
}

func TestAuthenticateWrongPasswordAndUnknownEmail(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars"); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if _, err := s.Authenticate(ctx, "ada@example.com", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password = %v, want ErrInvalidCredentials", err)
	}

	if _, err := s.Authenticate(ctx, "missing@example.com", "twelve-chars"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown email = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateSuccessNormalisesEmail(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "Ada", "Ada@Example.com", "twelve-chars", "twelve-chars")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := s.Authenticate(ctx, "ADA@example.com", "twelve-chars")
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}

	if got.ID != created.ID {
		t.Errorf("ID = %s, want %s", got.ID, created.ID)
	}
}

func TestPasswordResetUnknownEmailIsSilent(t *testing.T) {
	s, _ := testService(t)

	code, err := s.BeginPasswordReset(context.Background(), "missing@example.com")
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if code != "" {
		t.Fatalf("code = %q, want empty for an unknown address", code)
	}
}

func TestPasswordResetRoundTrip(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars"); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	code, err := s.BeginPasswordReset(ctx, "ada@example.com")
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if len(code) != ResetCodeLength {
		t.Fatalf("code length = %d, want %d", len(code), ResetCodeLength)
	}

	if err := s.CompletePasswordReset(ctx, "ada@example.com", code, "another-passw", "another-passw"); err != nil {
		t.Fatalf("CompletePasswordReset() = %v", err)
	}

	if _, err := s.Authenticate(ctx, "ada@example.com", "twelve-chars"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}

	if _, err := s.Authenticate(ctx, "ada@example.com", "another-passw"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestPasswordResetRejectsWrongCode(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars"); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if _, err := s.BeginPasswordReset(ctx, "ada@example.com"); err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if err := s.CompletePasswordReset(ctx, "ada@example.com", "000000", "another-passw", "another-passw"); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("CompletePasswordReset(wrong code) = %v, want ErrInvalidResetCode", err)
	}
}

func TestPasswordResetRequiresConfirmation(t *testing.T) {
	s, _ := testService(t)

	if err := s.CompletePasswordReset(context.Background(), "ada@example.com", "123456", "another-passw", "nope-nope-no"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("CompletePasswordReset(mismatch) = %v, want ErrPasswordMismatch", err)
	}
}
