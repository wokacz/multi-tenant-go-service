package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/api/problem"
	v1 "github.com/wokacz/go-example/internal/api/v1"
	"github.com/wokacz/go-example/internal/auth"
)

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")

		if s.cfg.TLSEnabled() {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) maxBytes(next http.Handler) http.Handler {
	limit := s.cfg.MaxRequestBytes
	if limit <= 0 {
		limit = 1 << 20
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var lim *limiter

		switch {
		case r.Method == http.MethodPost && r.URL.Path == v1.Prefix+"/users":
			lim = s.registerLimit
		case r.Method == http.MethodPost && r.URL.Path == v1.Prefix+"/sessions":
			lim = s.loginLimit
		}

		if lim != nil && !lim.Allow(remoteIP(r)) {
			problem.Write(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireBearer(ctx huma.Context, next func(huma.Context)) {
	if !operationRequiresBearer(ctx.Operation()) {
		next(ctx)
		return
	}

	header := ctx.Header("Authorization")
	const prefix = "Bearer "

	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}

	if s.deps.Tokens == nil {
		_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}

	id, err := s.deps.Tokens.Parse(strings.TrimSpace(header[len(prefix):]), time.Now().UTC())
	if err != nil {
		_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}

	next(huma.WithContext(ctx, auth.WithUserID(ctx.Context(), id)))
}

func operationRequiresBearer(op *huma.Operation) bool {
	if op == nil {
		return false
	}

	for _, scheme := range op.Security {
		if _, ok := scheme["bearer"]; ok {
			return true
		}
	}

	return false
}
