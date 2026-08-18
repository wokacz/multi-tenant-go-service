package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	v1 "github.com/wokacz/multi-tenant-go-service/internal/api/v1"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
)

// Version is reported in the OpenAPI document. It describes the API contract,
// not the build, so it only moves when the shape of the API changes.
const Version = "0.1.0"

// specAPIName is the title used when rendering the committed OpenAPI document.
// It is fixed rather than taken from API_NAME so the file does not change
// depending on whose environment generated it.
const specAPIName = "Example"

// Pinger is the slice of the store the health check actually needs. Depending
// on the one method rather than on *store.DB keeps this package testable with a
// stub and lets the store grow without touching the server.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are everything the API needs from the rest of the process. A struct
// keeps NewServer's signature stable as modules are added, and keeps main.go to
// assembling values rather than threading a growing parameter list.
type Deps struct {
	// DB backs the health check only. Repositories reach the database through
	// the domain services.
	DB     Pinger
	Users  *user.Service
	Tokens *auth.Signer
	Mail   mail.Sender

	// Authz answers "may this caller do this here" for every operation that is
	// neither public nor self-service. It is an interface rather than the
	// concrete service so a test can decide without a database.
	Authz authz.Authorizer

	// Snapshots describes what a caller may do, for the client to render from.
	// A separate field from Authz because the two are used at different points
	// and only one of them decides anything.
	Snapshots authz.Snapshotter
	Orgs      *orgs.Service
	Audit     *audit.Service
}

// Server owns the HTTP listener and the huma API registered on it. huma appears
// in this package and the ones beneath it and nowhere else: everything below
// deals in domain types and domain errors, and the translation to HTTP happens
// here and in problem. internal/architecture_test.go enforces that.
type Server struct {
	cfg           *config.Config
	log           *slog.Logger
	deps          Deps
	http          *http.Server
	api           huma.API
	registerLimit *limiter
	loginLimit    *limiter
	resetLimit    *limiter
	inviteLimit   *limiter
}

// NewServer wires the router, the middleware chain and the huma adapter. It
// does not bind a port — that happens in Run.
func NewServer(cfg *config.Config, log *slog.Logger, deps Deps) *Server {
	// Before any route is registered: huma reflects the error schema off
	// huma.NewError while building each operation's responses, so a later call
	// would leave the contract describing the wrong body.
	problem.Install()

	s := &Server{
		cfg:           cfg,
		log:           log,
		deps:          deps,
		registerLimit: newLimiter(cfg.RegisterPerMinute),
		loginLimit:    newLimiter(cfg.LoginPerMinute),
		resetLimit:    newLimiter(cfg.ResetPerMinute),
		inviteLimit:   newLimiter(cfg.InvitePerMinute),
	}

	router := chi.NewMux()

	// Order is outermost first. RequestID has to lead so every later layer can
	// stamp the same id, and Recoverer sits inside the logger so a panic is
	// still logged as the 500 it turns into.
	//
	// chi's RealIP is deliberately absent: it rewrites RemoteAddr from
	// X-Forwarded-For, which any client can set. remoteIP reads that header
	// only from addresses listed in TRUSTED_PROXIES.
	// cors sits inside the logger, so a preflight a browser client got wrong still
	// shows up, and outside the rate limiter, so answering one costs nothing that
	// belongs to the route it is asking about.
	router.Use(middleware.RequestID)
	router.Use(middleware.CleanPath)
	router.Use(s.securityHeaders)
	router.Use(s.maxBytes)
	router.Use(s.requestLogger)
	router.Use(s.cors)
	router.Use(s.locale)
	router.Use(s.clientInfo)
	router.Use(s.rateLimit)
	router.Use(s.recoverer)

	humaCfg := s.openAPIConfig()

	// chi answers these itself, before huma is involved, so they need the same
	// error shape wired up explicitly.
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusNotFound, problem.CodeNoOperation, r.URL.Path)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusMethodNotAllowed, problem.CodeMethodNotAllowed, r.Method, r.URL.Path)
	})

	s.api = humachi.New(router, humaCfg)

	// Order matters and is the same order the questions are asked in: who are
	// you, then what may you do. requirePermission reads the session
	// requireBearer put on the context, so it cannot run first.
	s.api.UseMiddleware(s.requireBearer, s.requirePermission)
	s.registerRoutes()

	s.http = &http.Server{
		Addr:              cfg.Addr(),
		Handler:           router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	return s
}

