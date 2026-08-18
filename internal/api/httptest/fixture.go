// Package httptest builds in-process HTTP integration tests against the API
// router. Requests go through httptest rather than a bound port.
package httptest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

var testPepper = []byte("0123456789abcdef0123456789abcdef")

var (
	ErrSMTPDown   = errors.New("smtp down")
	ErrResetStore = errors.New("store: replace password reset")
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// CapturingMailer keeps the two code kinds apart. Collapsing them into one
// field would let a test assert on "the last code" and pass while the wrong
// flow delivered it.
//
// TestIssuer is the installation name the test signer uses. It only has to match
// itself; auth's own tests are where the claim is checked against another issuer.
const TestIssuer = "test-issuer"

type CapturingMailer struct {
	To, Code        string
	TwoFactorTo     string
	TwoFactorCode   string
	InviteTo        string
	InviteOrg       string
	InviteToken     string
	InviteExpires   time.Time
	EmailChangeTo   string
	EmailChangeCode string

	// Invitations is every invitation message, in order.
	//
	// The single fields above keep only the last one, which was enough while one
	// request sent one message. A batch sends several, and "the last one" cannot
	// show that each invitee got a token of their own — which is the property that
	// stops one of them accepting as another.
	Invitations []SentInvitation
}

type SentInvitation struct {
	Email string
	Org   string
	Token string
}

func (c *CapturingMailer) SendPasswordReset(_ context.Context, to, code string) error {
	c.To, c.Code = to, code

	return nil
}

func (c *CapturingMailer) SendTwoFactorCode(_ context.Context, to, code string) error {
	c.TwoFactorTo, c.TwoFactorCode = to, code

	return nil
}

func (c *CapturingMailer) SendEmailChange(_ context.Context, to, code string) error {
	c.EmailChangeTo, c.EmailChangeCode = to, code

	return nil
}

func (c *CapturingMailer) SendInvitation(_ context.Context, to, orgName, token string, expiresAt time.Time) error {
	c.InviteTo, c.InviteOrg, c.InviteToken, c.InviteExpires = to, orgName, token, expiresAt
	c.Invitations = append(c.Invitations, SentInvitation{Email: to, Org: orgName, Token: token})

	return nil
}

type FailingMailer struct{}

func (FailingMailer) SendPasswordReset(context.Context, string, string) error {
	return ErrSMTPDown
}

func (FailingMailer) SendTwoFactorCode(context.Context, string, string) error {
	return ErrSMTPDown
}

func (FailingMailer) SendEmailChange(context.Context, string, string) error {
	return errors.New("smtp is down")
}

func (FailingMailer) SendInvitation(context.Context, string, string, string, time.Time) error {
	return ErrSMTPDown
}

// ReplaceResetErrorRepo is the in-memory repository with one method broken, so
// a test can prove that a storage failure on the reset path still answers 204.
type ReplaceResetErrorRepo struct {
	*memory.Users
	Err error
}

func (r *ReplaceResetErrorRepo) ReplacePasswordReset(context.Context, *ent.PasswordReset) error {
	return r.Err
}

func NewTestServer(t *testing.T) (*api.Server, *CapturingMailer) {
	t.Helper()

	mailer := &CapturingMailer{}

	return NewTestAPI(t, mailer, memory.NewUsers()), mailer
}

func NewTestAPI(t *testing.T, mailer mail.Sender, repo user.Repository) *api.Server {
	t.Helper()

	server, _, _ := NewTestAPIConfig(t, mailer, repo, nil)

	return server
}

// NewTestAPIConfig builds the server and lets a test adjust the configuration
// before it is wired. Rate limits in particular default to zero — which
// disables the limiter — so a test that wants to exercise it has to say so.
//
// The authorization fake is returned rather than taken, because every test that
// needs one needs to build fixtures in it, and handing back the instance the
// server actually consults is what stops a test setting up an organization the
// server cannot see.
func NewTestAPIConfig(
	t *testing.T,
	mailer mail.Sender,
	repo user.Repository,
	adjust func(*config.Config),
) (*api.Server, *memory.Authz, *user.Service) {
	t.Helper()

	return NewTestAPIConfigTel(t, mailer, repo, telemetry.Disabled(), adjust)
}

// NewTestAPIConfigTel is the same with the telemetry handed in, for the tests that
// assert on what was recorded. Everything else takes the no-op one: a counter behind
// a discarding meter costs an atomic add, and a test that does not read it should not
// have to build a reader.
func NewTestAPIConfigTel(
	t *testing.T,
	mailer mail.Sender,
	repo user.Repository,
	tel *telemetry.Telemetry,
	adjust func(*config.Config),
) (*api.Server, *memory.Authz, *user.Service) {
	t.Helper()

	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour, TestIssuer)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	cfg := &config.Config{
		Env:               config.EnvDevelopment,
		APIName:           "test",
		APIHost:           "127.0.0.1",
		APIPort:           8000,
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

	// The authorization fake joins to the user fake for names and addresses,
	// the way the SQL joins to the users table. Passing the repository the
	// service already uses is what keeps a member's account and their
	// membership describing the same person.
	users, _ := repo.(*memory.Users)
	authzRepo := memory.NewAuthz(users)
	accounts := user.NewService(repo, testPepper, user.WithBcryptCost(bcrypt.MinCost))
	authzService := authz.NewService(authzRepo)

	server := api.NewServer(cfg, slog.New(slog.DiscardHandler), api.Deps{
		DB:        okPinger{},
		Users:     accounts,
		Tokens:    tokens,
		Mail:      mailer,
		Authz:     authzService,
		Snapshots: authzService,
		Orgs:      orgs.NewService(authzRepo, authzRepo, authzRepo),
		Audit:     audit.NewService(authzRepo, authzRepo),
		Telemetry: tel,
	})

	return server, authzRepo, accounts
}

func PostJSON(t *testing.T, handler http.Handler, path, body string) *stdhttptest.ResponseRecorder {
	t.Helper()

	return Do(t, handler, Request(t, http.MethodPost, path, body))
}

// Request builds a JSON request. Headers are set by the caller afterwards,
// which is how the device token and the bearer token get attached.
func Request(t *testing.T, method, path, body string) *http.Request {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := stdhttptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return req
}

func Do(t *testing.T, handler http.Handler, req *http.Request) *stdhttptest.ResponseRecorder {
	t.Helper()

	rec := stdhttptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// SessionBody is the shared shape of both sign-in responses.
type SessionBody struct {
	TwoFactorRequired bool   `json:"two_factor_required"`
	DeviceToken       string `json:"device_token"`
	Token             string `json:"token"`
	User              struct {
		ID               uuid.UUID `json:"id"`
		TwoFactorEnabled bool      `json:"two_factor_enabled"`
	} `json:"user"`
}

func DecodeSession(t *testing.T, rec *stdhttptest.ResponseRecorder) SessionBody {
	t.Helper()

	var out SessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode session: %v (body %s)", err, rec.Body.Bytes())
	}

	return out
}

const (
	TestEmail       = "ada@example.com"
	TestPassword    = "twelve-chars"
	TestRegisterAda = `{"name":"Ada","email":"ada@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`
	TestSignInAda   = `{"email":"ada@example.com","password":"twelve-chars"}`
)

func RegisterAda(t *testing.T, s *api.Server) {
	t.Helper()

	if rec := PostJSON(t, s.Handler(), "/v1/users", TestRegisterAda); rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d body %s", rec.Code, rec.Body.Bytes())
	}
}

