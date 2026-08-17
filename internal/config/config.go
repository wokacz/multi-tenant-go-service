package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env names the deployment environment. It gates the handful of behaviours that
// must differ between a laptop and production on purpose rather than by
// accident — log format, whether the API docs are published, TLS and secret
// requirements.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvProduction  Env = "production"
)

func (e Env) IsProduction() bool { return e == EnvProduction }

// devAuthTokenSecret is the development fallback. Production rejects it so a
// forgotten AUTH_TOKEN_SECRET cannot ship with a value that is in the repo.
const devAuthTokenSecret = "dev-only-not-for-production-use-32bytes"

// devAuthResetSecret is a separate development fallback. Sharing the JWT
// secret would make rotating session tokens invalidate codes already emailed.
const devAuthResetSecret = "dev-only-reset-pepper-not-for-prod-32b"

const minAuthTokenSecretBytes = 32

// Config holds the configuration values for the application.
type Config struct {
	Env     Env
	APIName string
	APIHost string
	APIPort int

	// TLSCertFile and TLSKeyFile enable HTTPS on the listener. Both must be
	// set, or neither. Production listening on a non-loopback address requires
	// them: passwords otherwise travel in the clear. A reverse proxy that
	// terminates TLS should keep API_HOST on loopback instead.
	TLSCertFile string
	TLSKeyFile  string

	// AuthTokenSecret signs session tokens. Development on loopback fills in a
	// well-known value when unset; anything reachable from another machine
	// refuses to start without a unique secret.
	AuthTokenSecret string

	// AuthTokenTTL is how long a session token stays valid. It is the only
	// window in which a token whose device was revoked could still be used if
	// the per-request device check were ever removed, and the window a stolen
	// token is useful for after the password is changed.
	AuthTokenTTL time.Duration

	// AuthResetSecret peppers HMAC hashes of password-reset codes. It is a
	// separate secret from AuthTokenSecret so rotating session tokens does
	// not invalidate codes already delivered. Development on loopback fills
	// in a well-known value when unset; anything reachable from another
	// machine requires a unique one.
	AuthResetSecret string

	// RegisterPerMinute / LoginPerMinute cap bcrypt-heavy endpoints per peer
	// address. Zero disables the limiter, which is only for tests.
	RegisterPerMinute int
	LoginPerMinute    int
	ResetPerMinute    int

	// MaxRequestBytes bounds the request body so a client cannot pin memory
	// with an unbounded JSON document.
	MaxRequestBytes int64

	// ReadHeaderTimeout is the one timeout that is a security control rather
	// than a nicety: without it a client can pin a connection open forever by
	// dribbling out request headers one byte at a time.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	// HealthTimeout bounds the health check's own work. A probe that hangs is
	// worse than one that fails: orchestrators and load balancers block on it.
	HealthTimeout time.Duration

	PostgresHost         string
	PostgresPort         int
	PostgresUser         string
	PostgresPassword     string
	PostgresDatabaseName string
	PostgresSSLMode      string

	// Connection pool. MaxIdleConns matches MaxOpenConns on purpose: a lower
	// idle count makes the pool close and reopen connections under steady load,
	// and a Postgres handshake is expensive enough to show up in latency.
	DBMaxOpenConns int
	DBMaxIdleConns int
	// ConnMaxLifetime caps how long a connection is reused. Without it a pool
	// pins connections to whichever backend it first reached, so connections
	// survive a failover pointing at the old primary.
	DBConnMaxLifetime time.Duration
	DBConnMaxIdleTime time.Duration
	// DBSlowQueryThreshold is the point at which a query is logged as slow.
	DBSlowQueryThreshold time.Duration
	DBConnectTimeout     time.Duration

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string

	// TrustedProxies are CIDR ranges whose X-Forwarded-For (and similar)
	// headers may be believed. Empty means never trust a header: the rate
	// limiter and audit log key on the TCP peer, which behind an unlisted
	// proxy is the proxy itself. Spoofing X-Forwarded-For from an untrusted
	// address cannot mint extra buckets.
	TrustedProxies []net.IPNet

	// MailLogCodes writes one-time codes to stderr when SMTP is unset. It is
	// for a laptop with a TTY; shared logs must not receive them. Production
	// never reaches that path — SMTP_HOST is required there.
	MailLogCodes bool
}

