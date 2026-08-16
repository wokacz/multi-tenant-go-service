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
	"github.com/wokacz/go-example/internal/domain/audit"
	"github.com/wokacz/go-example/internal/i18n"
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

// locale negotiates the response language from Accept-Language.
//
// It runs for every request, including the ones that never reach huma, so a 404
// from the router is written in the same language as a 403 from a handler.
// requireBearer refines it once the account is known: a stored preference
// outranks the header, because somebody who chose a language in the product
// meant it, and Accept-Language is often whatever the machine was installed
// with.
func (s *Server) locale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chosen := i18n.Default().Negotiate(r.Header.Get("Accept-Language"), "")

		// Vary, because the body depends on a request header. Without it a
		// shared cache would hand a Polish error to an English client.
		w.Header().Add("Vary", "Accept-Language")
		w.Header().Set("Content-Language", string(chosen))

		next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), chosen)))
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

			problem.Write(w, r, http.StatusInternalServerError, problem.CodeInternal)
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

		// Adding a member turns an arbitrary address into an account, so an
		// organization administrator could otherwise ask "is this person
		// registered here" as fast as they like. It shares the registration
		// budget because it is the same question from the other side, and a
		// fourth knob nobody tunes is worse than a shared one.
		//
		// Matched by shape rather than equality: the path carries an
		// organization id, so there is no literal to compare against.
		case r.Method == http.MethodPost && isMembersPath(r.URL.Path):
			lim = s.registerLimit
		}

		if lim != nil && !lim.Allow(remoteIP(r)) {
			problem.Write(w, r, http.StatusTooManyRequests, problem.CodeTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isMembersPath reports whether the path is /v1/orgs/{something}/members.
//
// The switch above works on literal paths, which is why every organization
// route needed this: renaming one silently stops it being limited, and the
// default test configuration disables the limiter, so nothing else would
// notice. TestRateLimitAppliesToEveryCostlyRoute is what does.
func isMembersPath(path string) bool {
	const prefix = v1.Prefix + "/orgs/"
	const suffix = "/members"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}

	// Exactly one segment between the two, so /orgs/x/members/y/roles does not
	// match by accident.
	middle := path[len(prefix) : len(path)-len(suffix)]

	return middle != "" && !strings.Contains(middle, "/")
}

// unauthorized answers with the challenge RFC 7235 requires on a 401. Without
// it a client cannot tell which scheme to retry under, and generic HTTP
// tooling treats the response as a plain error rather than as an auth prompt.
func (s *Server) unauthorized(ctx huma.Context) {
	ctx.SetHeader("WWW-Authenticate", `Bearer realm="`+s.cfg.APIName+`"`)

	_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, problem.CodeUnauthorized)
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

	// A suspension has to bite on tokens that were already handed out, or
	// "block this account" is a promise the API keeps only after the token
	// expires. The epoch bump above catches it too, but only for tokens issued
	// before the suspension; this catches everything.
	if u.IsSuspended() {
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

	reqCtx := auth.WithSession(ctx.Context(), sess)

	// Everything the audit trail needs, assembled once here because this is the
	// only place that has both halves: the session says who, clientInfo says
	// from where. The store reads it inside the transaction that makes a change.
	client := reqctx.ClientFrom(reqCtx)
	reqCtx = audit.WithActor(reqCtx, audit.Actor{
		ID:        sess.UserID,
		IP:        client.IP,
		UserAgent: client.UserAgent,
	})

	// The account's own choice outranks the header from here on. Set on the
	// context rather than only on the response, so problem.Error and every
	// handler below see the same language.
	if u.Locale != "" {
		chosen := i18n.Default().Negotiate(ctx.Header("Accept-Language"), u.Locale)
		reqCtx = i18n.WithLocale(reqCtx, chosen)
		ctx.SetHeader("Content-Language", string(chosen))
	}

	next(huma.WithContext(ctx, reqCtx))
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
