package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/wokacz/multi-tenant-go-service/internal/logging"
)

func newLogger(buf *bytes.Buffer, colour bool) *slog.Logger {
	return slog.New(logging.NewConsoleHandler(buf, logging.ConsoleOptions{
		Level:  slog.LevelDebug,
		Colour: colour,
	}))
}

// TestTheLineCarriesEverythingItWasGiven is the first thing to get wrong in a hand
// written handler: an attribute that silently does not appear.
func TestTheLineCarriesEverythingItWasGiven(t *testing.T) {
	var buf bytes.Buffer

	log := newLogger(&buf, false)
	log.Info("request", "status", 200, "path", "/v1/me", "duration_ms", 12)

	line := buf.String()

	for _, want := range []string{"INFO", "request", "status=200", "path=/v1/me", "duration_ms=12"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line is missing %q: %s", want, line)
		}
	}
}

// TestColourIsOptional covers the property that matters when output is piped: the
// same information, without escapes.
func TestColourIsOptional(t *testing.T) {
	var plain, coloured bytes.Buffer

	newLogger(&plain, false).Warn("slow query", "duration_ms", 812)
	newLogger(&coloured, true).Warn("slow query", "duration_ms", 812)

	if strings.Contains(plain.String(), "\033[") {
		t.Errorf("escapes in the uncoloured output: %q", plain.String())
	}

	if !strings.Contains(coloured.String(), "\033[") {
		t.Errorf("no escapes in the coloured output: %q", coloured.String())
	}

	// Same content either way, which is what stops colour becoming a second format
	// with its own bugs.
	if stripANSI(coloured.String()) != plain.String() {
		t.Errorf("colour changed the text:\n plain    %q\n stripped %q",
			plain.String(), stripANSI(coloured.String()))
	}
}

// TestWithAttrsAndGroupsSurvive is the pair every hand-written handler gets wrong.
// slog callers build sub-loggers, and attributes attached that way have to appear on
// every later line.
func TestWithAttrsAndGroupsSurvive(t *testing.T) {
	var buf bytes.Buffer

	log := newLogger(&buf, false).With("request_id", "abc123").WithGroup("db")
	log.Info("query", "table", "users", "rows", 3)

	line := buf.String()

	for _, want := range []string{"request_id=abc123", "db.table=users", "db.rows=3"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line is missing %q: %s", want, line)
		}
	}
}

// TestAValueWithSpacesIsQuoted keeps one line one record. An unquoted message with
// spaces in the middle of key=value pairs is where a log parser starts guessing.
func TestAValueWithSpacesIsQuoted(t *testing.T) {
	var buf bytes.Buffer

	newLogger(&buf, false).Error("mail failed", "error", "smtp: connection refused")

	if !strings.Contains(buf.String(), `error="smtp: connection refused"`) {
		t.Errorf("the value was not quoted: %s", buf.String())
	}
}

// TestTheLevelFiltersTheLine covers Enabled, which slog trusts.
func TestTheLevelFiltersTheLine(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(logging.NewConsoleHandler(&buf, logging.ConsoleOptions{Level: slog.LevelWarn}))

	log.Info("not this one")
	log.Warn("this one")

	if strings.Contains(buf.String(), "not this one") {
		t.Errorf("an info line got through a warn handler: %s", buf.String())
	}

	if !strings.Contains(buf.String(), "this one") {
		t.Errorf("the warn line is missing: %s", buf.String())
	}
}

// TestTheTraceIsOnTheLine is what ties a log line to a span.
//
// Without it, telemetry and logs are two systems somebody has to correlate by
// timestamp, which is the thing tracing exists to stop.
func TestTheTraceIsOnTheLine(t *testing.T) {
	var buf bytes.Buffer

	traceID, err := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	if err != nil {
		t.Fatal(err)
	}

	spanID, err := trace.SpanIDFromHex("b7ad6b7169203331")
	if err != nil {
		t.Fatal(err)
	}

	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  true,
	}))

	newLogger(&buf, false).InfoContext(ctx, "request")

	if !strings.Contains(buf.String(), "trace=0af76519") {
		t.Errorf("no trace on the line: %s", buf.String())
	}
}

// TestNoTraceWithoutASpan is the other half: a line outside a request must not grow
// a field made of zeroes.
func TestNoTraceWithoutASpan(t *testing.T) {
	var buf bytes.Buffer

	newLogger(&buf, false).Info("starting up")

	if strings.Contains(buf.String(), "trace=") {
		t.Errorf("a trace appeared without a span: %s", buf.String())
	}
}

// TestFanoutReachesEveryHandler covers the case the type exists for, including the
// record cloning: a handler is allowed to consume a record's attributes, and the
// second handler must still see them.
func TestFanoutReachesEveryHandler(t *testing.T) {
	var first, second bytes.Buffer

	handler := logging.Fanout(
		logging.NewConsoleHandler(&first, logging.ConsoleOptions{Level: slog.LevelDebug}),
		logging.NewConsoleHandler(&second, logging.ConsoleOptions{Level: slog.LevelDebug}),
	)

	slog.New(handler).With("request_id", "abc").Info("request", "status", 200)

	for name, buf := range map[string]*bytes.Buffer{"first": &first, "second": &second} {
		if !strings.Contains(buf.String(), "status=200") || !strings.Contains(buf.String(), "request_id=abc") {
			t.Errorf("%s handler got %q", name, buf.String())
		}
	}
}

// TestFanoutKeepsGoingAfterAFailure is why the errors are joined rather than
// returned on the first one: a broken exporter must not take the terminal with it.
func TestFanoutKeepsGoingAfterAFailure(t *testing.T) {
	var buf bytes.Buffer

	handler := logging.Fanout(
		failingHandler{},
		logging.NewConsoleHandler(&buf, logging.ConsoleOptions{Level: slog.LevelDebug}),
	)

	if err := handler.Handle(context.Background(), slog.Record{Message: "still written"}); err == nil {
		t.Error("Handle() reported success though a handler failed")
	}

	if !strings.Contains(buf.String(), "still written") {
		t.Errorf("the working handler was skipped: %q", buf.String())
	}
}

type failingHandler struct{}

func (failingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (failingHandler) Handle(context.Context, slog.Record) error { return errBroken }
func (f failingHandler) WithAttrs([]slog.Attr) slog.Handler      { return f }
func (f failingHandler) WithGroup(string) slog.Handler           { return f }

var errBroken = errBrokenType{}

type errBrokenType struct{}

func (errBrokenType) Error() string { return "broken" }

// stripANSI removes escape sequences, so a test can compare coloured output with
// plain output instead of asserting on specific codes.
func stripANSI(s string) string {
	var b strings.Builder

	for i := 0; i < len(s); {
		if s[i] == '\033' {
			for i < len(s) && s[i] != 'm' {
				i++
			}

			i++

			continue
		}

		b.WriteByte(s[i])
		i++
	}

	return b.String()
}
