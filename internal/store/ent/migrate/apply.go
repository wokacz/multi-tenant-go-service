//go:build ignore

// Command apply creates the ent schema in an empty database.
//
// It exists to answer one question, which is the acceptance test for stage 2 of the
// migration off GORM (see ENT.md): does the schema described by internal/store/ent/schema
// produce the database that migrations/ produces? `task schema:dump:ent` runs this
// against a throwaway database, dumps it, and diffs the dump against
// schema.baseline.sql — so the answer is a reviewable list of differences rather than
// a reading of two descriptions.
//
// It is build-tagged out of the ordinary build. Automatic migration is not how this
// service manages its schema — Atlas and migrations/ are — and a binary that can
// rewrite the schema on start is not one that should be linkable by accident.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/migrate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "apply:", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("set DATABASE_URL")
	}

	// Two names, two meanings: pgx is the database/sql driver, postgres is the ent
	// dialect. entsql.Open takes one string for both, so it cannot express this — and
	// OpenDB on an existing pool is the same mechanism the service will use to run ent
	// and GORM side by side.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	defer func() {
		if err := db.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "apply: close:", err)
		}
	}()

	drv := entsql.OpenDB(dialect.Postgres, db)

	// WithForeignKeys keeps the constraints, which is most of what this comparison is
	// about; WithDropIndex/WithDropColumn are off because the target is empty and
	// nothing should be dropped.
	return migrate.Create(context.Background(), migrate.NewSchema(drv), migrate.Tables)
}
