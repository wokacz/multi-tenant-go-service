package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"

	// pgx registers itself as database/sql driver "pgx". The DSN is a postgres://
	// URL; sql.Open is lazy, so an unreachable host fails at Ping below rather than
	// here, under DBConnectTimeout.
	_ "github.com/jackc/pgx/v5/stdlib"

	// The generated runtime wires the schema's defaults, hooks and interceptors into
	// the client. Without it soft delete silently is not soft delete — ent refuses
	// with "uninitialized interceptor", which is better than proceeding, but the
	// import is easy to lose in a refactor and impossible to notice by reading the
	// client's construction. It belongs here because this is where the client is
	// built; the ent package cannot import it itself without a cycle.
	_ "github.com/wokacz/multi-tenant-go-service/internal/store/ent/runtime"
)

// DB owns the Postgres connection pool and the ent client that runs on it.
type DB struct {
	sql *sql.DB

	ent *ent.Client

	// entTrace wraps the driver's statements in spans. Instrument binds telemetry
	// onto it after open, so OpenPostgres does not need to know whether this process
	// is exporting anything.
	entTrace *tracedDriver
}

// OpenPostgres connects, configures the pool and verifies the connection is
// actually usable before returning.
//
// It deliberately does not migrate anything: the schema belongs to the files in
// migrations/, and AutoMigrate would quietly diverge from them.
func OpenPostgres(ctx context.Context, cfg *config.Config, log *slog.Logger) (*DB, error) {
	sqlDB, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		// cfg.DSN() carries the password, so it stays out of the error.
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)

	entTrace := newTracedDriver(entsql.OpenDB(dialect.Postgres, sqlDB))
	db := &DB{
		sql:      sqlDB,
		ent:      ent.NewClient(ent.Driver(entTrace)),
		entTrace: entTrace,
	}

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

// Ent is the client on this pool.
//
// It returns the client rather than exposing the field so that nothing outside the
// store can hold one: ent's generated types are a persistence detail, and
// TestEntStaysInsideTheStore is what keeps them there.
func (db *DB) Ent() *ent.Client {
	return db.ent
}

// SQL is the pool underneath.
//
// It exists for the two things that cannot go through the ORM: the schema tests,
// which have to read information_schema rather than trust a struct, and the handful
// of queries whose shape the client cannot express (a correlated subquery over four
// tables, a scan that lands in a DTO). Nothing else should reach for it — a query
// written here is a query no repository owns.
func (db *DB) SQL() *sql.DB {
	return db.sql
}

// Ping checks that a connection can actually be obtained and used. The pool
// opens lazily, so nothing before this has proved the credentials are right or
// the host is reachable.
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