// Load reads the configuration from the environment.
//
// Every problem found is reported at once via errors.Join. Configuration is
// fixed by editing a file and restarting, so surfacing one error per restart
// turns a single typo into a slow guessing game.
func Load() (*Config, error) {
	var errs []error

	getInt := func(key string, defaultValue int) int {
		v, err := getEnvInt(key, defaultValue)
		if err != nil {
			errs = append(errs, err)
		}

		return v
	}

	getDuration := func(key string, defaultValue time.Duration) time.Duration {
		v, err := getEnvDuration(key, defaultValue)
		if err != nil {
			errs = append(errs, err)
		}

		return v
	}

	cfg := &Config{
		// ENV has no default. A forgotten variable on a server used to
		// silently select development, including the well-known secrets
		// below. An explicit value is a one-line .env; an implicit one is a
		// forgeable installation.
		Env:     Env(os.Getenv("ENV")),
		APIName: getEnv("API_NAME", "Example"),
		APIHost: getEnv("API_HOST", "127.0.0.1"),
		APIPort: getInt("API_PORT", 8000),

		TLSCertFile: getEnv("TLS_CERT_FILE", ""),
		TLSKeyFile:  getEnv("TLS_KEY_FILE", ""),

		AuthTokenSecret: os.Getenv("AUTH_TOKEN_SECRET"),
		AuthTokenTTL:    getDuration("AUTH_TOKEN_TTL", time.Hour),
		AuthResetSecret: os.Getenv("AUTH_RESET_SECRET"),

		RegisterPerMinute: getInt("REGISTER_PER_MINUTE", 5),
		LoginPerMinute:    getInt("LOGIN_PER_MINUTE", 5),
		ResetPerMinute:    getInt("RESET_PER_MINUTE", 5),
		MaxRequestBytes:   int64(getInt("MAX_REQUEST_BYTES", 1<<20)),

		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   15 * time.Second,
		HealthTimeout:     2 * time.Second,

		PostgresHost:         getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:         getInt("POSTGRES_PORT", 5432),
		PostgresUser:         getEnv("POSTGRES_USER", "postgres"),
		PostgresPassword:     getEnv("POSTGRES_PASSWORD", "postgres"),
		PostgresDatabaseName: getEnv("POSTGRES_DATABASE_NAME", "postgres"),
		PostgresSSLMode:      getEnv("POSTGRES_SSL_MODE", "disable"),

		DBMaxOpenConns:       getInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:       getInt("DB_MAX_IDLE_CONNS", 25),
		DBConnMaxLifetime:    30 * time.Minute,
		DBConnMaxIdleTime:    5 * time.Minute,
		DBSlowQueryThreshold: 200 * time.Millisecond,
		DBConnectTimeout:     10 * time.Second,

		SMTPHost:     getEnv("SMTP_HOST", ""),
		SMTPPort:     getInt("SMTP_PORT", 587),
		SMTPUser:     getEnv("SMTP_USER", ""),
		SMTPPassword: getEnv("SMTP_PASSWORD", ""),
		SMTPFrom:     getEnv("SMTP_FROM", ""),

		MailLogCodes: getEnvBool("MAIL_LOG_CODES"),
	}

	proxies, err := parseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		errs = append(errs, err)
	} else {
		cfg.TrustedProxies = proxies
	}

	// Well-known secrets are a laptop convenience, and only there. Filling
	// them in when the process is reachable from another machine would let a
	// forgotten AUTH_TOKEN_SECRET mint session tokens with a value that is
	// in the repository. Production refuses those values even when set.
	if cfg.AuthTokenSecret == "" && cfg.Env == EnvDevelopment && cfg.BindsLoopback() {
		cfg.AuthTokenSecret = devAuthTokenSecret
	}

	if cfg.AuthResetSecret == "" && cfg.Env == EnvDevelopment && cfg.BindsLoopback() {
		cfg.AuthResetSecret = devAuthResetSecret
	}

	errs = append(errs, cfg.validate()...)

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() []error {
	var errs []error

	switch c.Env {
	case EnvDevelopment, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("config: ENV must be %q or %q, got %q",
			EnvDevelopment, EnvProduction, c.Env))
	}

	if c.APIName == "" {
		errs = append(errs, errors.New("config: API_NAME must not be empty"))
	}

	if c.APIHost == "" {
		errs = append(errs, errors.New("config: API_HOST must not be empty"))
	}

	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		errs = append(errs, errors.New("config: TLS_CERT_FILE and TLS_KEY_FILE must both be set, or both empty"))
	}

	if c.Env.IsProduction() && !c.BindsLoopback() && !c.TLSEnabled() {
		errs = append(errs, errors.New("config: production requires TLS_CERT_FILE and TLS_KEY_FILE unless API_HOST is loopback"))
	}

	if len(c.AuthTokenSecret) < minAuthTokenSecretBytes {
		errs = append(errs, fmt.Errorf("config: AUTH_TOKEN_SECRET must be at least %d bytes", minAuthTokenSecretBytes))
	}

	if c.AuthTokenSecret == devAuthTokenSecret && (c.Env.IsProduction() || !c.BindsLoopback()) {
		errs = append(errs, errors.New("config: AUTH_TOKEN_SECRET must not use the development default unless API_HOST is loopback"))
	}

	if len(c.AuthResetSecret) < minAuthTokenSecretBytes {
		errs = append(errs, fmt.Errorf("config: AUTH_RESET_SECRET must be at least %d bytes", minAuthTokenSecretBytes))
	}

	if c.AuthResetSecret == devAuthResetSecret && (c.Env.IsProduction() || !c.BindsLoopback()) {
		errs = append(errs, errors.New("config: AUTH_RESET_SECRET must not use the development default unless API_HOST is loopback"))
	}

	// auth.NewSigner refuses a non-positive TTL as well, but failing here puts
	// it in the same batch as every other configuration error instead of
	// aborting the assembly in main one problem later.
	if c.AuthTokenTTL <= 0 {
		errs = append(errs, fmt.Errorf("config: AUTH_TOKEN_TTL must be positive, got %s", c.AuthTokenTTL))
	}

	if c.RegisterPerMinute < 0 {
		errs = append(errs, fmt.Errorf("config: REGISTER_PER_MINUTE must be >= 0, got %d", c.RegisterPerMinute))
	}

	if c.LoginPerMinute < 0 {
		errs = append(errs, fmt.Errorf("config: LOGIN_PER_MINUTE must be >= 0, got %d", c.LoginPerMinute))
	}

	if c.ResetPerMinute < 0 {
		errs = append(errs, fmt.Errorf("config: RESET_PER_MINUTE must be >= 0, got %d", c.ResetPerMinute))
	}

	if c.MaxRequestBytes < 1024 {
		errs = append(errs, fmt.Errorf("config: MAX_REQUEST_BYTES must be at least 1024, got %d", c.MaxRequestBytes))
	}

	for _, p := range []struct {
		key   string
		value int
	}{
		{"API_PORT", c.APIPort},
		{"POSTGRES_PORT", c.PostgresPort},
	} {
		if p.value < 1 || p.value > 65535 {
			errs = append(errs, fmt.Errorf("config: %s must be between 1 and 65535, got %d", p.key, p.value))
		}
	}

	if c.SMTPHost != "" && (c.SMTPPort < 1 || c.SMTPPort > 65535) {
		errs = append(errs, fmt.Errorf("config: SMTP_PORT must be between 1 and 65535, got %d", c.SMTPPort))
	}

	if c.PostgresDatabaseName == "" {
		errs = append(errs, errors.New("config: POSTGRES_DATABASE_NAME must not be empty"))
	}

	if c.Env.IsProduction() && !postgresSSLModeSecure(c.PostgresSSLMode) {
		errs = append(errs, fmt.Errorf("config: POSTGRES_SSL_MODE must be require, verify-ca or verify-full in production, got %q", c.PostgresSSLMode))
	}

	if c.Env.IsProduction() && weakPostgresPassword(c.PostgresPassword) {
		errs = append(errs, errors.New("config: POSTGRES_PASSWORD is too weak for production"))
	}

	if c.Env.IsProduction() && c.SMTPHost == "" {
		errs = append(errs, errors.New("config: SMTP_HOST is required in production so password-reset codes can be delivered"))
	}

	if c.SMTPHost != "" && c.SMTPFrom == "" {
		errs = append(errs, errors.New("config: SMTP_FROM must not be empty when SMTP_HOST is set"))
	}

	if c.DBMaxOpenConns < 1 {
		errs = append(errs, fmt.Errorf("config: DB_MAX_OPEN_CONNS must be at least 1, got %d", c.DBMaxOpenConns))
	}

	// database/sql silently treats an idle count above the open limit as equal
	// to it, so an inconsistent pair here is a misconfiguration worth naming
	// rather than quietly correcting.
	if c.DBMaxIdleConns < 1 || c.DBMaxIdleConns > c.DBMaxOpenConns {
		errs = append(errs, fmt.Errorf("config: DB_MAX_IDLE_CONNS must be between 1 and DB_MAX_OPEN_CONNS (%d), got %d",
			c.DBMaxOpenConns, c.DBMaxIdleConns))
	}

	return errs
}

