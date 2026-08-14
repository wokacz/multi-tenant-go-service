package config

import (
	"log/slog"
	"os"
)

// NewLogger builds the process logger: JSON in production, where a log shipper
// parses it, and plain text in development, where a human reads it.
//
// It lives here rather than in main so that main stays pure assembly — the
// choice of format is a configuration decision, and this is where configuration
// decisions are made.
func NewLogger(cfg *Config) *slog.Logger {
	if cfg.Env.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}

	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
