package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

const testOrigin = "https://app.example.com"

// corsServer builds a server that allows testOrigin.
func corsServer(t *testing.T) *Server {
	t.Helper()

	server, _ := newTestAPIConfig(t, &capturingMailer{}, memory.NewUsers(), func(cfg *config.Config) {
		cfg.CORSAllowedOrigins = []string{testOrigin}
	})

	return server
}

func preflight(t *testing.T, method, path, origin string) *http.Request {
	t.Helper()

	req := request(t, http.MethodOptions, path, "")
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)

	return req
}

// TestCORSIsAbsentUntilConfigured is the default a deployment gets for free.
//
// An API with no browser client should not be answering cross-origin requests,
// and the absence of the grant is the refusal — so an unconfigured server must
// not even acknowledge that it looked at Origin.
func TestCORSIsAbsentUntilConfigured(t *testing.T) {
	server, _ := newTestServer(t)

	req := request(t, http.MethodGet, "/health", "")
	req.Header.Set("Origin", testOrigin)

	rec := do(t, server.http.Handler, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want it absent", got)
	}

	if got := rec.Header().Values("Vary"); slicesContainsFold(got, "Origin") {
		t.Errorf("Vary = %v, want no Origin — nothing varies by origin when none are allowed", got)
	}

	// The preflight is not short-circuited either, so it falls through to the
	// router, which has no OPTIONS route.
	pre := do(t, server.http.Handler, preflight(t, http.MethodGet, "/health", testOrigin))
	if pre.Code != http.StatusMethodNotAllowed {
		t.Errorf("preflight without configuration = %d, want 405", pre.Code)
	}
}

func TestCORSAnswersAConfiguredOrigin(t *testing.T) {
	server := corsServer(t)

	req := request(t, http.MethodGet, "/health", "")
	req.Header.Set("Origin", testOrigin)

	rec := do(t, server.http.Handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.Bytes())
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, testOrigin)
	}

	// Vary must be added on both the granted and the ungranted answer, or a
	// shared cache hands one origin's response to another.
	if got := rec.Header().Values("Vary"); !slicesContainsFold(got, "Origin") {
		t.Errorf("Vary = %v, want it to include Origin", got)
	}

	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	for _, header := range corsExposedHeaders {
		if !strings.Contains(exposed, header) {
			t.Errorf("Access-Control-Expose-Headers = %q, missing %s", exposed, header)
		}
	}
}

// TestCORSLeavesAnUnknownOriginWithoutTheHeader records the deliberate choice not
// to refuse server-side.
//
// The browser is what enforces this. Answering 403 would break a non-browser
// caller that happens to send an Origin and would change nothing for an attacker,
// because curl ignores the header either way.
func TestCORSLeavesAnUnknownOriginWithoutTheHeader(t *testing.T) {
	server := corsServer(t)

	req := request(t, http.MethodGet, "/health", "")
	req.Header.Set("Origin", "https://evil.example.com")

	rec := do(t, server.http.Handler, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — the request is answered, just not granted", rec.Code)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want it absent for an origin not on the list", got)
	}

	if got := rec.Header().Values("Vary"); !slicesContainsFold(got, "Origin") {
		t.Errorf("Vary = %v, want Origin even when the origin is refused", got)
	}
}

func TestCORSPreflightIsAnsweredWithoutRouting(t *testing.T) {
	server := corsServer(t)

	// A path that no operation registers: reaching the router at all would be a
	// 404, so a 204 proves the preflight never got there.
	rec := do(t, server.http.Handler, preflight(t, http.MethodPost, "/v1/orgs/x/members", testOrigin))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204; body %s", rec.Code, rec.Body.Bytes())
	}

	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != corsAllowedMethods {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, corsAllowedMethods)
	}

	if got := rec.Header().Get("Access-Control-Max-Age"); got != corsMaxAge {
		t.Errorf("Access-Control-Max-Age = %q, want %q", got, corsMaxAge)
	}

	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	for _, header := range corsAllowedHeaders {
		if !strings.Contains(allowed, header) {
			t.Errorf("Access-Control-Allow-Headers = %q, missing %s", allowed, header)
		}
	}

	// An unknown origin is still answered, and still gets no grant.
	bad := do(t, server.http.Handler, preflight(t, http.MethodPost, "/v1/sessions", "https://evil.example.com"))
	if bad.Code != http.StatusNoContent {
		t.Errorf("preflight from an unknown origin = %d, want 204", bad.Code)
	}

	if got := bad.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Access-Control-Allow-Methods = %q, want it absent for an unknown origin", got)
	}
}