// Addr is the address the HTTP server binds to.
func (c *Config) Addr() string {
	return net.JoinHostPort(c.APIHost, strconv.Itoa(c.APIPort))
}

// TLSEnabled reports whether the process will serve HTTPS itself.
func (c *Config) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// BindsLoopback is true when the listener cannot be reached from another
// machine. Production may skip in-process TLS in that case, because a proxy
// on the same host is expected to terminate it.
func (c *Config) BindsLoopback() bool {
	host := c.APIHost
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func postgresSSLModeSecure(mode string) bool {
	switch mode {
	case "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

func weakPostgresPassword(password string) bool {
	switch password {
	case "", "postgres", "password", "change-me", "changeme", "admin", "secret":
		return true
	default:
		return false
	}
}

// DSN builds the Postgres connection string.
//
// It goes through net/url rather than fmt.Sprintf so a password containing @,
// / or : is escaped instead of silently corrupting the URL — the resulting
// failure is a confusing "host not found", not an obvious quoting bug.
func (c *Config) DSN() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.PostgresUser, c.PostgresPassword),
		Host:     net.JoinHostPort(c.PostgresHost, strconv.Itoa(c.PostgresPort)),
		Path:     c.PostgresDatabaseName,
		RawQuery: url.Values{"sslmode": {c.PostgresSSLMode}}.Encode(),
	}

	return u.String()
}