// SignInAda signs in and fails the test unless the status matches, returning
// the decoded body so callers can pick out the token or the device token.
func SignInAda(t *testing.T, s *api.Server, deviceToken string, want int) SessionBody {
	t.Helper()

	req := Request(t, http.MethodPost, "/v1/sessions", TestSignInAda)
	if deviceToken != "" {
		req.Header.Set("X-Device-Token", deviceToken)
	}

	rec := Do(t, s.Handler(), req)
	if rec.Code != want {
		t.Fatalf("sign-in status = %d, want %d; body %s", rec.Code, want, rec.Body.Bytes())
	}

	return DecodeSession(t, rec)
}

// Authed issues a request carrying a bearer token, and the device token when
// one is given.
func Authed(t *testing.T, method, path, body, token, deviceToken string) *http.Request {
	t.Helper()

	req := Request(t, method, path, body)
	req.Header.Set("Authorization", "Bearer "+token)

	if deviceToken != "" {
		req.Header.Set("X-Device-Token", deviceToken)
	}

	return req
}

// WithDeviceToken satisfies the required header on /v1/sessions/verify so the
// request reaches the limiter instead of being rejected as malformed.
func WithDeviceToken(req *http.Request) *http.Request {
	req.Header.Set("X-Device-Token", "irrelevant-for-rate-limiting")

	return req
}

// ProblemBody is the extended RFC 7807 document this API emits. The two extra
// fields are what a client actually branches on: code is stable across
// languages and releases, required_permission is the raw key it can look up in
// the permission catalog.
type ProblemBody struct {
	Status             int    `json:"status"`
	Detail             string `json:"detail"`
	Code               string `json:"code"`
	RequiredPermission string `json:"required_permission"`
}

func DecodeProblem(t *testing.T, body []byte) ProblemBody {
	t.Helper()

	var out ProblemBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode problem: %v (body %s)", err, body)
	}

	return out
}
