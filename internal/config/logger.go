package config

import (
	"log/slog"
	"os"
	"strings"

	"github.com/wokacz/multi-tenant-go-service/internal/logging"
)

// NewLogger builds the process logger.
//
// Two readers, two formats. Production gets JSON, because a shipper parses it and
// nobody looks at alignment. Development gets the console handler: colour, a wall
// clock without the date, and attributes in a column, because the reader is a person
// watching a terminal and the thing they are doing is scanning.
//
// It lives here rather than in main so that main stays pure assembly — the choice of
// format is a configuration decision, and this is where configuration decisions are
// made.
func NewLogger(cfg *Config) *slog.Logger {
	out := os.Stdout

	if cfg.LogFormat == LogFormatJSON {
		return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: cfg.LogLevel}))
	}

	return slog.New(logging.NewConsoleHandler(out, logging.ConsoleOptions{
		Level:  cfg.LogLevel,
		Colour: logging.ShouldColour(cfg.LogColour, out),
	}))
}

// ParseLogLevel maps a name onto a level, falling back to info.
//
// Unknown names are not an error: a typo in LOG_LEVEL should leave the process
// running and logging, not refuse to start. Silence would be worse than a level
// somebody did not intend.
func ParseLogLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
