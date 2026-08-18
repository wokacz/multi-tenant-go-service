package httptest

import (
	"bytes"
	"encoding/json"
	"net/http"
	stdhttptest "net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

func TestGetUserRequiresBearer(t *testing.T) {
	s, _ := NewTestServer(t)
	req := stdhttptest.NewRequest(http.MethodGet, "/v1/users/"+uuid.Must(uuid.NewV7()).String(), nil)
	rec := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreateUserHidesDuplicateEmail(t *testing.T) {
	s, _ := NewTestServer(t)
	body := `{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`

	for i := 0; i < 2; i++ {
		rec := PostJSON(t, s.Handler(), "/v1/users", body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status = %d, want 204 body %s", i+1, rec.Code, rec.Body.Bytes())
		}
	}
}

func TestCreateUserRequiresPasswordConfirmation(t *testing.T) {
	s, _ := NewTestServer(t)
	rec := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-charZ"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestSessionThenSelfFetch(t *testing.T) {
	s, _ := NewTestServer(t)
	created := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`)

	if created.Code != http.StatusNoContent {
		t.Fatalf("create status = %d, want 204", created.Code)
	}

	logged := PostJSON(t, s.Handler(), "/v1/sessions",
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

	self := stdhttptest.NewRequest(http.MethodGet, "/v1/me", nil)
	self.Header.Set("Authorization", "Bearer "+session.Token)
	got := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(got, self)

	if got.Code != http.StatusOK {
		t.Fatalf("GET /v1/me status = %d, want 200", got.Code)
	}

	byID := stdhttptest.NewRequest(http.MethodGet, "/v1/users/"+session.User.ID.String(), nil)
	byID.Header.Set("Authorization", "Bearer "+session.Token)
	gotID := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(gotID, byID)

	if gotID.Code != http.StatusOK {
		t.Fatalf("self fetch by id status = %d, want 200", gotID.Code)
	}

	other := stdhttptest.NewRequest(http.MethodGet, "/v1/users/"+uuid.Must(uuid.NewV7()).String(), nil)
	other.Header.Set("Authorization", "Bearer "+session.Token)
	hidden := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(hidden, other)

	if hidden.Code != http.StatusNotFound {
		t.Fatalf("other user status = %d, want 404", hidden.Code)
	}
}

func TestPasswordResetDeliversCodeAndChangesPassword(t *testing.T) {
	s, mailer := NewTestServer(t)
	if rec := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	unknown := PostJSON(t, s.Handler(), "/v1/password-resets", `{"email":"missing@example.com"}`)
	if unknown.Code != http.StatusNoContent {
		t.Fatalf("unknown email status = %d, want 204", unknown.Code)
	}

	if mailer.Code != "" {
		t.Fatal("a code was delivered for an unknown address")
	}

	requested := PostJSON(t, s.Handler(), "/v1/password-resets", `{"email":"ada@example.com"}`)
	if requested.Code != http.StatusNoContent {
		t.Fatalf("reset request status = %d body %s", requested.Code, requested.Body.Bytes())
	}

	if mailer.Code == "" || mailer.To != "ada@example.com" {
		t.Fatalf("mailer got to=%q code=%q", mailer.To, mailer.Code)
	}

	confirm := PostJSON(t, s.Handler(), "/v1/password-resets/confirm",
		`{"email":"ada@example.com","code":"`+mailer.Code+`","password":"another-passw","password_confirm":"another-passw"}`)
	if confirm.Code != http.StatusNoContent {
		t.Fatalf("confirm status = %d body %s", confirm.Code, confirm.Body.Bytes())
	}

	old := PostJSON(t, s.Handler(), "/v1/sessions",
		`{"email":"ada@example.com","password":"twelve-chars"}`)
	if old.Code != http.StatusUnauthorized {
		t.Fatalf("old password status = %d, want 401", old.Code)
	}

	logged := PostJSON(t, s.Handler(), "/v1/sessions",
		`{"email":"ada@example.com","password":"another-passw"}`)
	if logged.Code != http.StatusCreated {
		t.Fatalf("new password status = %d body %s", logged.Code, logged.Body.Bytes())
	}
}

func TestPasswordResetInvalidatesExistingToken(t *testing.T) {
	s, mailer := NewTestServer(t)
	if rec := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	logged := PostJSON(t, s.Handler(), "/v1/sessions",
		`{"email":"ada@example.com","password":"twelve-chars"}`)
	if logged.Code != http.StatusCreated {
		t.Fatalf("login status = %d", logged.Code)
	}

	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(logged.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	if rec := PostJSON(t, s.Handler(), "/v1/password-resets", `{"email":"ada@example.com"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("reset request status = %d", rec.Code)
	}

	if rec := PostJSON(t, s.Handler(), "/v1/password-resets/confirm",
		`{"email":"ada@example.com","code":"`+mailer.Code+`","password":"another-passw","password_confirm":"another-passw"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	self := stdhttptest.NewRequest(http.MethodGet, "/v1/me", nil)
	self.Header.Set("Authorization", "Bearer "+session.Token)
	got := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(got, self)

	if got.Code != http.StatusUnauthorized {
		t.Fatalf("old token status = %d, want 401", got.Code)
	}
}

func TestPasswordResetRequestHidesMailFailure(t *testing.T) {
	s := NewTestAPI(t, FailingMailer{}, memory.NewUsers())
	if rec := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	rec := PostJSON(t, s.Handler(), "/v1/password-resets", `{"email":"ada@example.com"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 even when mail fails; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestPasswordResetRequestHidesPersistenceFailure(t *testing.T) {
	repo := &ReplaceResetErrorRepo{Users: memory.NewUsers(), Err: ErrResetStore}
	s := NewTestAPI(t, &CapturingMailer{}, repo)
	if rec := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	unknown := PostJSON(t, s.Handler(), "/v1/password-resets", `{"email":"missing@example.com"}`)
	if unknown.Code != http.StatusNoContent {
		t.Fatalf("unknown email status = %d, want 204", unknown.Code)
	}

	registered := PostJSON(t, s.Handler(), "/v1/password-resets", `{"email":"ada@example.com"}`)
	if registered.Code != http.StatusNoContent {
		t.Fatalf("registered email status = %d, want 204 when persist fails; body %s", registered.Code, registered.Body.Bytes())
	}
}

func TestHealthOmitsDependencyName(t *testing.T) {
	s, _ := NewTestServer(t)
	req := stdhttptest.NewRequest(http.MethodGet, "/health", nil)
	rec := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

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
	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour, TestIssuer)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	s := api.NewServer(&config.Config{
		Env:               config.EnvProduction,
		APIName:           "test",
		APIHost:           "127.0.0.1",
		APIPort:           8000,
		HealthTimeout:     time.Second,
		MaxRequestBytes:   1 << 20,
		ReadHeaderTimeout: time.Second,
	}, slog.New(slog.DiscardHandler), api.Deps{
		DB:     okPinger{},
		Users:  user.NewService(memory.NewUsers(), testPepper, user.WithBcryptCost(bcrypt.MinCost)),
		Tokens: tokens,
	})

	for _, path := range []string{"/docs", "/openapi.json", "/openapi.yaml"} {
		req := stdhttptest.NewRequest(http.MethodGet, path, nil)
		rec := stdhttptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestCreateUserRejectsMissingConfirmField(t *testing.T) {
	s, _ := NewTestServer(t)
	req := stdhttptest.NewRequest(http.MethodPost, "/v1/users", bytes.NewReader(
		[]byte(`{"name":"Ada","email":"ada@example.com","password":"twelve-chars"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := stdhttptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 or 422", rec.Code)
	}
}
