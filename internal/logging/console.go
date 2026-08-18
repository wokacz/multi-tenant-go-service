// Package logging holds the process's log handlers: the one a person reads in a
// terminal, and the fan-out that lets the same record also leave the process.
//
// It is separate from internal/config because config decides *which* handler to
// use, and that is a different job from writing one.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// ANSI escapes, written out rather than pulled from a library. There are eight of
// them and no library would make this shorter.
const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
	ansiCyan   = "\033[36m"
	ansiGrey   = "\033[90m"
)

// ConsoleOptions configures the handler.
type ConsoleOptions struct {
	Level slog.Leveler

	// Colour, when false, writes the same layout without escapes. Ordinary
	// grep-ability matters more than colour when the output is being piped, and
	// escape codes in a CI log are noise nobody asked for.
	Colour bool

	// TimeFormat defaults to a wall clock with milliseconds. The date is left out
	// on purpose: a developer watching a terminal knows what day it is, and the
	// column costs eleven characters on every line.
	TimeFormat string
}

// ConsoleHandler writes one readable line per record.
//
// The layout is fixed-width on the left so the eye can scan down it:
//
//	08:25:48.773 INFO  request           status=200 duration_ms=12 path=/v1/me
//	08:25:49.102 WARN  slow query        duration_ms=812
//	08:25:49.400 ERROR invitation mail failed  error="smtp: connection refused"
//
// The message is padded to a column rather than followed by a separator, because
// attributes are what one actually reads once the message is familiar.
//
// It is not a production handler and does not try to be: production gets JSON,
// where a shipper parses fields and nobody looks at alignment.
type ConsoleHandler struct {
	opts   ConsoleOptions
	out    io.Writer
	mu     *sync.Mutex
	groups []string
	attrs  []slog.Attr
}

// messageColumn is where attributes start. Wide enough for the messages this
// process actually writes, and a longer one simply pushes the attributes right
// rather than wrapping.
const messageColumn = 24

var _ slog.Handler = (*ConsoleHandler)(nil)

// NewConsoleHandler builds a handler writing to out.
func NewConsoleHandler(out io.Writer, opts ConsoleOptions) *ConsoleHandler {
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}

	if opts.TimeFormat == "" {
		opts.TimeFormat = "15:04:05.000"
	}

	return &ConsoleHandler{opts: opts, out: out, mu: &sync.Mutex{}}
}

// ShouldColour decides whether escapes are wanted.
//
// Three inputs, in order: an explicit choice, then NO_COLOR — which is a
// convention with a specification and worth honouring — then whether the writer is
// a terminal at all. A pipe gets no colour, which is what makes `task run | tee`
// produce a readable file.
func ShouldColour(choice string, out io.Writer) bool {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "always":
		return true
	case "never":
		return false
	}

	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}

	file, ok := out.(*os.File)
	if !ok {
		return false
	}

	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func (h *ConsoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	clone := h.clone()
	clone.attrs = append(clone.attrs, attrs...)

	return clone
}

func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	clone := h.clone()
	clone.groups = append(clone.groups, name)

	return clone
}

func (h *ConsoleHandler) clone() *ConsoleHandler {
	return &ConsoleHandler{
		opts:   h.opts,
		out:    h.out,
		mu:     h.mu,
		groups: slices.Clone(h.groups),
		attrs:  slices.Clone(h.attrs),
	}
}

func (h *ConsoleHandler) Handle(ctx context.Context, record slog.Record) error {
	var b strings.Builder

	stamp := record.Time
	if stamp.IsZero() {
		stamp = time.Now()
	}

	b.WriteString(h.paint(ansiGrey, stamp.Format(h.opts.TimeFormat)))
	b.WriteByte(' ')
	b.WriteString(h.paint(levelColour(record.Level), fmt.Sprintf("%-5s", levelLabel(record.Level))))
	b.WriteByte(' ')

	message := record.Message
	b.WriteString(h.paint(ansiBold, message))

	// Pad to the attribute column, and give a long message a single space rather
	// than truncating it: a message that runs over is rarer than one somebody needs
	// to read in full.
	if pad := messageColumn - len([]rune(message)); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	} else {
		b.WriteByte(' ')
	}

	// The trace is written first when there is one, because that is the field
	// somebody copies into a trace viewer. Shortened, because sixteen bytes of hex
	// is not something anybody reads — it is something they paste, and the first
	// eight are enough to recognise it in a list.
	if span := trace.SpanContextFromContext(ctx); span.IsValid() {
		id := span.TraceID().String()
		h.writeAttr(&b, "trace", id[:8])
	}

	for _, attr := range h.attrs {
		h.writeGrouped(&b, h.groups, attr)
	}

	record.Attrs(func(attr slog.Attr) bool {
		h.writeGrouped(&b, h.groups, attr)

		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := io.WriteString(h.out, b.String())

	return err
}

// writeGrouped flattens a group into dotted keys, which is what makes a nested
// attribute greppable on one line.
func (h *ConsoleHandler) writeGrouped(b *strings.Builder, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()

	if attr.Value.Kind() == slog.KindGroup {
		nested := attr.Value.Group()
		if len(nested) == 0 {
			return
		}

		inner := groups
		if attr.Key != "" {
			inner = append(slices.Clone(groups), attr.Key)
		}

		for _, sub := range nested {
			h.writeGrouped(b, inner, sub)
		}

		return
	}

	if attr.Equal(slog.Attr{}) {
		return
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}

	h.writeAttr(b, key, attr.Value.String())
}

// writeAttr writes one key=value pair, quoting the value when it would otherwise
// be ambiguous.
func (h *ConsoleHandler) writeAttr(b *strings.Builder, key, value string) {
	colour := ansiCyan
	if key == "error" || key == "err" {
		// The one attribute worth finding without reading: an error is why the line
		// is on the screen at all.
		colour = ansiRed
	}

	b.WriteString(h.paint(ansiDim, key))
	b.WriteString(h.paint(ansiDim, "="))

	if needsQuoting(value) {
		value = strconv.Quote(value)
	}

	b.WriteString(h.paint(colour, value))
	b.WriteByte(' ')
}

func needsQuoting(value string) bool {
	if value == "" {
		return true
	}

	return strings.ContainsAny(value, " \t\n\"=")
}

func (h *ConsoleHandler) paint(colour, text string) string {
	if !h.opts.Colour || colour == "" {
		return text
	}

	return colour + text + ansiReset
}

func levelLabel(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

func levelColour(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return ansiBlue
	case level < slog.LevelWarn:
		return ansiGreen
	case level < slog.LevelError:
		return ansiYellow
	default:
		return ansiRed
	}
}