// API exposes the huma API so route packages can register operations on it.
func (s *Server) API() huma.API { return s.api }

// openAPIConfig describes the API itself. Everything here ends up in
// /openapi.json and therefore in Swagger UI and in any generated client, so it
// is worth more than the bare title and version huma defaults to.
func (s *Server) openAPIConfig() huma.Config {
	cfg := huma.DefaultConfig(s.cfg.APIName, Version)

	if cfg.Components == nil {
		cfg.Components = &huma.Components{}
	}

	if cfg.Components.SecuritySchemes == nil {
		cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}

	cfg.Components.SecuritySchemes["bearer"] = &huma.SecurityScheme{
		Type:         "http",
		Scheme:       "bearer",
		BearerFormat: "JWT",
	}

	// Swagger UI in place of huma's default Stoplight Elements. Huma serves it
	// with the asset versions pinned, subresource integrity hashes attached and
	// a matching CSP header — worth using as-is rather than hand-rolling the
	// page, which is how those protections get dropped.
	cfg.DocsRenderer = huma.DocsRendererSwaggerUI

	cfg.Info.Description = "Tracks users, their known devices, and login history."
	// Identifier rather than URL: OpenAPI 3.1 allows only one of the two, and an
	// SPDX id needs no guess at where the licence text is hosted.
	cfg.Info.License = &huma.License{Name: "MIT", Identifier: "MIT"}

	cfg.Tags = []*huma.Tag{
		{Name: "meta", Description: "Service health and introspection"},
		{Name: "auth", Description: "Registration, sign-in and password reset"},
		{Name: "users", Description: "User accounts"},
		{Name: "devices", Description: "Known devices and sign-in history"},
		{Name: "organizations", Description: "Organizations, membership and roles"},
		{Name: "platform", Description: "Installation-wide administration"},
	}

	if s.cfg.Env.IsProduction() {
		// The browsable UI and the machine-readable map both go away. Generated
		// clients are built from the committed api/openapi.yaml, not from a
		// live document the process would otherwise publish.
		cfg.DocsPath = ""
		cfg.OpenAPIPath = ""
		cfg.SchemasPath = ""

		return cfg
	}

	// Servers drives the target of Swagger UI's "Try it out". It is only
	// declared for development, where the address is known — in production the
	// public URL is whatever sits in front of the process, so leaving it out
	// lets clients fall back to the origin they fetched the document from.
	cfg.Servers = []*huma.Server{
		{URL: fmt.Sprintf("http://localhost:%d", s.cfg.APIPort), Description: "Local development"},
	}

	return cfg
}

// Spec renders the OpenAPI document that this build serves, for committing to
// the repository.
//
// It is generated from a fixed configuration rather than the running one, so
// the committed file is a property of the code alone. Reading the live config
// would make the output depend on which port the developer happened to use, and
// `git diff --exit-code` would then fail for reasons that have nothing to do
// with the contract.
func Spec() ([]byte, error) {
	cfg := &config.Config{
		APIName: specAPIName,
		// Production semantics: no servers block, so the document does not
		// hard-code anyone's localhost.
		Env: config.EnvProduction,
	}

	// Deps stay zero. Registration only reads the handler types to build the
	// schemas; nothing is invoked, so no service or database is needed.
	s := NewServer(cfg, slog.New(slog.DiscardHandler), Deps{})

	out, err := s.api.OpenAPI().YAML()
	if err != nil {
		return nil, fmt.Errorf("api: render openapi: %w", err)
	}

	return out, nil
}

