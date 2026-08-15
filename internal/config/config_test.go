package config

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestAuthTokenTTLRejectsBareNumbers(t *testing.T) {
	t.Setenv("AUTH_TOKEN_TTL", "30")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_TOKEN_TTL") {
		t.Fatalf("Load() = %v, want an AUTH_TOKEN_TTL parse error", err)
	}
}

func TestAuthTokenTTLReadsADuration(t *testing.T) {
	t.Setenv("AUTH_TOKEN_TTL", "45m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if cfg.AuthTokenTTL != 45*time.Minute {
		t.Errorf("AuthTokenTTL = %s, want 45m", cfg.AuthTokenTTL)
	}
}

func TestValidateRejectsNonPositiveTokenTTL(t *testing.T) {
	c := productionConfig()
	c.AuthTokenTTL = 0

	if got := errorsJoin(c.validate()); !strings.Contains(got, "AUTH_TOKEN_TTL") {
		t.Fatalf("validate() = %q, want an AUTH_TOKEN_TTL error", got)
	}
}

func TestAddrUsesConfiguredHost(t *testing.T) {
	c := &Config{APIHost: "127.0.0.1", APIPort: 4000}
	if got := c.Addr(); got != net.JoinHostPort("127.0.0.1", "4000") {
		t.Errorf("Addr() = %q", got)
	}
}

func TestBindsLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"10.0.0.1", false},
	}
	for _, tc := range cases {
		c := &Config{APIHost: tc.host}
		if got := c.BindsLoopback(); got != tc.want {
			t.Errorf("BindsLoopback(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestProductionRejectsDisableSSLAndWeakPassword(t *testing.T) {
	c := productionConfig()
	c.PostgresSSLMode = "disable"
	c.PostgresPassword = "postgres"

	errs := c.validate()
	if len(errs) == 0 {
		t.Fatal("validate() accepted production with disable SSL and a weak password")
	}

	joined := errorsJoin(errs)
	if !strings.Contains(joined, "POSTGRES_SSL_MODE") {
		t.Errorf("missing SSL error in %q", joined)
	}

	if !strings.Contains(joined, "POSTGRES_PASSWORD") {
		t.Errorf("missing password error in %q", joined)
	}
}

func TestProductionRejectsPublicBindWithoutTLS(t *testing.T) {
	c := productionConfig()
	c.APIHost = "0.0.0.0"
	c.TLSCertFile = ""
	c.TLSKeyFile = ""

	errs := c.validate()
	if len(errs) == 0 {
		t.Fatal("validate() accepted a public bind without TLS")
	}
}

func TestProductionAcceptsLoopbackWithoutTLS(t *testing.T) {
	c := productionConfig()
	if errs := c.validate(); len(errs) != 0 {
		t.Fatalf("validate() = %v, want none", errs)
	}
}

func TestProductionRejectsMissingSMTP(t *testing.T) {
	c := productionConfig()
	c.SMTPHost = ""
	c.SMTPFrom = ""

	if errs := c.validate(); len(errs) == 0 {
		t.Fatal("validate() accepted production without SMTP")
	}
}

func TestProductionRejectsDevTokenSecret(t *testing.T) {
	c := productionConfig()
	c.AuthTokenSecret = devAuthTokenSecret

	if errs := c.validate(); len(errs) == 0 {
		t.Fatal("validate() accepted the development token secret")
	}
}

func TestProductionRejectsDevResetSecret(t *testing.T) {
	c := productionConfig()
	c.AuthResetSecret = devAuthResetSecret

	if errs := c.validate(); len(errs) == 0 {
		t.Fatal("validate() accepted the development reset secret")
	}
}

func TestProductionRejectsShortResetSecret(t *testing.T) {
	c := productionConfig()
	c.AuthResetSecret = "too-short"

	if errs := c.validate(); len(errs) == 0 {
		t.Fatal("validate() accepted a short reset secret")
	}
}

func productionConfig() *Config {
	return &Config{
		Env:                  EnvProduction,
		APIName:              "Example",
		APIHost:              "127.0.0.1",
		APIPort:              4000,
		AuthTokenSecret:      "production-secret-must-be-at-least-32b",
		AuthResetSecret:      "production-reset-must-be-at-least-32b",
		AuthTokenTTL:         time.Hour,
		RegisterPerMinute:    5,
		LoginPerMinute:       5,
		ResetPerMinute:       5,
		MaxRequestBytes:      1 << 20,
		PostgresPort:         5432,
		PostgresDatabaseName: "notes",
		PostgresSSLMode:      "require",
		PostgresPassword:     "not-a-default-password",
		SMTPHost:             "smtp.example.com",
		SMTPPort:             587,
		SMTPFrom:             "noreply@example.com",
		DBMaxOpenConns:       25,
		DBMaxIdleConns:       25,
	}
}

func errorsJoin(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}

	return strings.Join(parts, "; ")
}
