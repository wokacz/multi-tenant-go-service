// Command seed fills a development database with data worth testing against.
//
// A documented cast of accounts, a hundred more to page through, and organizations
// in each of the shapes the rules care about — including one nobody can administer,
// which the API cannot produce at all.
//
//	task seed            # add what is missing
//	task seed -- -reset  # remove the seed data first
//
// It refuses to run with ENV=production, refuses to run without -yes, and refuses a
// database that already holds accounts it did not create. See internal/seed/guard.go
// for why those three and not one.
//
// Every account gets the same password, printed at the end and written down in
// docs/guides/009_seed_data.md. Hashing uses bcrypt's minimum cost: a hundred and
// twenty accounts at the production cost is minutes of waiting for data nobody is
// protecting.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/seed"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run() error {
	yes := flag.Bool("yes", false, "required; without it nothing is written")
	force := flag.Bool("force", false,
		"seed even though the database holds accounts from outside the seed domain")
	reset := flag.Bool("reset", false, "delete the seed data first, then seed again")
	// A fixed default, so two runs produce the same people. Change it when you want
	// a different hundred rather than the same hundred again.
	randomSeed := flag.Uint64("seed", 20260818, "seed for the random half of the data")
	only := flag.String("only", "", "comma-separated parts to run (default: all)")
	skip := flag.String("skip", "", "comma-separated parts to skip")
	flag.Parse()

	time.Local = time.UTC

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := config.NewLogger(cfg)

	// Generous, because this writes a few hundred rows and a slow laptop with a
	// containerised database is the normal case.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := store.OpenPostgres(ctx, cfg, log)
	if err != nil {
		return err
	}

	defer func() {
		if err := db.Close(); err != nil {
			log.Error("closing database", "error", err)
		}
	}()

	userRepo := repositories.NewUser(db)

	if err := seed.Guard(ctx, cfg, userRepo, *yes, *force); err != nil {
		return err
	}

	// bcrypt.MinCost, and only here. The API's own service is constructed with the
	// default cost; this one exists for the length of this process and hashes one
	// password a hundred and twenty times.
	users := user.NewService(userRepo, []byte(cfg.AuthResetSecret), user.WithBcryptCost(bcrypt.MinCost))
	orgRepo := repositories.NewOrgs(db)
	service := orgs.NewService(orgRepo, orgRepo, orgRepo)

	rng := rand.New(rand.NewPCG(*randomSeed, *randomSeed))
	world := seed.NewWorld(users, service, orgRepo, orgRepo, userRepo, rng, log)

	if *reset {
		if err := seed.Reset(ctx, world); err != nil {
			return err
		}
	}

	if err := seed.Run(ctx, world, seed.Plan(), split(*only), split(*skip)); err != nil {
		return err
	}

	log.Info("seeded",
		slog.String("password", seed.Password),
		slog.String("domain", seed.Domain),
		slog.String("documented_in", "docs/guides/009_seed_data.md"))

	return nil
}

// split turns a comma-separated flag into a list, dropping empties so that -only=""
// means "no filter" rather than "run the part named empty string".
func split(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}
