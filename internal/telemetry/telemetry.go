// Package telemetry sets up OpenTelemetry: traces, metrics and logs, with one
// switch and one shutdown.
//
// Everything here is off when no endpoint is configured, and off means the process
// behaves exactly as it did before it had any telemetry. That is deliberate: an
// observability stack the API cannot start without is a new way to take production
// down, and a laptop should not need a collector to run the tests.
//
// The three signals share a resource — the same service name, version and instance
// on all of them — because a trace, a metric and a log line that disagree about
// which process they came from cannot be joined, which is the whole point of
// sending them to one place.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/logging"
)

// Telemetry is what the process holds on to: the instruments it records through,
// and the shutdown that flushes them.
type Telemetry struct {
	// Enabled says whether anything is actually being exported. Callers do not need
	// to check it — the instruments are no-ops when it is false — but the log line
	// at startup should say which of the two situations this is.
	Enabled bool

	Tracer  trace.Tracer
	Meter   metric.Meter
	Metrics *Metrics

	logHandler slog.Handler
	shutdowns  []func(context.Context) error
}

// scope names this instrumentation in the collected data. It is the import path by
// convention, so a span's origin is findable without guessing.
const scope = "github.com/wokacz/multi-tenant-go-service"

// Setup builds the providers and registers them globally.
//
// The global registration is what lets otelhttp and any library instrumentation
// find them without being handed anything, and it is the one piece of global state
// this codebase accepts: the alternative is threading a provider through every
// constructor for the benefit of libraries that will read the global anyway.
func Setup(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Telemetry, error) {
	// The propagator is set even when nothing is exported. A request arriving with
	// a traceparent should keep its trace id in this process's logs whether or not
	// this process is the one sending spans anywhere.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// otel's own failures go to the process log rather than to stderr through its
	// default printer, so an exporter that cannot reach its collector is a line in
	// the same stream as everything else.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("telemetry error", "error", err)
	}))

	if cfg.OTLPEndpoint == "" {
		return Disabled(), nil
	}

	res, err := newResource(cfg)
	if err != nil {
		return nil, err
	}

	tel := &Telemetry{Enabled: true}

	if err := tel.setupTraces(ctx, cfg, res); err != nil {
		return nil, tel.abandon(ctx, err)
	}

	if err := tel.setupMetrics(ctx, cfg, res); err != nil {
		return nil, tel.abandon(ctx, err)
	}

	if err := tel.setupLogs(ctx, cfg, res); err != nil {
		return nil, tel.abandon(ctx, err)
	}

	tel.Tracer = otel.Tracer(scope)
	tel.Meter = otel.Meter(scope)

	metrics, err := NewMetrics(tel.Meter)
	if err != nil {
		return nil, tel.abandon(ctx, err)
	}

	tel.Metrics = metrics

	return tel, nil
}

// Disabled is the no-op telemetry: real instrument types that record nothing, so
// call sites never branch on whether telemetry exists.
//
// Exported because tests and any second entrypoint need one. A nil *Telemetry would
// be the other way to say this, and it would be a nil dereference on the first
// refused request instead.
func Disabled() *Telemetry {
	tracer := tracenoop.NewTracerProvider().Tracer(scope)
	meter := noop.NewMeterProvider().Meter(scope)

	metrics, err := NewMetrics(meter)
	if err != nil {
		// Creating instruments on a no-op meter cannot fail, and if it somehow did
		// the honest answer is instruments that discard rather than a dead process.
		metrics = &Metrics{}
	}

	return &Telemetry{Tracer: tracer, Meter: meter, Metrics: metrics}
}

// newResource describes this process. service.instance.id matters more than it
// looks: without it three replicas are one series, and a memory leak in one of them
// is a third of a leak in all of them.
func newResource(cfg *config.Config) (*resource.Resource, error) {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}

	// Merged with the SDK's default rather than replacing it, so the process keeps
	// telemetry.sdk.* alongside our own attributes. The two have to agree on a schema
	// version or Merge refuses — which is why the semconv import above is pinned to
	// the version the installed SDK's default resource uses, and why upgrading the
	// SDK means moving that import with it.
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.ServiceInstanceID(host),
		semconv.DeploymentEnvironmentNameKey.String(string(cfg.Env)),
		attribute.String("host.name", host),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}

	return res, nil
}

