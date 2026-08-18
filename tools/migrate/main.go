// Command migrate writes a versioned migration from the difference between the ent
// schema and migrations/.
//
// This is ent's own versioned-migration flow rather than Atlas reading a description
// of the schema from outside: ent knows its schema, replays the migration directory
// onto a throwaway database to learn the current state, and asks Atlas to render the
// difference as SQL. Replaying rather than connecting to a real database is what makes
// it safe to run anywhere and correct when several deployments are at different
// versions.
//
//	task migrate:diff NAME=add_something
//
// The directory stays at migrations/ — the same one Atlas, Compose and task migrate
// have always used — because where a project keeps its migrations is the project's
// convention, not the ORM's.
//
// It is a real package under tools/ rather than a file behind //go:build ignore, and
// that is not cosmetic: an ignored file is invisible to `go mod tidy`, which would
// then drop ariga.io/atlas from go.mod and leave this unable to build.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	atlas "ariga.io/atlas/sql/migrate"
	_ "ariga.io/atlas/sql/postgres"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/migrate"
)

// dir is relative to the repository root, which is where the task runs this from.
const dir = "migrations"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

// Atlas opens its dev database through database/sql under the scheme's own name, and
// pgx registers itself as "pgx". One alias rather than a second driver in the module.
func init() {
	sql.Register("postgres", stdlib.GetDefaultDriver())
}

func run() error {
	flag.Parse()

	if flag.NArg() != 1 {
		return fmt.Errorf("usage: go run ./tools/migrate <name>")
	}

	// The dev database Atlas computes against, supplied by the task that runs this: a
	// throwaway container on a port of its own, torn down afterwards. The Atlas CLI's
	// docker:// URL is not available from the SDK, and pointing this at a real database
	// would mean a migration whose contents depend on which machine generated it.
	devURL := os.Getenv("ATLAS_DEV_URL")
	if devURL == "" {
		return fmt.Errorf("set ATLAS_DEV_URL to a throwaway Postgres (task migrate:diff does)")
	}

	local, err := atlas.NewLocalDir(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}

	opts := []schema.MigrateOption{
		schema.WithDir(local),
		schema.WithDialect(dialect.Postgres),
		schema.WithFormatter(atlas.DefaultFormatter),
		// Replay the directory to learn the current state, rather than reading a live
		// database. A migration generated against whatever happens to be running is a
		// migration that depends on which laptop generated it.
		schema.WithMigrationMode(schema.ModeReplay),
		// The one thing this must never do on its own: dropping a column or an index
		// is a decision, and a generator that makes it quietly will eventually make it
		// on the wrong branch.
		schema.WithDropColumn(false),
		schema.WithDropIndex(false),
	}

	return migrate.NamedDiff(context.Background(), devURL, flag.Arg(0), opts...)
}
