package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Env names the deployment environment. It gates the handful of behaviours that
// must differ between a laptop and production on purpose rather than by
// accident — log format, and whether the browsable API docs are published.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvProduction  Env = "production"
)

func (e Env) IsProduction() bool { return e == EnvProduction }

// Config holds the configuration values for the application.
type Config struct {
	Env     Env
	APIName string
	APIPort int

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

	cfg := &Config{
		Env:     Env(getEnv("ENV", string(EnvDevelopment))),
		APIName: getEnv("API_NAME", "Example"),
		APIPort: getInt("API_PORT", 4000),

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

	if c.PostgresDatabaseName == "" {
		errs = append(errs, errors.New("config: POSTGRES_DATABASE_NAME must not be empty"))
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

// Addr is the address the HTTP server binds to. The host is left empty so the
// listener accepts on every interface.
func (c *Config) Addr() string {
	return net.JoinHostPort("", strconv.Itoa(c.APIPort))
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
// API_PORT=4OOO start the server on 4000 with nothing to show for it.
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
