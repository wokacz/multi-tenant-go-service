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

var testPepper = []byte("0123456789abcdef0123456789abcdef")

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

type capturingMailer struct {
	to, code string
}

func (c *capturingMailer) SendPasswordReset(_ context.Context, to, code string) error {
	c.to, c.code = to, code

	return nil
}

type stubRepo struct {
	byID    map[uuid.UUID]*models.User
	byEmail map[string]*models.User
	resets  map[uuid.UUID]*models.PasswordReset
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		byID:    map[uuid.UUID]*models.User{},
		byEmail: map[string]*models.User{},
		resets:  map[uuid.UUID]*models.PasswordReset{},
	}
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

func (s *stubRepo) ReplacePasswordReset(_ context.Context, reset *models.PasswordReset) error {
	if reset.ID == uuid.Nil {
		reset.ID = uuid.Must(uuid.NewV7())
	}

	s.resets[reset.UserID] = reset

	return nil
}

func (s *stubRepo) ActivePasswordReset(_ context.Context, userID uuid.UUID, now time.Time) (*models.PasswordReset, error) {
	reset, ok := s.resets[userID]
	if !ok || reset.ConsumedAt != nil || !reset.ExpiresAt.After(now) {
		return nil, user.ErrNotFound
	}

	return reset, nil
}

func (s *stubRepo) SavePasswordReset(_ context.Context, reset *models.PasswordReset) error {
	s.resets[reset.UserID] = reset

	return nil
}

func (s *stubRepo) ConsumePasswordReset(_ context.Context, reset *models.PasswordReset, passwordHash string) error {
	u, ok := s.byID[reset.UserID]
	if !ok {
		return user.ErrNotFound
	}

	u.PasswordHash = passwordHash
	s.resets[reset.UserID] = reset

	return nil
}

func newTestServer(t *testing.T) (*Server, *capturingMailer) {
	t.Helper()

	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	mailer := &capturingMailer{}
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

	s := NewServer(cfg, slog.New(slog.DiscardHandler), Deps{
		DB:     okPinger{},
		Users:  user.NewService(newStubRepo(), testPepper),
		Tokens: tokens,
		Mail:   mailer,
	})

	return s, mailer
}

func postJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func TestGetUserRequiresBearer(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/users/"+uuid.Must(uuid.NewV7()).String(), nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreateUserHidesDuplicateEmail(t *testing.T) {
	s, _ := newTestServer(t)
	body := `{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`

	for i := 0; i < 2; i++ {
		rec := postJSON(t, s.http.Handler, "/v1/users", body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status = %d, want 204 body %s", i+1, rec.Code, rec.Body.Bytes())
		}
	}
}

func TestCreateUserRequiresPasswordConfirmation(t *testing.T) {
	s, _ := newTestServer(t)
	rec := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-charZ"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestSessionThenSelfFetch(t *testing.T) {
	s, _ := newTestServer(t)
	created := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`)

	if created.Code != http.StatusNoContent {
		t.Fatalf("create status = %d, want 204", created.Code)
	}

	logged := postJSON(t, s.http.Handler, "/v1/sessions",
		`{"email":"ada@example.com","password":"twelve-chars"}`)

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

	self := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	self.Header.Set("Authorization", "Bearer "+session.Token)
	got := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(got, self)

	if got.Code != http.StatusOK {
		t.Fatalf("GET /v1/me status = %d, want 200", got.Code)
	}

	byID := httptest.NewRequest(http.MethodGet, "/v1/users/"+session.User.ID.String(), nil)
	byID.Header.Set("Authorization", "Bearer "+session.Token)
	gotID := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(gotID, byID)

	if gotID.Code != http.StatusOK {
		t.Fatalf("self fetch by id status = %d, want 200", gotID.Code)
	}

	other := httptest.NewRequest(http.MethodGet, "/v1/users/"+uuid.Must(uuid.NewV7()).String(), nil)
	other.Header.Set("Authorization", "Bearer "+session.Token)
	hidden := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(hidden, other)

	if hidden.Code != http.StatusNotFound {
		t.Fatalf("other user status = %d, want 404", hidden.Code)
	}
}

func TestPasswordResetDeliversCodeAndChangesPassword(t *testing.T) {
	s, mailer := newTestServer(t)
	if rec := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	unknown := postJSON(t, s.http.Handler, "/v1/password-resets", `{"email":"missing@example.com"}`)
	if unknown.Code != http.StatusNoContent {
		t.Fatalf("unknown email status = %d, want 204", unknown.Code)
	}

	if mailer.code != "" {
		t.Fatal("a code was delivered for an unknown address")
	}

	requested := postJSON(t, s.http.Handler, "/v1/password-resets", `{"email":"ada@example.com"}`)
	if requested.Code != http.StatusNoContent {
		t.Fatalf("reset request status = %d body %s", requested.Code, requested.Body.Bytes())
	}

	if mailer.code == "" || mailer.to != "ada@example.com" {
		t.Fatalf("mailer got to=%q code=%q", mailer.to, mailer.code)
	}

	confirm := postJSON(t, s.http.Handler, "/v1/password-resets/confirm",
		`{"email":"ada@example.com","code":"`+mailer.code+`","password":"another-passw","password_confirm":"another-passw"}`)
	if confirm.Code != http.StatusNoContent {
		t.Fatalf("confirm status = %d body %s", confirm.Code, confirm.Body.Bytes())
	}

	old := postJSON(t, s.http.Handler, "/v1/sessions",
		`{"email":"ada@example.com","password":"twelve-chars"}`)
	if old.Code != http.StatusUnauthorized {
		t.Fatalf("old password status = %d, want 401", old.Code)
	}

	logged := postJSON(t, s.http.Handler, "/v1/sessions",
		`{"email":"ada@example.com","password":"another-passw"}`)
	if logged.Code != http.StatusCreated {
		t.Fatalf("new password status = %d body %s", logged.Code, logged.Body.Bytes())
	}
}

func TestHealthOmitsDependencyName(t *testing.T) {
	s, _ := newTestServer(t)
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
		Users:  user.NewService(newStubRepo(), testPepper),
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

func TestCreateUserRejectsMissingConfirmField(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/users", bytes.NewReader(
		[]byte(`{"name":"Ada","email":"ada@example.com","password":"twelve-chars"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 or 422", rec.Code)
	}
}
