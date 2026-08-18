package api

import (
	"errors"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/api/reqctx"
	v1 "github.com/wokacz/multi-tenant-go-service/internal/api/v1"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
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

// corsAllowedHeaders are the request headers a browser client may send.
//
// Authorization and Content-Type are here because nothing works without them.
// The rest mirror the header parameters the operations actually declare, and
// TestCORSAllowsEveryHeaderTheAPIDeclares walks the OpenAPI document to prove the
// two lists have not drifted — adding a header parameter and forgetting this line
// fails silently in a browser and nowhere else.
var corsAllowedHeaders = []string{
	"Authorization",
	"Content-Type",
	"Accept-Language",
	"If-None-Match",
	"X-Device-Token",
}

// corsExposedHeaders are the response headers a browser client may read.
//
// Only headers outside the CORS safelist need naming: Cache-Control,
// Content-Language, Content-Length, Content-Type, Expires, Last-Modified and
// Pragma are readable without being listed, which is why Content-Language is
// absent despite being set on every response.
//
// Each of these is load-bearing for a client. ETag drives the conditional
// request on the permission snapshot, Retry-After tells it how long to wait after
// a 429, and WWW-Authenticate is how it tells "no token" apart from "token
// rejected".
var corsExposedHeaders = []string{
	"ETag",
	"Retry-After",
	"WWW-Authenticate",
}

// corsAllowedMethods is written out rather than echoed back from
// Access-Control-Request-Method, so the answer describes this API instead of
// agreeing with whatever was asked.
const corsAllowedMethods = "GET, POST, PATCH, PUT, DELETE, OPTIONS"

// corsMaxAge caps how long a browser may reuse one preflight. Ten minutes keeps
// the round trips down while still letting a changed allowlist take effect
// without anyone clearing a cache.
const corsMaxAge = "600"

// cors answers cross-origin requests from the origins in CORS_ALLOWED_ORIGINS.
//
// It sits before locale, clientInfo and the rate limiter, so a preflight is
// answered without any of the work the real request does. A preflight is asked by
// the browser rather than by the caller, and charging it to a budget would let a
// browser client rate-limit itself just by loading. Every case in rateLimit is
// gated on POST today, so that would not happen as things stand — the ordering is
// what keeps it from becoming something the limiter has to remember.
//
// A request from an origin that is not on the list is not refused here: it is
// answered without the header that would let the page read the response. The
// browser is what enforces this, and refusing server-side would only break
// non-browser callers that happen to send an Origin, while changing nothing for
// an attacker — curl ignores the header either way.
func (s *Server) cors(next http.Handler) http.Handler {
	origins := s.cfg.CORSAllowedOrigins

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Nothing configured: the API has no browser client, and no response
		// varies by origin, so not even Vary is warranted.
		if len(origins) == 0 {
			next.ServeHTTP(w, r)

			return
		}

		// Vary regardless of whether this origin matched. Without it a shared
		// cache could hand a response granted to one origin to another, or cache
		// the ungranted version and serve it to an allowed one.
		w.Header().Add("Vary", "Origin")

		origin := r.Header.Get("Origin")
		allowed := origin != "" && slices.Contains(origins, strings.ToLower(origin))

		if allowed {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", strings.Join(corsExposedHeaders, ", "))
		}

		// A preflight is answered here and never routed. Letting it fall through
		// would reach chi's MethodNotAllowed, because no operation registers
		// OPTIONS.
		if isCORSPreflight(r) {
			if allowed {
				h := w.Header()
				h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
				h.Set("Access-Control-Allow-Headers", strings.Join(corsAllowedHeaders, ", "))
				h.Set("Access-Control-Max-Age", corsMaxAge)
			}

			w.WriteHeader(http.StatusNoContent)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// isCORSPreflight identifies the browser's permission question. All three parts
// are required: an OPTIONS without them is an ordinary request for a route that
// does not exist.
func isCORSPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != ""
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
			IP:        s.remoteIP(r),
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

		// Inviting mails an address the caller named, which is the same shape of
		// abuse as registration — but not the same act, and it no longer shares
		// registration's budget. Sharing it meant onboarding a team from one
		// office address stopped at the fifth person, while the reason for the
		// shared bucket had already gone: add-member stopped being an oracle for
		// which addresses are registered when invitations grew a token of their
		// own.
		//
		// Reissuing is on the same budget because it does the same thing: a new
		// token to the same address. It was not limited at all before, which is
		// the more interesting half of this — the switch matches literal paths,
		// and nothing fails when a mailing route is simply missing from it.
		//
		// Matched by shape rather than equality: the paths carry an organization
		// id, so there is no literal to compare against.
		case r.Method == http.MethodPost && (isMembersPath(r.URL.Path) ||
			isBulkInvitePath(r.URL.Path) || isReissuePath(r.URL.Path)):
			lim = s.inviteLimit

		// Asking to change an address emails one the caller named, which is the
		// same shape of abuse and shares the same budget. The confirmation step
		// does not: it emails nobody, and its guessing is already bounded by the
		// per-code attempt cap.
		case r.Method == http.MethodPost && r.URL.Path == v1.Prefix+"/me/email":
			lim = s.registerLimit

		// Changing a password takes the current one, which makes this an
		// authenticated guessing surface: a token with a wrong password attached
		// still gets an answer. It shares the reset budget rather than getting a
		// fourth knob, because both are "prove a secret to move the password".
		case r.Method == http.MethodPost && r.URL.Path == v1.Prefix+"/me/password":
			lim = s.resetLimit
		}

		if lim != nil && !lim.Allow(s.remoteIP(r)) {
			problem.Write(w, r, http.StatusTooManyRequests, problem.CodeTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isBulkInvitePath reports whether the path is
// /v1/orgs/{something}/invitations — the bulk invite.
//
// One request costs one token however many addresses it carries, so the real
// bound is INVITE_PER_MINUTE times the batch cap. That is stated rather than
// hidden: the limiter is chi middleware and runs before the body is read, so it
// cannot charge per address without parsing the request twice. A per-organization
// mail quota is the honest fix and is not this.
func isBulkInvitePath(path string) bool {
	return orgSubPath(path, "/invitations") != ""
}

// isReissuePath reports whether the path is
// /v1/orgs/{something}/invitations/{something}/reissue.
func isReissuePath(path string) bool {
	const prefix = v1.Prefix + "/orgs/"
	const suffix = "/reissue"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}

	middle := path[len(prefix) : len(path)-len(suffix)]
	// {orgID}/invitations/{invitationID}
	parts := strings.Split(middle, "/")

	return len(parts) == 3 && parts[0] != "" && parts[1] == "invitations" && parts[2] != ""
}

// isMembersPath reports whether the path is /v1/orgs/{something}/members.
//
// The switch above works on literal paths, which is why every organization
// route needed this: renaming one silently stops it being limited, and the
// default test configuration disables the limiter, so nothing else would
// notice. TestRateLimitAppliesToEveryCostlyRoute is what does.
func isMembersPath(path string) bool {
	return orgSubPath(path, "/members") != ""
}

// orgSubPath returns the organization id in /v1/orgs/{orgID}{suffix}, or "" when
// the path is not that shape.
//
// Exactly one segment before the suffix, so /orgs/x/members/y/roles does not match
// by accident.
func orgSubPath(path, suffix string) string {
	const prefix = v1.Prefix + "/orgs/"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}

	middle := path[len(prefix) : len(path)-len(suffix)]
	if middle == "" || strings.Contains(middle, "/") {
		return ""
	}

	return middle
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
