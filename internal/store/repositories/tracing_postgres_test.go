package repositories_test

import (
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// instrumented wires a real database with an in-memory span recorder.
func instrumented(t *testing.T) (*store.DB, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	tel := telemetry.Disabled()
	tel.Enabled = true
	tel.Tracer = provider.Tracer("test")

	db := testDB(t)

	if err := store.Instrument(db, tel); err != nil {
		t.Fatalf("Instrument() = %v", err)
	}

	return db, recorder
}

// TestAQueryBecomesASpan is the shape of the instrumentation: one span per
// statement, named after what it did rather than after the statement itself.
//
// A span name is a series key. Naming it after the SQL would make every distinct
// query its own name, which is the trace equivalent of a metric with unbounded
// cardinality.
func TestAQueryBecomesASpan(t *testing.T) {
	db, recorder := instrumented(t)
	repo := repositories.NewUser(db)

	u := newUser(t, repo)

	if _, err := repo.ByID(context.Background(), u.ID); err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatal("no spans recorded; the driver wrapper is not bound")
	}

	var found bool

	for _, span := range spans {
		if !strings.HasPrefix(span.Name(), "SELECT") {
			continue
		}

		found = true

		attrs := map[string]string{}
		for _, kv := range span.Attributes() {
			attrs[string(kv.Key)] = kv.Value.String()
		}

		if attrs["db.system.name"] != "postgresql" {
			t.Errorf("db.system.name = %q", attrs["db.system.name"])
		}

		if attrs["db.operation.name"] != "SELECT" {
			t.Errorf("db.operation.name = %q", attrs["db.operation.name"])
		}

		if attrs["db.collection.name"] != "users" {
			t.Errorf("db.collection.name = %q, want the real table rather than an alias",
				attrs["db.collection.name"])
		}
	}

	if !found {
		t.Errorf("no SELECT span among %d spans", len(spans))
	}
}

// TestASpanNeverCarriesQueryValues is the property that decides whether this
// instrumentation may exist at all.
//
// A span attribute is a log line with different retention and a different set of
// people who can read it. The store's own logger already refuses to log bound values
// — password hashes, addresses, IP addresses — and a trace that carried them would
// undo that decision quietly, from a package whose job nobody associates with
// privacy.
func TestASpanNeverCarriesQueryValues(t *testing.T) {
	db, recorder := instrumented(t)
	repo := repositories.NewUser(db)

	u := newUser(t, repo)

	if _, err := repo.ByEmail(context.Background(), u.Email); err != nil {
		t.Fatalf("ByEmail() = %v", err)
	}

	for _, span := range recorder.Ended() {
		for _, kv := range span.Attributes() {
			value := kv.Value.String()

			if strings.Contains(value, u.Email) {
				t.Errorf("%s carries the address in %s: %s", span.Name(), kv.Key, value)
			}

			if strings.Contains(value, u.PasswordHash) {
				t.Errorf("%s carries the password hash in %s", span.Name(), kv.Key)
			}
		}
	}

	// And the statement is there, with its placeholders, because a trace with no SQL
	// in it cannot answer which query was slow.
	var sawStatement bool

	for _, span := range recorder.Ended() {
		for _, kv := range span.Attributes() {
			if string(kv.Key) == "db.query.text" && strings.Contains(kv.Value.String(), "$1") {
				sawStatement = true
			}
		}
	}

	if !sawStatement {
		t.Error("no statement with placeholders on any span; the SQL is missing entirely")
	}
}

// TestARecordNotFoundIsNotAnError keeps the traces readable.
//
// A miss is zero rows from the driver, then a domain error. Marking those spans
// red trains everybody to ignore red.
func TestARecordNotFoundIsNotAnError(t *testing.T) {
	db, recorder := instrumented(t)
	repo := repositories.NewUser(db)

	if _, err := repo.ByEmail(context.Background(), "nobody@example.com"); err == nil {
		t.Fatal("ByEmail() found an account that does not exist")
	}

	for _, span := range recorder.Ended() {
		if span.Status().Code.String() == "Error" {
			t.Errorf("%s is marked as an error for a miss: %s", span.Name(), span.Status().Description)
		}
	}
}
