package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/go-example/internal/auth"
	"github.com/wokacz/go-example/internal/config"
	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/mail"
	"github.com/wokacz/go-example/internal/store/models"
	"github.com/wokacz/go-example/internal/store/repositories/memory"
)

var testPepper = []byte("0123456789abcdef0123456789abcdef")

var (
	errSMTPDown   = errors.New("smtp down")
	errResetStore = errors.New("store: replace password reset")
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// capturingMailer keeps the two code kinds apart. Collapsing them into one
// field would let a test assert on "the last code" and pass while the wrong
// flow delivered it.
type capturingMailer struct {
	to, code      string
	twoFactorTo   string
	twoFactorCode string
}

func (c *capturingMailer) SendPasswordReset(_ context.Context, to, code string) error {
	c.to, c.code = to, code

	return nil
}

func (c *capturingMailer) SendTwoFactorCode(_ context.Context, to, code string) error {
	c.twoFactorTo, c.twoFactorCode = to, code

	return nil
}

type failingMailer struct{}

func (failingMailer) SendPasswordReset(context.Context, string, string) error {
	return errSMTPDown
}

func (failingMailer) SendTwoFactorCode(context.Context, string, string) error {
	return errSMTPDown
}

// replaceResetErrorRepo is the in-memory repository with one method broken, so
// a test can prove that a storage failure on the reset path still answers 204.
type replaceResetErrorRepo struct {
	*memory.Users
	err error
}

func (r *replaceResetErrorRepo) ReplacePasswordReset(context.Context, *models.PasswordReset) error {
	return r.err
}

func newTestServer(t *testing.T) (*Server, *capturingMailer) {
	t.Helper()

	mailer := &capturingMailer{}

	return newTestAPI(t, mailer, memory.NewUsers()), mailer
}

func newTestAPI(t *testing.T, mailer mail.Sender, repo user.Repository) *Server {
	t.Helper()

	return newTestAPIConfig(t, mailer, repo, nil)
}

// newTestAPIConfig builds the server and lets a test adjust the configuration
// before it is wired. Rate limits in particular default to zero — which
// disables the limiter — so a test that wants to exercise it has to say so.
func newTestAPIConfig(t *testing.T, mailer mail.Sender, repo user.Repository, adjust func(*config.Config)) *Server {
	t.Helper()

	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
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

	if adjust != nil {
		adjust(cfg)
	}

	return NewServer(cfg, slog.New(slog.DiscardHandler), Deps{
		DB:     okPinger{},
		Users:  user.NewService(repo, testPepper, user.WithBcryptCost(bcrypt.MinCost)),
		Tokens: tokens,
		Mail:   mailer,
	})
}

func postJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, handler, request(t, http.MethodPost, path, body))
}

// request builds a JSON request. Headers are set by the caller afterwards,
// which is how the device token and the bearer token get attached.
func request(t *testing.T, method, path, body string) *http.Request {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return req
}

func do(t *testing.T, handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// sessionBody is the shared shape of both sign-in responses.
type sessionBody struct {
	TwoFactorRequired bool   `json:"two_factor_required"`
	DeviceToken       string `json:"device_token"`
	Token             string `json:"token"`
	User              struct {
		ID               uuid.UUID `json:"id"`
		TwoFactorEnabled bool      `json:"two_factor_enabled"`
	} `json:"user"`
}

func decodeSession(t *testing.T, rec *httptest.ResponseRecorder) sessionBody {
	t.Helper()

	var out sessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode session: %v (body %s)", err, rec.Body.Bytes())
	}

	return out
}

const (
	testEmail       = "ada@example.com"
	testPassword    = "twelve-chars"
	testRegisterAda = `{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`
	testSignInAda   = `{"email":"ada@example.com","password":"twelve-chars"}`
)

func registerAda(t *testing.T, s *Server) {
	t.Helper()

	if rec := postJSON(t, s.http.Handler, "/v1/users", testRegisterAda); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d body %s", rec.Code, rec.Body.Bytes())
	}
}

// signInAda signs in and fails the test unless the status matches, returning
// the decoded body so callers can pick out the token or the device token.
func signInAda(t *testing.T, s *Server, deviceToken string, want int) sessionBody {
	t.Helper()

	req := request(t, http.MethodPost, "/v1/sessions", testSignInAda)
	if deviceToken != "" {
		req.Header.Set("X-Device-Token", deviceToken)
	}

	rec := do(t, s.http.Handler, req)
	if rec.Code != want {
		t.Fatalf("sign-in status = %d, want %d; body %s", rec.Code, want, rec.Body.Bytes())
	}

	return decodeSession(t, rec)
}

// authed issues a request carrying a bearer token, and the device token when
// one is given.
func authed(t *testing.T, method, path, body, token, deviceToken string) *http.Request {
	t.Helper()

	req := request(t, method, path, body)
	req.Header.Set("Authorization", "Bearer "+token)

	if deviceToken != "" {
		req.Header.Set("X-Device-Token", deviceToken)
	}

	return req
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

func TestPasswordResetInvalidatesExistingToken(t *testing.T) {
	s, mailer := newTestServer(t)
	if rec := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	logged := postJSON(t, s.http.Handler, "/v1/sessions",
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

	if rec := postJSON(t, s.http.Handler, "/v1/password-resets", `{"email":"ada@example.com"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("reset request status = %d", rec.Code)
	}

	if rec := postJSON(t, s.http.Handler, "/v1/password-resets/confirm",
		`{"email":"ada@example.com","code":"`+mailer.code+`","password":"another-passw","password_confirm":"another-passw"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	self := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	self.Header.Set("Authorization", "Bearer "+session.Token)
	got := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(got, self)

	if got.Code != http.StatusUnauthorized {
		t.Fatalf("old token status = %d, want 401", got.Code)
	}
}

func TestPasswordResetRequestHidesMailFailure(t *testing.T) {
	s := newTestAPI(t, failingMailer{}, memory.NewUsers())
	if rec := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	rec := postJSON(t, s.http.Handler, "/v1/password-resets", `{"email":"ada@example.com"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 even when mail fails; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestPasswordResetRequestHidesPersistenceFailure(t *testing.T) {
	repo := &replaceResetErrorRepo{Users: memory.NewUsers(), err: errResetStore}
	s := newTestAPI(t, &capturingMailer{}, repo)
	if rec := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d", rec.Code)
	}

	unknown := postJSON(t, s.http.Handler, "/v1/password-resets", `{"email":"missing@example.com"}`)
	if unknown.Code != http.StatusNoContent {
		t.Fatalf("unknown email status = %d, want 204", unknown.Code)
	}

	registered := postJSON(t, s.http.Handler, "/v1/password-resets", `{"email":"ada@example.com"}`)
	if registered.Code != http.StatusNoContent {
		t.Fatalf("registered email status = %d, want 204 when persist fails; body %s", registered.Code, registered.Body.Bytes())
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
		Users:  user.NewService(memory.NewUsers(), testPepper, user.WithBcryptCost(bcrypt.MinCost)),
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