// TestCORSPreflightCostsNoRateLimitToken keeps a browser client from
// rate-limiting itself just by loading.
//
// Two things guarantee it today — every case in rateLimit is gated on POST, and
// cors short-circuits before the limiter runs — so this pins the property rather
// than one of the mechanisms.
func TestCORSPreflightCostsNoRateLimitToken(t *testing.T) {
	server, _ := newTestAPIConfig(t, &capturingMailer{}, memory.NewUsers(), func(cfg *config.Config) {
		cfg.CORSAllowedOrigins = []string{testOrigin}
		cfg.LoginPerMinute = 1
	})

	for range 3 {
		rec := do(t, server.http.Handler, preflight(t, http.MethodPost, "/v1/sessions", testOrigin))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight = %d, want 204", rec.Code)
		}
	}

	// The one token in the budget is still there: this fails on the credentials,
	// not on the limit.
	rec := do(t, server.http.Handler, withDeviceToken(
		request(t, http.MethodPost, "/v1/sessions", `{"email":"nobody@example.com","password":"twelve-chars"}`)))
	if rec.Code == http.StatusTooManyRequests {
		t.Error("the sign-in budget was spent by preflights")
	}
}

// TestCORSAllowsEveryHeaderTheAPIDeclares walks the OpenAPI document so that
// adding a header parameter and forgetting corsAllowedHeaders cannot pass review.
//
// That mistake is invisible everywhere except a browser, where the request is
// blocked before it is sent and the server logs nothing at all.
func TestCORSAllowsEveryHeaderTheAPIDeclares(t *testing.T) {
	server := corsServer(t)

	forEachOperation(server.api.OpenAPI(), func(op *huma.Operation) {
		for _, param := range op.Parameters {
			if param == nil || param.In != "header" {
				continue
			}

			if !slicesContainsFold(corsAllowedHeaders, param.Name) {
				t.Errorf("%s declares header %q, which corsAllowedHeaders does not list — "+
					"a browser client cannot send it", op.OperationID, param.Name)
			}
		}
	})
}

// TestCORSNeverWildcardsOrAllowsCredentials pins the two decisions that make the
// rest safe: the answer names one origin, and the API never asks the browser to
// attach ambient credentials, because it has none — authorization is a Bearer
// token the client holds itself, and there is not a cookie anywhere in the
// service.
func TestCORSNeverWildcardsOrAllowsCredentials(t *testing.T) {
	server := corsServer(t)

	for _, req := range []*http.Request{
		func() *http.Request {
			r := request(t, http.MethodGet, "/health", "")
			r.Header.Set("Origin", testOrigin)

			return r
		}(),
		preflight(t, http.MethodGet, "/health", testOrigin),
	} {
		rec := do(t, server.http.Handler, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "*" {
			t.Errorf("%s Access-Control-Allow-Origin = *, want the one origin echoed back", req.Method)
		}

		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("%s Access-Control-Allow-Credentials = %q, want it absent", req.Method, got)
		}
	}
}

// slicesContainsFold is a case-insensitive membership test. Header names are
// case-insensitive on the wire, and comparing them exactly would make these tests
// fail for a reason that does not matter.
func slicesContainsFold(haystack []string, needle string) bool {
	for _, item := range haystack {
		if strings.EqualFold(strings.TrimSpace(item), needle) {
			return true
		}
	}

	return false
}
