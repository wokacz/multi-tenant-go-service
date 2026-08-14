package user

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/go-example/internal/store/models"
)

type memRepo struct {
	byID    map[uuid.UUID]*models.User
	byEmail map[string]*models.User
}

func newMemRepo() *memRepo {
	return &memRepo{
		byID:    map[uuid.UUID]*models.User{},
		byEmail: map[string]*models.User{},
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

func testService(t *testing.T) (*Service, *memRepo) {
	t.Helper()

	repo := newMemRepo()

	return newService(repo, bcrypt.MinCost), repo
}

func TestCreateTrimsAndRejectsBlankName(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "   ", "a@example.com", "twelve-chars"); !errors.Is(err, ErrNameEmpty) {
		t.Fatalf("Create(blank name) = %v, want ErrNameEmpty", err)
	}

	u, err := s.Create(ctx, "  Ada  ", "a@example.com", "twelve-chars")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if u.Name != "Ada" {
		t.Errorf("Name = %q, want Ada", u.Name)
	}
}

func TestCreateRejectsLongName(t *testing.T) {
	s, _ := testService(t)
	name := strings.Repeat("n", MaxNameLength+1)

	if _, err := s.Create(context.Background(), name, "a@example.com", "twelve-chars"); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("Create(long name) = %v, want ErrNameTooLong", err)
	}
}

func TestAuthenticateWrongPasswordAndUnknownEmail(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars"); err != nil {
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

	created, err := s.Create(ctx, "Ada", "Ada@Example.com", "twelve-chars")
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
