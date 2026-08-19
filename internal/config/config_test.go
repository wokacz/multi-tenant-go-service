package config

import (
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAuthTokenTTLRejectsBareNumbers(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("AUTH_TOKEN_TTL", "30")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_TOKEN_TTL") {
		t.Fatalf("Load() = %v, want an AUTH_TOKEN_TTL parse error", err)
	}
}

func TestAuthTokenTTLReadsADuration(t *testing.T) {
	t.Setenv("ENV", "development")
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
	c := &Config{APIHost: "127.0.0.1", APIPort: 8000}
	if got := c.Addr(); got != net.JoinHostPort("127.0.0.1", "8000") {
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

func TestLoadRequiresENV(t *testing.T) {
	t.Setenv("ENV", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ENV") {
		t.Fatalf("Load() = %v, want an ENV error", err)
	}
}

func TestDevelopmentSecretsAreRejectedOffLoopback(t *testing.T) {
	c := &Config{
		Env:                  EnvDevelopment,
		APIName:              "Example",
		APIHost:              "0.0.0.0",
		APIPort:              8000,
		AuthTokenSecret:      devAuthTokenSecret,
		AuthResetSecret:      devAuthResetSecret,
		AuthTokenTTL:         time.Hour,
		AuthTokenIssuer:      "test-issuer",
		LogFormat:            LogFormatJSON,
		RegisterPerMinute:    5,
		LoginPerMinute:       5,
		ResetPerMinute:       5,
		MaxRequestBytes:      1 << 20,
		PostgresPort:         5432,
		PostgresDatabaseName: "notes",
		PostgresSSLMode:      "disable",
		PostgresPassword:     "postgres",
		DBMaxOpenConns:       25,
		DBMaxIdleConns:       25,
	}

	joined := errorsJoin(c.validate())
	if !strings.Contains(joined, "AUTH_TOKEN_SECRET") {
		t.Errorf("missing token secret error in %q", joined)
	}

	if !strings.Contains(joined, "AUTH_RESET_SECRET") {
		t.Errorf("missing reset secret error in %q", joined)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies("127.0.0.1, 10.0.0.0/8")
	if err != nil {
		t.Fatalf("parseTrustedProxies() = %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	if !got[0].Contains(net.ParseIP("127.0.0.1")) {
		t.Errorf("first CIDR = %s, want 127.0.0.1/32", got[0].String())
	}

	if !got[1].Contains(net.ParseIP("10.1.2.3")) {
		t.Errorf("second CIDR = %s, want 10.0.0.0/8", got[1].String())
	}
}

func TestParseTrustedProxiesRejectsGarbage(t *testing.T) {
	if _, err := parseTrustedProxies("not-an-ip"); err == nil {
		t.Fatal("parseTrustedProxies() = nil, want an error")
	}
}

func TestParseAllowedOrigins(t *testing.T) {
	got, err := parseAllowedOrigins("https://app.example.com, http://localhost:4200, https://APP.Example.COM:8443")
	if err != nil {
		t.Fatalf("parseAllowedOrigins() = _, %v", err)
	}

	// Lower-cased, because a browser sends the scheme and host that way and the
	// comparison is exact.
	want := []string{"https://app.example.com", "http://localhost:4200", "https://app.example.com:8443"}
	if !slices.Equal(got, want) {
		t.Errorf("parseAllowedOrigins() = %v, want %v", got, want)
	}
}

// TestParseAllowedOriginsRejectsAnythingThatWouldNeverMatch is the point of
// validating here at all. An origin with a path or a bare host produces an
// allowlist that silently matches nothing, which is the worst way for a security
// setting to fail: it looks configured.
func TestParseAllowedOriginsRejectsAnythingThatWouldNeverMatch(t *testing.T) {
	for _, value := range []string{
		"*",
		"https://app.example.com/",
		"https://app.example.com/app",
		"app.example.com",
		"ftp://app.example.com",
		"https://",
	} {
		if _, err := parseAllowedOrigins(value); err == nil {
			t.Errorf("parseAllowedOrigins(%q) = _, nil; want an error", value)
		}
	}
}

func TestProductionRejectsAPlaintextOrigin(t *testing.T) {
	c := productionConfig()
	c.CORSAllowedOrigins = []string{"http://app.example.com"}

	if errs := c.validate(); len(errs) == 0 {
		t.Error("validate() accepted an http:// origin in production")
	}

	// Loopback stays allowed, for the same reason TLS is optional there.
	c.CORSAllowedOrigins = []string{"http://localhost:4200", "https://app.example.com"}

	for _, err := range c.validate() {
		if strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
			t.Errorf("validate() refused a loopback origin in production: %v", err)
		}
	}
}

func productionConfig() *Config {
	return &Config{
		Env:                        EnvProduction,
		APIName:                    "Example",
		APIHost:                    "127.0.0.1",
		APIPort:                    8000,
		AuthTokenSecret:            "production-secret-must-be-at-least-32b",
		AuthResetSecret:            "production-reset-must-be-at-least-32b",
		AuthTokenTTL:               time.Hour,
		AuthTokenIssuer:            "test-issuer",
		LogFormat:                  LogFormatJSON,
		RegisterPerMinute:          5,
		LoginPerMinute:             5,
		ResetPerMinute:             5,
		MaxRequestBytes:            1 << 20,
		FilesUploadPerMinute:       20,
		FilesStorageBackend:        "local",
		FilesStoragePath:           "/var/files",
		FilesMaxBytes:              defaultFilesBytes,
		FilesAvatarMaxBytes:        defaultAvatarBytes,
		FilesScanMode:              ScanOff,
		FilesClamAVTimeout:         10 * time.Second,
		FilesEncryptionKey:         []byte("production-files-key-must-be-32b"),
		FilesBlockExecutables:      true,
		FilesRequireDeclaredMatch:  true,
		FilesRequireExtensionMatch: true,
		PostgresPort:               5432,
		PostgresDatabaseName:       "notes",
		PostgresSSLMode:            "require",
		PostgresPassword:           "not-a-default-password",
		SMTPHost:                   "smtp.example.com",
		SMTPPort:                   587,
		SMTPFrom:                   "noreply@example.com",
		DBMaxOpenConns:             25,
		DBMaxIdleConns:             25,
	}
}

func errorsJoin(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}

	return strings.Join(parts, "; ")
}

func TestFilesScanRequiredNeedsAnAddress(t *testing.T) {
	c := productionConfig()
	c.FilesScanMode = ScanRequired
	c.FilesClamAVAddr = ""

	if got := errorsJoin(c.validate()); !strings.Contains(got, "FILES_CLAMAV_ADDR") {
		t.Fatalf("validate() = %q, want a FILES_CLAMAV_ADDR error", got)
	}
}

func TestParseAllowedTypesRejectsAWildcard(t *testing.T) {
	if _, err := parseAllowedTypes("*"); err == nil {
		t.Fatal("parseAllowedTypes(*) = nil, want an error")
	}
}

func TestParseEncryptionKeyAcceptsHexAndRaw(t *testing.T) {
	raw := strings.Repeat("a", 32)
	got, err := parseEncryptionKey(raw)
	if err != nil {
		t.Fatalf("parseEncryptionKey(raw) = %v", err)
	}

	if string(got) != raw {
		t.Errorf("parseEncryptionKey(raw) = %q, want the raw bytes", got)
	}

	hexKey := strings.Repeat("ab", 32)
	got, err = parseEncryptionKey(hexKey)
	if err != nil {
		t.Fatalf("parseEncryptionKey(hex) = %v", err)
	}

	if len(got) != 32 {
		t.Errorf("hex key length = %d, want 32", len(got))
	}
}
