package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
)

// DB owns the Postgres connection pool. The embedded *gorm.DB gives callers the
// full GORM API; sql is kept alongside it so the pool can be closed and pinged
// without re-deriving the handle and re-handling its error every time.
type DB struct {
	*gorm.DB

	sql *sql.DB
}

// OpenPostgres connects, configures the pool and verifies the connection is
// actually usable before returning.
//
// It deliberately does not migrate anything: the schema belongs to Atlas
// (migrations/), and AutoMigrate would quietly diverge from it — GORM guesses
// at column changes and never drops anything, so the two would disagree in ways
// nothing reports.
func OpenPostgres(ctx context.Context, cfg *config.Config, log *slog.Logger) (*DB, error) {
	gormCfg := &gorm.Config{
		// Turns driver-specific constraint violations into gorm.ErrDuplicatedKey
		// and friends. internal/api maps those onto a 409; without this the raw
		// pgx error falls through and surfaces as a 500.
		TranslateError: true,

		// The models write time.Now().UTC() throughout. This keeps the
		// timestamps GORM sets on its own consistent with them, instead of
		// mixing in the host's local zone.
		NowFunc: func() time.Time { return time.Now().UTC() },

		// GORM otherwise pings on Open with no deadline of its own. Ping below
		// instead, so an unreachable database fails in DBConnectTimeout rather
		// than hanging on whatever the driver defaults to.
		DisableAutomaticPing: true,

		Logger: gormlogger.New(
			slog.NewLogLogger(log.Handler(), slog.LevelWarn),
			gormlogger.Config{
				SlowThreshold: cfg.DBSlowQueryThreshold,
				LogLevel:      gormlogger.Warn,

				// ErrRecordNotFound is ordinary control flow here — the API
				// turns it into a 404 — not something worth a warning per miss.
				IgnoreRecordNotFoundError: true,

				// Log placeholders rather than bound values, so password hashes,
				// emails and IP addresses never reach the log through a slow
				// query line.
				ParameterizedQueries: true,
			},
		),
	}

	gormDB, err := gorm.Open(postgres.Open(cfg.DSN()), gormCfg)
	if err != nil {
		// cfg.DSN() carries the password, so it stays out of the error.
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("store: sql handle: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)

	db := &DB{DB: gormDB, sql: sqlDB}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DBConnectTimeout)
	defer cancel()

	if err := db.Ping(pingCtx); err != nil {
		// Opening succeeded, so the pool exists and would leak on this path.
		_ = db.Close()

		return nil, fmt.Errorf("store: connect to postgres at %s:%d/%s: %w",
			cfg.PostgresHost, cfg.PostgresPort, cfg.PostgresDatabaseName, err)
	}

	log.Info("postgres connected",
		"host", cfg.PostgresHost,
		"port", cfg.PostgresPort,
		"database", cfg.PostgresDatabaseName,
		"max_open_conns", cfg.DBMaxOpenConns,
	)

	return db, nil
}

// Ping checks that a connection can actually be obtained and used. The pool
// opens lazily, so nothing before this has proved the credentials are right or
// the host is reachable.
// SQL is the pool underneath.
//
// It exists for the two things that cannot go through the ORM: the schema tests,
// which have to read information_schema rather than trust a struct tag, and the
// migration to ent, which builds its client on this same pool so both can run side
// by side. Nothing else should reach for it — a query written here is a query no
// repository owns.
func (db *DB) SQL() *sql.DB {
	return db.sql
}

func (db *DB) Ping(ctx context.Context) error {
	if err := db.sql.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}

	return nil
}

// Close drains the pool. Callers should defer it for the lifetime of the
// process rather than per request — the pool is the thing meant to be reused.
func (db *DB) Close() error {
	if err := db.sql.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}

	return nil
}
