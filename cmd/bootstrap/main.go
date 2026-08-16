// Command bootstrap grants the first owner.
//
// It exists because the authorization rules have no way to create their own
// starting point: making somebody an owner needs members.roles.assign, which
// nobody holds until an owner exists. Something outside the API has to break
// that circle, and it has to be a deliberate deployment step rather than a rule
// like "the first account to register wins" — with open registration, that is a
// race anybody on the internet can enter.
//
// It is idempotent. Running it again on an account that is already an owner
// changes nothing.
//
//	BOOTSTRAP_EMAIL=ada@example.com go run ./cmd/bootstrap
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/wokacz/go-example/internal/config"
	"github.com/wokacz/go-example/internal/domain/orgs"
	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store"
	"github.com/wokacz/go-example/internal/store/repositories"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err)
		os.Exit(1)
	}
}

func run() error {
	// The address comes from the environment by default so the value is not in
	// shell history on a production box; the flag is there for local use.
	email := flag.String("email", os.Getenv("BOOTSTRAP_EMAIL"),
		"address of an existing account to make owner of the default organization")
	platform := flag.Bool("platform-admin", true,
		"also grant the installation-wide administrator role")
	flag.Parse()

	if *email == "" {
		return errors.New("no address given; set BOOTSTRAP_EMAIL or pass -email")
	}

	time.Local = time.UTC

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := config.NewLogger(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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

	orgRepo := repositories.NewOrgs(db)
	users := user.NewService(repositories.NewUser(db), []byte(cfg.AuthResetSecret))
	service := orgs.NewService(orgRepo, orgRepo, users, orgRepo)

	// The account has to exist already. Creating one here would mean this
	// command could set a password, and a tool that can mint credentials is a
	// tool worth stealing.
	account, err := users.ByEmail(ctx, *email)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return fmt.Errorf("no account registered for %s; sign up first, then run this", *email)
		}

		return err
	}

	org, err := service.EnsureDefaultOrganization(ctx)
	if err != nil {
		return err
	}

	if err := service.PromoteToOwner(ctx, org.ID, account.ID, *platform); err != nil {
		return err
	}

	log.Info("bootstrapped",
		slog.String("email", *email),
		slog.String("organization", org.Slug),
		slog.Bool("platform_admin", *platform),
	)

	return nil
}
