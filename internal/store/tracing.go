package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// Instrument registers callbacks that turn every statement into a span and a
// measurement.
//
// It lives here rather than in internal/telemetry because gorm does: the store owns
// the database driver, and TestGormStaysInsideTheStore is what keeps it that way.
// The entrypoint that serves traffic calls this; cmd/bootstrap and cmd/seed do not,
// because a one-shot command has nobody watching its spans.
//
// Written here rather than taken from a community package for one reason that
// matters and one that follows from it. The one that matters: what goes into
// db.statement. GORM can render a statement with its arguments substituted, and this
// codebase already refuses to log those — password hashes, addresses and IP
// addresses would otherwise reach the log through a slow-query line, and a span
// attribute is a log line with different retention. So the span carries the SQL with
// its placeholders intact, and never the values. The one that follows: forty lines
// against a stable API is cheaper than a dependency whose defaults have to be audited
// for the same question on every upgrade.
//
// The span name is "operation table" rather than the statement, because a span name
// is a series key: one per shape of query, not one per query.
func Instrument(db *DB, tel *telemetry.Telemetry) error {
	if !tel.Enabled {
		// Nothing to do, and the callbacks are not free: they allocate a span
		// context and read the clock on every statement.
		return nil
	}

	i := &gormInstrumentation{tel: tel}

	// The ent client is already wrapped; this is what makes the wrapper emit spans
	// rather than pass through. GORM still uses the callbacks below until those
	// repositories move.
	if db.entTrace != nil {
		db.entTrace.bind(tel)
	}

	// Each of GORM's processors has its own callback chain, and a statement only
	// passes through the one matching what it is doing.
	if err := registerPair(db.Callback().Create(), "gorm:create", "create", i.start("INSERT"), i.finish); err != nil {
		return err
	}

	if err := registerPair(db.Callback().Query(), "gorm:query", "query", i.start("SELECT"), i.finish); err != nil {
		return err
	}

	if err := registerPair(db.Callback().Update(), "gorm:update", "update", i.start("UPDATE"), i.finish); err != nil {
		return err
	}

	if err := registerPair(db.Callback().Delete(), "gorm:delete", "delete", i.start("DELETE"), i.finish); err != nil {
		return err
	}

	if err := registerPair(db.Callback().Row(), "gorm:row", "row", i.start("SQL"), i.finish); err != nil {
		return err
	}

	return registerPair(db.Callback().Raw(), "gorm:raw", "raw", i.start("SQL"), i.finish)
}

// registerPair hangs one before and one after callback around a GORM hook.
//
// The type parameters are what make this possible at all: db.Callback().Query()
// returns an unexported type, so no variable or interface here could name it — but
// inference can, which is why the six calls above are one helper rather than six
// copies of the same two Register lines.
func registerPair[C interface {
	Register(string, func(*gorm.DB)) error
}, P interface {
	Before(string) C
	After(string) C
}](processor P, hook, name string, before, after func(*gorm.DB)) error {
	if err := processor.Before(hook).Register("otel:before_"+name, before); err != nil {
		return fmt.Errorf("telemetry: gorm before %s: %w", name, err)
	}

	if err := processor.After(hook).Register("otel:after_"+name, after); err != nil {
		return fmt.Errorf("telemetry: gorm after %s: %w", name, err)
	}

	return nil
}

type gormInstrumentation struct {
	tel *telemetry.Telemetry
}

// The keys the two callbacks pass state through. GORM has no per-statement storage
// other than the settings map, and a package-private key keeps this out of anything
// else's way.
const (
	spanKey  = "otel:span"
	startKey = "otel:start"
)

func (i *gormInstrumentation) start(kind string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		ctx := db.Statement.Context
		if ctx == nil {
			return
		}

		// Client kind, because from this process's point of view the database is a
		// remote service — which is what makes the span line up under the request
		// that caused it rather than looking like work of its own.
		ctx, span := i.tel.Tracer.Start(ctx, kind+" "+db.Statement.Table,
			trace.WithSpanKind(trace.SpanKindClient))

		db.Statement.Context = ctx
		db.InstanceSet(spanKey, span)
		db.InstanceSet(startKey, time.Now())
	}
}

func (i *gormInstrumentation) finish(db *gorm.DB) {
	value, ok := db.InstanceGet(spanKey)
	if !ok {
		return
	}

	span, ok := value.(trace.Span)
	if !ok {
		return
	}

	defer span.End()

	sql := db.Statement.SQL.String()
	operation := operationOf(sql)
	table := tableOf(sql, db.Statement.Table)

	span.SetAttributes(
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.collection.name", table),
		attribute.String("db.operation.name", operation),
		// Placeholders, never values. See the note on InstrumentGORM.
		attribute.String("db.query.text", db.Statement.SQL.String()),
		attribute.Int64("db.response.returned_rows", db.Statement.RowsAffected),
	)

	// ErrRecordNotFound is ordinary control flow here — the API turns it into a 404
	// — so it is not an error on the span. Marking it would paint every miss red and
	// train everybody to ignore red.
	if db.Error != nil && !errors.Is(db.Error, gorm.ErrRecordNotFound) {
		span.RecordError(db.Error)
		span.SetStatus(codes.Error, "query failed")
	}

	attrs := metric.WithAttributes(
		attribute.String(telemetry.AttrOperation, operation),
		attribute.String(telemetry.AttrTable, table),
		attribute.Bool(telemetry.AttrError, db.Error != nil && !errors.Is(db.Error, gorm.ErrRecordNotFound)),
	)

	if i.tel.Metrics.DBQueries != nil {
		i.tel.Metrics.DBQueries.Add(db.Statement.Context, 1, attrs)
	}

	if started, ok := db.InstanceGet(startKey); ok {
		if at, ok := started.(time.Time); ok && i.tel.Metrics.DBDuration != nil {
			i.tel.Metrics.DBDuration.Record(db.Statement.Context, time.Since(at).Seconds(), attrs)
		}
	}
}

// tableOf finds the table the statement is about.
//
// GORM's Statement.Table is the right answer for the model-driven queries and the
// wrong one for the hand-written joins: Table("memberships AS m") records "m", so a
// metric grouped by it says the busiest table in the installation is called "m". The
// first identifier after FROM, INTO or UPDATE is what those queries are actually
// touching.
//
// Falls back to what GORM recorded, because a statement this cannot parse is better
// labelled by a short alias than by nothing.
func tableOf(sql, fallback string) string {
	upper := strings.ToUpper(sql)

	for _, keyword := range []string{" FROM ", " INTO ", "UPDATE "} {
		i := strings.Index(upper, keyword)
		if i < 0 {
			continue
		}

		rest := strings.TrimLeft(sql[i+len(keyword):], " (\"`")

		end := strings.IndexAny(rest, " \t\n(,;\"`")
		if end < 0 {
			end = len(rest)
		}

		if name := strings.TrimSpace(rest[:end]); name != "" {
			return name
		}
	}

	return fallback
}

// operationOf reads the verb off the front of the statement.
//
// GORM's own processor name would be close enough for the four CRUD chains but says
// "row" or "raw" for everything else, and the raw chain is where the interesting
// queries live in this codebase — the member listing, the owner count, the audit
// reader. The first word of the SQL is what those actually are.
func operationOf(sql string) string {
	trimmed := strings.TrimLeft(sql, " \t\n(")

	if i := strings.IndexAny(trimmed, " \t\n"); i > 0 {
		return strings.ToUpper(trimmed[:i])
	}

	if trimmed == "" {
		return "UNKNOWN"
	}

	return strings.ToUpper(trimmed)
}
