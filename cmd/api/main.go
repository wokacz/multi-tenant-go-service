package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/filestore"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
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

	// Telemetry before anything else that might want to record: with no endpoint
	// configured this is a no-op and the process behaves exactly as it did before it
	// had any, which is what keeps a laptop and the test suite from needing a
	// collector.
	tel, err := telemetry.Setup(ctx, cfg, log)
	if err != nil {
		return err
	}

	// The same records now go to the terminal and to the collector. Set as the
	// default too, so a library reaching for slog.Default lands in the same place.
	log = tel.Logger(log)
	slog.SetDefault(log)

	defer func() {
		// Its own timeout: ctx is already cancelled by the time this runs — that is
		// what ended the server — and a batch processor given a dead context drops
		// exactly the spans describing the shutdown.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := tel.Shutdown(flushCtx); err != nil {
			log.Error("flushing telemetry", "error", err)
		}
	}()

	log.Info("telemetry",
		slog.Bool("enabled", tel.Enabled),
		slog.String("endpoint", cfg.OTLPEndpoint),
		slog.String("service", cfg.ServiceName),
		slog.Float64("trace_sample_ratio", cfg.TraceSampleRatio),
	)

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

	tokens, err := auth.NewSigner(cfg.AuthTokenSecret, cfg.AuthTokenTTL, cfg.AuthTokenIssuer)
	if err != nil {
		return err
	}

	// AuthResetSecret peppers reset-code HMACs. It is not AuthTokenSecret:
	// rotating session tokens must not rewrite hashes of codes already emailed.
	// Registered on the pool rather than passed to OpenPostgres, so the one-shot
	// commands that share that function do not pay for callbacks nobody watches.
	if err := store.Instrument(db, tel); err != nil {
		return err
	}

	orgRepo := repositories.NewOrgs(db)
	userRepo := repositories.NewUser(db)
	users := user.NewService(userRepo, []byte(cfg.AuthResetSecret))
	authzService := authz.NewService(repositories.NewAuthz(db))

	blobs, err := filestore.NewLocal(cfg.FilesStoragePath)
	if err != nil {
		return err
	}

	scanner := files.NopScanner()
	if cfg.FilesScanMode != config.ScanOff {
		scanner = filestore.NewClamAV(cfg.FilesClamAVAddr, cfg.FilesClamAVTimeout)
	}

	fileRepo := repositories.NewFiles(db)
	fileService, err := files.NewService(fileRepo, blobs, fileRepo, scanner, files.Settings{
		MaxBytes:              cfg.FilesMaxBytes,
		AvatarMaxBytes:        cfg.FilesAvatarMaxBytes,
		AllowedTypes:          cfg.FilesAllowedTypes,
		RequireDeclaredMatch:  cfg.FilesRequireDeclaredMatch,
		RequireExtensionMatch: cfg.FilesRequireExtensionMatch,
		BlockExecutables:      cfg.FilesBlockExecutables,
		EncryptionKey:         cfg.FilesEncryptionKey,
		ScanRequired:          cfg.FilesScanMode == config.ScanRequired,
	})
	if err != nil {
		return err
	}

	deps := api.Deps{
		DB:     db,
		Users:  users,
		Tokens: tokens,
		// Wrapped, so a message that could not be sent is a counter and a span
		// rather than only a log line the handlers deliberately swallow.
		Mail:      telemetry.MeteredMailer(mail.New(cfg, log), tel),
		Authz:     authzService,
		Snapshots: authzService,
		Orgs:      orgs.NewService(orgRepo, orgRepo, orgRepo),
		Audit:     audit.NewService(orgRepo, orgRepo),
		Files:     fileService,
		Telemetry: tel,
	}

	return api.NewServer(cfg, log, deps).Run(ctx)
}
