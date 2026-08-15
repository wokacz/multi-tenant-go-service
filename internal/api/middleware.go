package api

import (
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/api/problem"
	"github.com/wokacz/go-example/internal/api/reqctx"
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

// clientInfo puts the peer address and user agent on the context so handlers
// can record them. It runs for every request, including the ones that never
// reach huma.
func (s *Server) clientInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := reqctx.WithClient(r.Context(), reqctx.Client{
			IP:        remoteIP(r),
			UserAgent: r.UserAgent(),
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverer turns a panic into the same problem+json every other error uses,
// and logs it against the request id.
//
// chi's Recoverer is deliberately not used: it answers with a bare text/plain
// body, which would be the only response in the API that is not a problem
// document, and it prints the stack to stderr where nothing correlates it with
// the request that caused it.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// http.ErrAbortHandler is how a handler says "stop, and say
			// nothing" — net/http expects it to reach the server, and
			// swallowing it here would turn a deliberate abort into a 500.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}

			problem.LoggerFrom(r.Context()).Error("panic recovered",
				"panic", rec,
				"stack", string(debug.Stack()),
			)

			problem.Write(w, http.StatusInternalServerError, "internal server error")
		}()

		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var lim *limiter

		switch {
		case r.Method == http.MethodPost && r.URL.Path == v1.Prefix+"/users":
			lim = s.registerLimit
		// Verifying a second factor shares the sign-in budget: both are steps
		// of one sign-in, and a separate bucket would just be a second place
		// to guess from.
		case r.Method == http.MethodPost && (r.URL.Path == v1.Prefix+"/sessions" ||
			r.URL.Path == v1.Prefix+"/sessions/verify"):
			lim = s.loginLimit
		case r.Method == http.MethodPost && (r.URL.Path == v1.Prefix+"/password-resets" ||
			r.URL.Path == v1.Prefix+"/password-resets/confirm"):
			lim = s.resetLimit
		}

		if lim != nil && !lim.Allow(remoteIP(r)) {
			problem.Write(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// unauthorized answers with the challenge RFC 7235 requires on a 401. Without
// it a client cannot tell which scheme to retry under, and generic HTTP
// tooling treats the response as a plain error rather than as an auth prompt.
func (s *Server) unauthorized(ctx huma.Context) {
	ctx.SetHeader("WWW-Authenticate", `Bearer realm="`+s.cfg.APIName+`"`)

	_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "unauthorized")
}

// requireBearer authenticates every operation that is not on the public list.
//
// The default is deny. Reading the operation's Security block and letting
// anything without one through is the tempting shape, and it fails open: a new
// route registered without Security is silently public, and no test that does
// not already know to look for it would catch that. Here the mistake goes the
// other way — forget to list an operation and it stops being reachable, which
// shows up immediately.
func (s *Server) requireBearer(ctx huma.Context, next func(huma.Context)) {
	// A nil operation is not a route this middleware knows how to classify, so
	// it does not get the benefit of the doubt.
	if op := ctx.Operation(); op != nil && publicOperations[op.OperationID] {
		next(ctx)
		return
	}

	header := ctx.Header("Authorization")
	const prefix = "Bearer "

	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		s.unauthorized(ctx)
		return
	}

	if s.deps.Tokens == nil || s.deps.Users == nil {
		s.unauthorized(ctx)
		return
	}

	sess, err := s.deps.Tokens.Parse(strings.TrimSpace(header[len(prefix):]), time.Now().UTC())
	if err != nil {
		s.unauthorized(ctx)
		return
	}

	u, err := s.deps.Users.ByID(ctx.Context(), sess.UserID)
	if err != nil || u.SessionEpoch != sess.Epoch {
		s.unauthorized(ctx)
		return
	}

	// The device is checked on every request, which is what makes revoking one
	// take effect on tokens that were already handed out. The alternative —
	// waiting for the token to expire — would make "revoke this device" a
	// promise the API does not keep for up to a full TTL.
	if _, err := s.deps.Users.ActiveDevice(ctx.Context(), sess.UserID, sess.DeviceID); err != nil {
		s.unauthorized(ctx)
		return
	}

	next(huma.WithContext(ctx, auth.WithSession(ctx.Context(), sess)))
}

// publicOperations is the allow-list requireBearer consults. Keys are
// huma.Operation.OperationID, which is also what the OpenAPI document and every
// generated client call the operation, so a rename cannot leave this stale
// without the contract changing too.
//
// Adding an entry here is the only way to make a route anonymous, and it should
// be as deliberate as it looks.
var publicOperations = map[string]bool{
	"health":                 true,
	"create-user":            true,
	"create-session":         true,
	"verify-session":         true,
	"request-password-reset": true,
	"confirm-password-reset": true,
}
