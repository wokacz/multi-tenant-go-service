package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories/memory"
)

// TestEveryOperationIsClassified is the guard on the default-deny middleware.
//
// requireBearer authenticates anything not named in publicOperations, so a new
// route is protected by default. That is the safe failure, but it is a silent
// one in the other direction: an operation could be locked down while its
// OpenAPI entry says nothing about a token, and the generated clients would be
// wrong. This asserts the two agree in both directions.
func TestEveryOperationIsClassified(t *testing.T) {
	s, _ := newTestServer(t)

	forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) {
		public := publicOperations[op.OperationID]
		documented := declaresBearer(op)

		switch {
		case public && documented:
			t.Errorf("%s is in publicOperations but its spec declares bearer security", op.OperationID)
		case !public && !documented:
			t.Errorf("%s is authenticated by requireBearer but its spec does not declare bearer security", op.OperationID)
		}
	})
}

// TestPublicOperationsAllExist stops the allow-list from rotting. An entry that
// matches nothing is either a typo — leaving the route it meant to open behind
// a token — or a leftover from a deleted operation.
func TestPublicOperationsAllExist(t *testing.T) {
	s, _ := newTestServer(t)

	registered := map[string]bool{}
	forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) {
		registered[op.OperationID] = true
	})

	for id := range publicOperations {
		if !registered[id] {
			t.Errorf("publicOperations names %q, which is not a registered operation", id)
		}
	}
}

func TestUnauthorizedCarriesTheBearerChallenge(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s.Handler(), request(t, http.MethodGet, "/v1/me", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("401 has no WWW-Authenticate header")
	}

	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", got)
	}
}

// TestPanicBecomesAProblemDocument covers the replacement for chi's Recoverer,
// which answered with an empty body and no content type — the only response in
// the API that was not a problem document.
func TestPanicBecomesAProblemDocument(t *testing.T) {
	s, _ := newTestServer(t)

	mux, ok := s.Handler().(interface {
		Get(string, http.HandlerFunc)
	})
	if !ok {
		t.Fatal("router does not expose Get; cannot install a panicking route")
	}

	mux.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := do(t, s.Handler(), request(t, http.MethodGet, "/boom", ""))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", got)
	}

	var body huma.ErrorModel
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v (body %q)", err, rec.Body.String())
	}

	if body.Status != http.StatusInternalServerError {
		t.Errorf("body status = %d, want 500", body.Status)
	}

	// The panic value must not travel to the client.
	if body.Detail != "internal server error" {
		t.Errorf("detail = %q, want the opaque message", body.Detail)
	}
}

// TestRateLimitAppliesToEveryCostlyRoute is the test the limiter never had.
//
// Its wiring is a switch over literal paths, so renaming a route silently
// stops limiting it, and nothing else in the suite would notice: the default
// test configuration leaves the limits at zero, which disables the limiter
// altogether.
func TestRateLimitAppliesToEveryCostlyRoute(t *testing.T) {
	const perMinute = 2

	limited := []string{
		"/v1/users",
		"/v1/sessions",
		"/v1/sessions/verify",
		"/v1/password-resets",
		"/v1/password-resets/confirm",
		"/v1/orgs/018f0000-0000-7000-8000-000000000000/members",
		"/v1/me/email",
		"/v1/me/password",
		"/v1/orgs/018f0000-0000-7000-8000-000000000000/invitations",
		"/v1/orgs/018f0000-0000-7000-8000-000000000000/invitations/018f0000-0000-7000-8000-000000000001/reissue",
		"/v1/orgs/018f0000-0000-7000-8000-000000000000/files",
		"/v1/me/avatar",
	}

	for _, path := range limited {
		t.Run(path, func(t *testing.T) {
			// A server per subtest, so one route's bucket cannot drain
			// another's and make this pass for the wrong reason.
			s, _, _ := newTestAPIConfig(t, harnessMailer{}, memory.NewUsers(), func(cfg *config.Config) {
				cfg.RegisterPerMinute = perMinute
				cfg.LoginPerMinute = perMinute
				cfg.ResetPerMinute = perMinute
				cfg.InvitePerMinute = perMinute
				cfg.FilesUploadPerMinute = perMinute
			})

			body := `{"name":"Ada","email":"ada@example.com","password":"twelve-chars",` +
				`"password_confirm":"twelve-chars","code":"123456"}`

			last := 0

			for i := 0; i <= perMinute; i++ {
				got := do(t, s.Handler(), withDeviceToken(request(t, http.MethodPost, path, body)))
				last = got.Code

				if got.Code == http.StatusTooManyRequests {
					if retry := got.Header().Get("Retry-After"); retry == "" {
						t.Error("429 has no Retry-After header")
					}

					if ct := got.Header().Get("Content-Type"); ct != "application/problem+json" {
						t.Errorf("429 content type = %q, want application/problem+json", ct)
					}

					return
				}
			}

			t.Fatalf("never rate limited after %d requests, last status %d", perMinute+1, last)
		})
	}
}

// TestRateLimitKeysOnForwardedIPOnlyFromTrustedProxies is why TRUSTED_PROXIES
// exists. A spoofed X-Forwarded-For from an untrusted peer must share one
// bucket; the same header from a listed proxy must not.
func TestRateLimitKeysOnForwardedIPOnlyFromTrustedProxies(t *testing.T) {
	_, cidr, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}

	const perMinute = 2
	body := `{"email":"ada@example.com","password":"twelve-chars"}`

	t.Run("untrusted peer cannot mint buckets with X-Forwarded-For", func(t *testing.T) {
		s, _, _ := newTestAPIConfig(t, harnessMailer{}, memory.NewUsers(), func(cfg *config.Config) {
			cfg.LoginPerMinute = perMinute
		})

		var last int
		for i := 0; i <= perMinute; i++ {
			req := request(t, http.MethodPost, "/v1/sessions", body)
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
			last = do(t, s.Handler(), req).Code
			if last == http.StatusTooManyRequests {
				return
			}
		}

		t.Fatalf("never rate limited, last status %d — spoofed X-Forwarded-For minted extra buckets", last)
	})

	t.Run("trusted proxy uses the forwarded client address", func(t *testing.T) {
		s, _, _ := newTestAPIConfig(t, harnessMailer{}, memory.NewUsers(), func(cfg *config.Config) {
			cfg.LoginPerMinute = perMinute
			cfg.TrustedProxies = []net.IPNet{*cidr}
		})

		for i := 0; i < perMinute; i++ {
			req := request(t, http.MethodPost, "/v1/sessions", body)
			req.RemoteAddr = "127.0.0.1:1"
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
			got := do(t, s.Handler(), req).Code
			if got == http.StatusTooManyRequests {
				t.Fatalf("request %d from distinct forwarded IPs = 429, want the limiter to key per client", i+1)
			}
		}
	})
}

func forEachOperation(spec *huma.OpenAPI, fn func(*huma.Operation)) {
	for _, item := range spec.Paths {
		for _, op := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete,
			item.Options, item.Head, item.Patch, item.Trace,
		} {
			if op != nil {
				fn(op)
			}
		}
	}
}

func declaresBearer(op *huma.Operation) bool {
	for _, scheme := range op.Security {
		if _, ok := scheme["bearer"]; ok {
			return true
		}
	}

	return false
}
