package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wokacz/go-example/internal/api"
	"github.com/wokacz/go-example/internal/config"
	"github.com/wokacz/go-example/internal/store"
	"github.com/wokacz/go-example/internal/user"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// run assembles the process and hands control to the server. It makes no
// decisions of its own: anything resembling a rule belongs in the package that
// owns it, so that a second entrypoint — a worker, a CLI — composes the same
// pieces without inheriting choices made here.
//
// It is also why this is a function rather than main itself: os.Exit skips
// defers, and the database handle needs closing.
func run() error {
	// The models store UTC throughout, but pgx decodes timestamptz into the
	// local zone on the way back, so the same row would serialise as +02:00 on
	// a laptop and Z on a UTC server. Pinning the zone keeps timestamps in
	// responses identical wherever the process happens to run.
	time.Local = time.UTC

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := config.NewLogger(cfg)
	slog.SetDefault(log)

	// SIGTERM is what a container runtime sends before it kills the process, so
	// it has to be handled for the graceful drain to ever run in production.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.OpenPostgres(ctx, cfg, log)
	if err != nil {
		return err
	}

	// Closed after Run returns, which is after the server has drained — so
	// in-flight requests still have a working pool while they finish.
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("closing database", "error", err)
		}
	}()

	deps := api.Deps{
		DB:    db,
		Users: user.NewService(store.NewUserRepository(db)),
	}

	return api.NewServer(cfg, log, deps).Run(ctx)
}
