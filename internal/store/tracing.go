package store

import (
	"strings"

	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// Instrument binds telemetry onto the ent driver wrapper so every statement
// becomes a span and a measurement.
//
// It lives here rather than in internal/telemetry because the store owns the
// database driver, and TestEntStaysInsideTheStore is what keeps it that way.
// The entrypoint that serves traffic calls this; cmd/bootstrap and cmd/seed do
// not, because a one-shot command has nobody watching its spans.
//
// Bound values never enter the span. The query string already carries $1, $2, …;
// the args stay in the call and out of the attributes. A span attribute is a log
// line with different retention, and this codebase already refuses to log those
// — password hashes, addresses and IP addresses. TestASpanNeverCarriesQueryValues
// is the acceptance test.
//
// The span name is "operation table" rather than the statement, because a span
// name is a series key: one per shape of query, not one per query.
func Instrument(db *DB, tel *telemetry.Telemetry) error {
	if !tel.Enabled {
		// Nothing to do, and tracing is not free: it allocates a span context and
		// reads the clock on every statement.
		return nil
	}

	if db.entTrace != nil {
		db.entTrace.bind(tel)
	}

	return nil
}

// tableOf finds the table the statement is about.
//
// The first identifier after FROM, INTO or UPDATE is what the query is actually
// touching. A join written as `memberships AS m` would otherwise report the
// busiest table in the installation as "m".
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