func parseTrustedProxies(value string) ([]net.IPNet, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	out := make([]net.IPNet, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("config: TRUSTED_PROXIES contains invalid address %q", part)
			}

			if ip.To4() != nil {
				part += "/32"
			} else {
				part += "/128"
			}
		}

		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("config: TRUSTED_PROXIES contains invalid CIDR %q", part)
		}

		out = append(out, *cidr)
	}

	return out, nil
}

func getEnvBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// getEnv retrieves the value of the environment variable named by the key.
// If the variable is not present, it returns the provided default value.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}

// getEnvInt retrieves the value of the environment variable named by the key
// and converts it to an integer. An unset variable falls back to the default; a
// set but unparseable one is an error, because silently falling back would let
// API_PORT=4OOO start the server on 8000 with nothing to show for it.
func getEnvInt(key string, defaultValue int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}

	i, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue, fmt.Errorf("config: %s must be an integer, got %q", key, value)
	}

	return i, nil
}

// getEnvDuration reads a Go duration string such as "45m" or "12h". A bare
// number is rejected rather than guessed at: "AUTH_TOKEN_TTL=30" is as likely
// to mean thirty seconds as thirty minutes, and picking one silently is how a
// token ends up living sixty times longer than intended.
func getEnvDuration(key string, defaultValue time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}

	d, err := time.ParseDuration(value)
	if err != nil {
		return defaultValue, fmt.Errorf("config: %s must be a duration such as %q, got %q", key, "45m", value)
	}

	return d, nil
}
