package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/auth"
	"github.com/wokacz/go-example/internal/config"
	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store/models"
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

type stubRepo struct {
	byID    map[uuid.UUID]*models.User
	byEmail map[string]*models.User
}

func (s *stubRepo) Create(_ context.Context, u *models.User) error {
	if _, ok := s.byEmail[u.Email]; ok {
		return user.ErrEmailTaken
	}

	u.ID = uuid.Must(uuid.NewV7())
	s.byID[u.ID] = u
	s.byEmail[u.Email] = u

	return nil
}

func (s *stubRepo) ByID(_ context.Context, id uuid.UUID) (*models.User, error) {
	u, ok := s.byID[id]
	if !ok {
		return nil, user.ErrNotFound
	}

	return u, nil
}

func (s *stubRepo) ByEmail(_ context.Context, email string) (*models.User, error) {
	u, ok := s.byEmail[email]
	if !ok {
		return nil, user.ErrNotFound
	}

	return u, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	repo := &stubRepo{
		byID:    map[uuid.UUID]*models.User{},
		byEmail: map[string]*models.User{},
	}

	cfg := &config.Config{
		Env:               config.EnvDevelopment,
		APIName:           "test",
		APIHost:           "127.0.0.1",
		APIPort:           4000,
		HealthTimeout:     time.Second,
		MaxRequestBytes:   1 << 20,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
	}

	return NewServer(cfg, slog.New(slog.DiscardHandler), Deps{
		DB:     okPinger{},
		Users:  user.NewService(repo),
		Tokens: tokens,
	})
}

func TestGetUserRequiresBearer(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/users/"+uuid.Must(uuid.NewV7()).String(), nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreateUserHidesDuplicateEmail(t *testing.T) {
	s := newTestServer(t)
	body := []byte(`{"name":"Ada","email":"ada@example.com","password":"twelve-chars"}`)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/users", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status = %d, want 204", i+1, rec.Code)
		}
	}
}

func TestSessionThenSelfFetch(t *testing.T) {
	s := newTestServer(t)
	create := httptest.NewRequest(http.MethodPost, "/v1/users", strings.NewReader(
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(created, create)

	if created.Code != http.StatusNoContent {
		t.Fatalf("create status = %d, want 204", created.Code)
	}

	login := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"email":"ada@example.com","password":"twelve-chars"}`))
	login.Header.Set("Content-Type", "application/json")
	logged := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(logged, login)

	if logged.Code != http.StatusCreated {
		t.Fatalf("login status = %d body = %s", logged.Code, logged.Body.Bytes())
	}

	var session struct {
		Token string `json:"token"`
		User  struct {
			ID uuid.UUID `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(logged.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	self := httptest.NewRequest(http.MethodGet, "/v1/users/"+session.User.ID.String(), nil)
	self.Header.Set("Authorization", "Bearer "+session.Token)
	got := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(got, self)

	if got.Code != http.StatusOK {
		t.Fatalf("self fetch status = %d, want 200", got.Code)
	}

	other := httptest.NewRequest(http.MethodGet, "/v1/users/"+uuid.Must(uuid.NewV7()).String(), nil)
	other.Header.Set("Authorization", "Bearer "+session.Token)
	hidden := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(hidden, other)

	if hidden.Code != http.StatusNotFound {
		t.Fatalf("other user status = %d, want 404", hidden.Code)
	}
}

func TestHealthOmitsDependencyName(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options")
	}

	if strings.Contains(rec.Body.String(), "database") {
		t.Fatalf("health body leaked a dependency name: %s", rec.Body.String())
	}
}

func TestProductionHidesOpenAPI(t *testing.T) {
	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	s := NewServer(&config.Config{
		Env:               config.EnvProduction,
		APIName:           "test",
		APIHost:           "127.0.0.1",
		APIPort:           4000,
		HealthTimeout:     time.Second,
		MaxRequestBytes:   1 << 20,
		ReadHeaderTimeout: time.Second,
	}, slog.New(slog.DiscardHandler), Deps{
		DB:     okPinger{},
		Users:  user.NewService(&stubRepo{byID: map[uuid.UUID]*models.User{}, byEmail: map[string]*models.User{}}),
		Tokens: tokens,
	})

	for _, path := range []string{"/docs", "/openapi.json", "/openapi.yaml"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
}