func (t *Telemetry) setupTraces(ctx context.Context, cfg *config.Config, res *resource.Resource) error {
	exporter, err := otlptracehttp.New(ctx, endpointOptions(cfg)...)
	if err != nil {
		return fmt.Errorf("telemetry: trace exporter: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		// Parent-based, so a sampling decision made upstream is respected: half a
		// trace is worse than none, because it looks like a request that stopped.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio))),
	)

	otel.SetTracerProvider(provider)
	t.shutdowns = append(t.shutdowns, provider.Shutdown)

	return nil
}

func (t *Telemetry) setupMetrics(ctx context.Context, cfg *config.Config, res *resource.Resource) error {
	exporter, err := otlpmetrichttp.New(ctx, metricEndpointOptions(cfg)...)
	if err != nil {
		return fmt.Errorf("telemetry: metric exporter: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(15*time.Second))),
	)

	otel.SetMeterProvider(provider)
	t.shutdowns = append(t.shutdowns, provider.Shutdown)

	return nil
}

func (t *Telemetry) setupLogs(ctx context.Context, cfg *config.Config, res *resource.Resource) error {
	exporter, err := otlploghttp.New(ctx, logEndpointOptions(cfg)...)
	if err != nil {
		return fmt.Errorf("telemetry: log exporter: %w", err)
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	global.SetLoggerProvider(provider)

	t.logHandler = otelslog.NewHandler(scope, otelslog.WithLoggerProvider(provider))
	t.shutdowns = append(t.shutdowns, provider.Shutdown)

	return nil
}

// Logger returns the logger the process should use from here on.
//
// With telemetry off it hands back what it was given. With telemetry on it fans the
// same records out to the console or JSON handler *and* to the collector, so one
// call writes both — rather than every call site remembering to do it twice, which
// is a thing no codebase manages for long.
func (t *Telemetry) Logger(base *slog.Logger) *slog.Logger {
	if t.logHandler == nil {
		return base
	}

	return slog.New(logging.Fanout(base.Handler(), t.logHandler))
}

// Shutdown flushes everything that is buffered.
//
// Every provider is shut down even if an earlier one fails, because a batch
// processor that is not flushed loses the spans describing whatever went wrong just
// before the process ended — which is exactly the interesting part.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error

	for _, shutdown := range t.shutdowns {
		if err := shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// abandon tears down what was already built when a later step fails, so a partial
// setup does not leave exporters running in a process that is about to report an
// error and exit.
func (t *Telemetry) abandon(ctx context.Context, cause error) error {
	if err := t.Shutdown(ctx); err != nil {
		return errors.Join(cause, err)
	}

	return cause
}

// The three exporters each take their own URL, and each one is the base endpoint
// with the signal's standard path on the end.
//
// This is the sharp edge in the SDK's API: WithEndpointURL takes the host *and the
// path* from what it is given, so passing the bare base — which is what
// OTEL_EXPORTER_OTLP_ENDPOINT means per the specification — sends every request to /
// and a collector answers 404. It looks like a working exporter with an unreachable
// collector, which is the most expensive kind of misconfiguration to read.
const (
	pathTraces  = "/v1/traces"
	pathMetrics = "/v1/metrics"
	pathLogs    = "/v1/logs"
)

// signalURL joins the base endpoint with a signal path.
//
// url.JoinPath rather than concatenation, so a base that already carries a prefix —
// a collector behind http://gateway/otlp — keeps it instead of losing it.
func signalURL(base, signal string) string {
	joined, err := url.JoinPath(base, signal)
	if err != nil {
		// Unparseable was already refused by config validation; falling back to the
		// base is better than an empty endpoint, and the exporter will say so.
		return base
	}

	return joined
}

func endpointOptions(cfg *config.Config) []otlptracehttp.Option {
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(signalURL(cfg.OTLPEndpoint, pathTraces))}
	if cfg.OTLPInsecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	return opts
}

func metricEndpointOptions(cfg *config.Config) []otlpmetrichttp.Option {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(signalURL(cfg.OTLPEndpoint, pathMetrics))}
	if cfg.OTLPInsecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}

	return opts
}

func logEndpointOptions(cfg *config.Config) []otlploghttp.Option {
	opts := []otlploghttp.Option{otlploghttp.WithEndpointURL(signalURL(cfg.OTLPEndpoint, pathLogs))}
	if cfg.OTLPInsecure {
		opts = append(opts, otlploghttp.WithInsecure())
	}

	return opts
}
