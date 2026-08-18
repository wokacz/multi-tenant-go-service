package api

import (
	"context"
	"io"
	"net/http"
	stdhttptest "net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// Guard tests in this package need a server without importing httptest, which
// would cycle back through api. These helpers mirror the ones in httptest and
// stay here on purpose.

var harnessPepper = []byte("0123456789abcdef0123456789abcdef")

type harnessMailer struct{}

func (harnessMailer) SendPasswordReset(context.Context, string, string) error { return nil }
func (harnessMailer) SendTwoFactorCode(context.Context, string, string) error { return nil }
func (harnessMailer) SendEmailChange(context.Context, string, string) error   { return nil }
func (harnessMailer) SendInvitation(context.Context, string, string, string, time.Time) error {
	return nil
}

type harnessPinger struct{}

func (harnessPinger) Ping(context.Context) error { return nil }

func newTestServer(t *testing.T) (*Server, harnessMailer) {
	t.Helper()

	mailer := harnessMailer{}

	return newTestAPI(t, mailer, memory.NewUsers()), mailer
}

func newTestAPI(t *testing.T, mailer mail.Sender, repo user.Repository) *Server {
	t.Helper()

	server, _, _ := newTestAPIConfig(t, mailer, repo, nil)

	return server
}

func newTestAPIConfig(
	t *testing.T,
	mailer mail.Sender,
	repo user.Repository,
	adjust func(*config.Config),
) (*Server, *memory.Authz, *user.Service) {
	t.Helper()

	tokens, err := auth.NewSigner(strings.Repeat("k", 32), time.Hour, "test-issuer")
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

	users, _ := repo.(*memory.Users)
	authzRepo := memory.NewAuthz(users)
	accounts := user.NewService(repo, harnessPepper, user.WithBcryptCost(bcrypt.MinCost))
	authzService := authz.NewService(authzRepo)

	server := NewServer(cfg, slog.New(slog.DiscardHandler), Deps{
		DB:        harnessPinger{},
		Users:     accounts,
		Tokens:    tokens,
		Mail:      mailer,
		Authz:     authzService,
		Snapshots: authzService,
		Orgs:      orgs.NewService(authzRepo, authzRepo, authzRepo),
		Audit:     audit.NewService(authzRepo, authzRepo),
		Telemetry: telemetry.Disabled(),
	})

	return server, authzRepo, accounts
}

func request(t *testing.T, method, path, body string) *http.Request {
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

func do(t *testing.T, handler http.Handler, req *http.Request) *stdhttptest.ResponseRecorder {
	t.Helper()

	rec := stdhttptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func withDeviceToken(req *http.Request) *http.Request {
	req.Header.Set("X-Device-Token", "irrelevant-for-rate-limiting")

	return req
}
