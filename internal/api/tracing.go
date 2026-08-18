package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// tracing turns every request into a span, with the standard server metrics that
// come with it.
//
// otelhttp rather than a hand-rolled middleware: the HTTP semantic conventions are a
// moving target maintained by people who read the specification, and getting
// http.route or the duration histogram's buckets subtly wrong produces dashboards
// that look right and compare badly across services.
//
// What it cannot do on its own is name the span after the route: it runs before chi
// matches one, so left alone every span is called "GET /v1/orgs/018f.../members"
// with the id in it — which makes a hundred organizations a hundred series and the
// route impossible to aggregate. The inner middleware fixes that once the pattern is
// known.
func (s *Server) tracing(next http.Handler) http.Handler {
	handler := otelhttp.NewHandler(
		s.routeSpanName(next),
		// The outer name only shows for a request that matched nothing; anything
		// routed is renamed below.
		"http",
		otelhttp.WithFilter(func(r *http.Request) bool {
			// The health endpoint is polled by a container runtime every few
			// seconds forever. Tracing it buys nothing and would be most of the
			// spans in a quiet installation.
			return r.URL.Path != healthPath
		}),
	)

	return handler
}

// routeSpanName renames the span once chi knows which route matched, and hangs the
// request id on it.
//
// The id is the join between a span and the log lines from the same request: the log
// carries request_id and the trace carries it too, so either one finds the other.
func (s *Server) routeSpanName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		// The labeler is how a route reaches otelhttp's *metrics*. Setting the span
		// attribute is not enough: the duration histogram is recorded after this
		// handler returns, from attributes collected here, and without the route
		// every request is one undifferentiated series — which is a latency graph
		// that cannot tell a slow report from a fast health check.
		if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			if pattern := chi.RouteContext(r.Context()).RoutePattern(); pattern != "" {
				labeler.Add(semconv.HTTPRoute(pattern))
			}
		}

		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			return
		}

		if id := middleware.GetReqID(r.Context()); id != "" {
			span.SetAttributes(attribute.String("http.request.id", id))
		}

		// Read after the handler, because that is when chi has matched.
		pattern := chi.RouteContext(r.Context()).RoutePattern()
		if pattern == "" {
			return
		}

		span.SetName(r.Method + " " + pattern)
		span.SetAttributes(semconv.HTTPRoute(pattern))
	})
}