// Run serves until ctx is cancelled, then drains in-flight requests.
func (s *Server) Run(ctx context.Context) error {
	// Bind before announcing anything: a port clash should fail here rather
	// than after a log line claiming the server is listening.
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("api: listen on %s: %w", s.http.Addr, err)
	}

	serveErr := make(chan error, 1)

	go func() {
		s.log.Info("api listening", "addr", ln.Addr().String(), "env", string(s.cfg.Env))

		// ErrServerClosed is what a graceful Shutdown looks like from here, so
		// it is not worth waking the select for.
		if err := s.serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("api: serve: %w", err)
	case <-ctx.Done():
	}

	// A fresh context: ctx is already cancelled, and passing it would abort the
	// drain immediately instead of giving open requests their grace period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	s.log.Info("api shutting down", "timeout", s.cfg.ShutdownTimeout)

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("api: shutdown: %w", err)
	}

	return nil
}

func (s *Server) serve(ln net.Listener) error {
	if s.cfg.TLSEnabled() {
		return s.http.ServeTLS(ln, s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	}

	return s.http.Serve(ln)
}

// requestLogger records one line per request and puts a logger carrying the
// request id into the context, so anything downstream — errors.go in
// particular — logs against the request it belongs to.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := s.log.With(
			"request_id", middleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
		)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		defer func() {
			log.Info("request",
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_ip", s.remoteIP(r),
			)
		}()

		next.ServeHTTP(ww, r.WithContext(problem.WithLogger(r.Context(), log)))
	})
}

// remoteIP is the client address with the ephemeral port dropped.
//
// The TCP peer is always the starting point. X-Forwarded-For is read only when
// that peer sits in TRUSTED_PROXIES, walking the header from the right and
// taking the first hop that is not itself trusted. chi's RealIP is not used:
// it rewrites RemoteAddr from a header any client can set.
func (s *Server) remoteIP(r *http.Request) string {
	peer := tcpPeer(r)
	if !s.trustedProxy(peer) {
		return peer
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}

	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(parts[i])
		if hop == "" {
			continue
		}

		if host, _, err := net.SplitHostPort(hop); err == nil {
			hop = host
		}

		if !s.trustedProxy(hop) {
			return hop
		}
	}

	return peer
}

func tcpPeer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

func (s *Server) trustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil || s.cfg == nil {
		return false
	}

	for i := range s.cfg.TrustedProxies {
		if s.cfg.TrustedProxies[i].Contains(parsed) {
			return true
		}
	}

	return false
}

// HealthOutput is the body of the health check.
type HealthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok" doc:"Overall health of the service"`
	}
}

func (s *Server) registerRoutes() {
	// Versioned operations live in their own package; a future v2 registers
	// beside this line rather than replacing it.
	v1.Register(s.api, v1.Deps{
		Users:  s.deps.Users,
		Tokens: s.deps.Tokens,
		Mail:   s.deps.Mail,
		Orgs:   s.deps.Orgs,
		Authz:  s.deps.Snapshots,
		Audit:  s.deps.Audit,
		Log:    s.log,
	})

	// Health stays outside /v1 on purpose — see v1.Prefix.
	huma.Register(s.api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Description: "Reports whether the service can serve traffic. Returns 503 " +
			"when a dependency it cannot work without is unreachable.",
		Tags: []string{"meta"},
		// Without this the 503 below is missing from the OpenAPI document, so
		// Swagger UI and any generated client would only know about the 200.
		Errors: []int{http.StatusServiceUnavailable},
	}, s.health)
}

// health answers the probe. It reaches the database rather than reporting on
// the process alone: this process is useless without one, and a check that only
// proves the HTTP server is up would keep a broken instance in the load
// balancer's rotation.
func (s *Server) health(ctx context.Context, _ *struct{}) (*HealthOutput, error) {
	// Its own deadline, not the request's. Whatever is polling this is usually
	// waiting on a much shorter clock than an ordinary API caller.
	ctx, cancel := context.WithTimeout(ctx, s.cfg.HealthTimeout)
	defer cancel()

	if err := s.deps.DB.Ping(ctx); err != nil {
		s.log.Error("health check failed", "dependency", "database", "error", err)

		// A failing status has to be a failing code — a probe reads the status
		// line, not the body.
		return nil, huma.Error503ServiceUnavailable("unavailable")
	}

	out := &HealthOutput{}
	out.Body.Status = "ok"

	return out, nil
}
