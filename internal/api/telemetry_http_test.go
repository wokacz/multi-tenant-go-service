package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// collected reads every metric out of a manual reader, flattened to
// "name{attr=value}" -> value, which is what makes an assertion in a test readable.
func collected(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	out := map[string]int64{}

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}

			for _, point := range sum.DataPoints {
				var attrs []string

				for _, kv := range point.Attributes.ToSlice() {
					attrs = append(attrs, string(kv.Key)+"="+kv.Value.String())
				}

				out[m.Name+"{"+strings.Join(attrs, ",")+"}"] += point.Value
			}
		}
	}

	return out
}

// newMeteredFixture is newAuthzFixture with a real meter behind it, reading into a
// manual reader instead of an exporter.
func newMeteredFixture(t *testing.T) (*authzFixture, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	metrics, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v", err)
	}

	tel := telemetry.Disabled()
	tel.Metrics = metrics

	f := newAuthzFixtureWith(t, tel)

	return f, reader
}

// TestAFailedSignInIsCounted is the metric that exists because the response cannot
// carry the information.
//
// A wrong password and an unknown address share one error on purpose, so nobody can
// use the status to discover who has an account. That makes the ratio between them
// invisible from outside — and a number is the only place left to see it.
func TestAFailedSignInIsCounted(t *testing.T) {
	f, reader := newMeteredFixture(t)

	body := `{"email":"` + testEmail + `","password":"not-the-password"}`
	if code := do(t, f.server.http.Handler,
		request(t, http.MethodPost, "/v1/sessions", body)).Code; code != http.StatusUnauthorized {
		t.Fatalf("sign-in = %d, want 401", code)
	}

	metrics := collected(t, reader)

	if got := metrics["auth.sign_ins{outcome=bad_credentials}"]; got != 1 {
		t.Errorf("bad_credentials = %d, want 1; collected %v", got, metrics)
	}
}

// TestAGrantedSignInIsCounted covers the other side, so the two are comparable.
//
// The delta rather than the total: the fixture signs Ada in while building itself, so
// asserting on an absolute value would be asserting on how the fixture works.
func TestAGrantedSignInIsCounted(t *testing.T) {
	f, reader := newMeteredFixture(t)

	const key = "auth.sign_ins{outcome=granted}"

	before := collected(t, reader)[key]

	signInAda(t, f.server, "", http.StatusCreated)

	if got := collected(t, reader)[key] - before; got != 1 {
		t.Errorf("granted went up by %d, want 1", got)
	}
}

// TestADenialIsCountedByPermission is what answers "which permission is stopping
// people", which is a question about role configuration rather than about code.
func TestADenialIsCountedByPermission(t *testing.T) {
	f, reader := newMeteredFixture(t)

	// A member with no roles: reading the organization needs organization.read.
	if got := f.getOrg(t); got != http.StatusForbidden {
		t.Fatalf("reading the organization = %d, want 403", got)
	}

	want := "authz.denials{permission=organization.read,scope=organization}"
	if got := collected(t, reader)[want]; got != 1 {
		t.Errorf("%s = %d, want 1; collected %v", want, got, collected(t, reader))
	}
}

// TestRateLimitingIsCountedByRouteNotPath keeps the metric's cardinality bounded. An
// organization id in an attribute is a new series per tenant, which is how a metrics
// bill becomes a surprise.
func TestRateLimitingIsCountedByRouteNotPath(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	metrics, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v", err)
	}

	tel := telemetry.Disabled()
	tel.Metrics = metrics

	const perMinute = 1

	s, _ := newTestAPIConfigTel(t, &capturingMailer{}, memory.NewUsers(), tel, func(cfg *config.Config) {
		cfg.InvitePerMinute = perMinute
	})

	path := "/v1/orgs/018f0000-0000-7000-8000-000000000000/members"
	body := `{"email":"bo@example.com","role_ids":[]}`

	for range perMinute + 1 {
		do(t, s.http.Handler, withDeviceToken(request(t, http.MethodPost, path, body)))
	}

	want := "http.rate_limited{route=/v1/orgs/{orgID}/members}"
	if got := collected(t, reader)[want]; got != 1 {
		t.Errorf("%s = %d, want 1; collected %v", want, got, collected(t, reader))
	}
}
