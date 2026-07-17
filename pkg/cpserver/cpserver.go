// Package cpserver is the deployment-agnostic builder for the Vulos control
// plane. It turns a generic, host-neutral Config plus a set of injected provider
// seams (Deps) into a running HTTP control plane — WITHOUT any knowledge of a
// specific host, payment processor, or object-storage vendor.
//
// The contract is deliberately small:
//
//		srv, err := cpserver.New(cfg, deps)   // cfg = generic config, deps = seams
//		err = srv.Run(ctx)                    // serve until ctx is cancelled
//
//	  - Config is populated from plain env/flags/file by whoever builds the
//	    server. It carries NO provider-specific knowledge (no Fly, no the managed store, no
//	    Paystack) — just an address, a domain, a database DSN, and the like.
//	  - Deps carries the pluggable providers. A self-hoster leaves them nil and
//	    New fills in the free no-op / bring-your-own defaults from pkg/billingport
//	    and pkg/storageport. A commercial distributor injects real
//	    implementations (a payment rail, a tier/quota resolver, a bucket
//	    provisioner) built in its own module.
//
// Deps is a struct of interfaces so new injected providers scale cleanly: add a
// field, default it in applyDefaults, expose it on Runtime. Composition code
// mounts feature routes through the RouteRegistrar hook, so the operational and
// commercial route sets compose without this package importing either.
//
// This session ships the builder, the seam wiring, the always-on operational
// endpoints (liveness/readiness/version), and the registration hook. Migrating
// the remaining feature routes (currently still assembled in vulos-cloud's
// cmd/server) onto the RouteRegistrar hook is mechanical follow-up work — each
// route file registers itself against the *Runtime instead of a local main().
package cpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/cproutes"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/middleware"
	"github.com/vul-os/vulos-management/pkg/relayscale"
	"github.com/vul-os/vulos-management/pkg/storageport"
)

// Config is the generic, host-neutral configuration for a control plane. Every
// field is populated from plain env/flags/file by the caller; nothing here knows
// about any hosting provider, payment processor, or storage vendor.
type Config struct {
	// Addr is the TCP listen address, e.g. ":8080". Defaults to ":8080".
	Addr string

	// Domain is the deployment's own domain (identity emails, cookie scope, CT
	// zone). Defaults to "vulos.org" when empty, matching env.Domain().
	Domain string

	// DatabaseDSN selects the control-plane database backend. When set, every
	// subsystem opens a Postgres schema via this DSN. When empty, subsystems open
	// per-subsystem SQLite files under DBDir — the zero-config self-host default.
	DatabaseDSN string

	// DBDir is the directory for per-subsystem SQLite files when DatabaseDSN is
	// empty. Defaults to "./cpdata".
	DBDir string

	// SessionSecret keys the auth store's sessions. When empty it is read from
	// SESSION_SECRET; a dev fallback is used in non-prod, but prod refuses to
	// start without one.
	SessionSecret string

	// Environment is a free-form deployment label ("prod", "dev", …) surfaced on
	// the version endpoint. It does not change behaviour on its own.
	Environment string

	// Version is the build version surfaced on /version. Defaults to "dev".
	Version string

	// ReadTimeout / WriteTimeout / ShutdownTimeout bound request and shutdown
	// behaviour. Zero values fall back to conservative defaults.
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

func (c *Config) withDefaults() {
	if c.Addr == "" {
		c.Addr = ":8080"
	}
	if c.Domain == "" {
		c.Domain = "vulos.org"
	}
	if c.DBDir == "" {
		c.DBDir = "./cpdata"
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	if c.Environment == "" {
		c.Environment = "dev"
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 15 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 30 * time.Second
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 20 * time.Second
	}
}

// RouteRegistrar mounts a set of HTTP routes onto the server's mux using shared
// runtime state (the database, the injected seams, the deployment domain). Both
// the operational route set (this module) and a distributor's commercial route
// set (its module) are mounted through registrars, so cpserver never imports
// either directly. Registrars run in the order provided during New.
type RouteRegistrar func(mux *http.ServeMux, rt *Runtime) error

// Deps carries the injected provider seams and optional collaborators. Leave a
// field nil to accept the free self-host default. It is a struct of interfaces
// so the set of pluggable providers can grow without changing the New signature.
type Deps struct {
	// Billing is the payment rail. Default: billingport.NewNoopProvider (never
	// charges).
	Billing billingport.BillingProvider

	// Entitlements resolves an account's tier / seat cap / storage quota.
	// Default: billingport.NewNoopResolver (unlimited self-host tier).
	Entitlements billingport.EntitlementResolver

	// StorageProvisioner creates managed object-storage buckets. Default:
	// storageport.NewNoopProvisioner (bring-your-own-bucket; provisions nothing).
	StorageProvisioner storageport.StorageProvisioner

	// RelayProvisioner scales the relay pool on the operator's substrate. Default:
	// resolved from RELAY_PROVISIONER (manual|external|kubernetes|firecracker|
	// proxmox), falling back to relayscale.ManualProvisioner (bring-your-own-relay;
	// provisions nothing). A commercial distributor injects its managed
	// multi-provider (Fly/Hetzner/Vultr) here, bypassing the env registry.
	RelayProvisioner relayscale.RelayProvisioner

	// Logger is the structured logger. Default: slog.Default().
	Logger *slog.Logger

	// Routes are feature-route registrars mounted after the always-on
	// operational endpoints. A distributor appends its commercial route
	// registrars here; the operational route set is appended by callers that
	// have finished migrating routes onto the hook.
	Routes []RouteRegistrar

	// OnStart hooks run once after the server is assembled and before it serves
	// (e.g. a distributor starting its billing sweeper). They must not block.
	OnStart []func(ctx context.Context, rt *Runtime)
}

func (d *Deps) withDefaults() {
	if d.Billing == nil {
		d.Billing = billingport.NewNoopProvider()
	}
	if d.Entitlements == nil {
		d.Entitlements = billingport.NewNoopResolver()
	}
	if d.StorageProvisioner == nil {
		d.StorageProvisioner = storageport.NewNoopProvisioner()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.RelayProvisioner == nil {
		// Resolve from RELAY_PROVISIONER; a misconfigured provider (e.g. kubernetes
		// without a token) fails closed to manual so the CP still boots and a
		// self-hoster who set nothing gets bring-your-own-relay.
		if p, err := relayscale.ProvisionerFromEnv(); err == nil {
			d.RelayProvisioner = p
		} else {
			d.Logger.Warn("relay provisioner config invalid; falling back to manual", "err", err)
			d.RelayProvisioner = relayscale.NewManualProvisioner()
		}
	}
}

// Runtime is the shared state a RouteRegistrar builds handlers against. It
// exposes the resolved config, the database handle, the injected seams, and the
// logger. Extend it (not the New signature) when a new shared collaborator is
// added.
type Runtime struct {
	Config Config
	// DB is the auth-subsystem database handle, used for readiness checks. Other
	// subsystems open their own handle via cpdb.Open(<subsystem>) — the DB backend
	// (SQLite dir or Postgres schema) is already configured in the process env by
	// the time registrars run.
	DB *cpdb.DB
	// AuthStore is the shared account/session store nearly every route group gates
	// against. Built once by New.
	AuthStore          *auth.Store
	Billing            billingport.BillingProvider
	Entitlements       billingport.EntitlementResolver
	StorageProvisioner storageport.StorageProvisioner
	RelayProvisioner   relayscale.RelayProvisioner
	Logger             *slog.Logger
}

// Server is an assembled control plane ready to Run.
type Server struct {
	cfg     Config
	rt      *Runtime
	http    *http.Server
	onStart []func(ctx context.Context, rt *Runtime)
	// closers are the operational store closers returned by
	// cproutes.RegisterOperational; run in reverse order on Close.
	closers []func()
}

// New assembles a control plane from generic config and injected seams. Nil
// Deps fields are filled with the free self-host defaults. New opens the
// configured database, mounts the always-on operational endpoints, then runs
// each provided RouteRegistrar in order.
func New(cfg Config, deps Deps) (*Server, error) {
	cfg.withDefaults()
	deps.withDefaults()

	// Configure the process DB backend so every subsystem's cpdb.Open(...) resolves
	// to the same place: a Postgres DSN, or per-subsystem SQLite files under DBDir.
	if cfg.DatabaseDSN != "" {
		_ = os.Setenv("DATABASE_URL", cfg.DatabaseDSN)
	} else if os.Getenv("DATABASE_URL") == "" && os.Getenv("VULOS_DATABASE_URL") == "" {
		_ = os.Setenv("VULOS_DB_DIR", cfg.DBDir)
	}

	authStore, authDB, err := openAuthStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("cpserver: open auth store: %w", err)
	}

	rt := &Runtime{
		Config:             cfg,
		DB:                 authDB,
		AuthStore:          authStore,
		Billing:            deps.Billing,
		Entitlements:       deps.Entitlements,
		StorageProvisioner: deps.StorageProvisioner,
		RelayProvisioner:   deps.RelayProvisioner,
		Logger:             deps.Logger,
	}

	// M2 — fail-open-to-free guard. Self-host legitimately runs the no-op
	// entitlement resolver in prod (unlimited, uncapped, honest), so this must NOT
	// hard-fail. But if a CLOUD composition regression drops the resolver
	// injection, the process boots with EVERY quota/storage cap disabled and
	// metering off, silently. Log a LOUD warning when prod + noop so the
	// regression is observable in logs; the resolver identity is also exposed on
	// /version + /healthz for a machine-checkable signal.
	warnBillingSeamFailOpen(rt.Logger, env.IsProdResolved(), rt.Entitlements, rt.Billing, cfg.Environment)

	mux := http.NewServeMux()
	mountOperational(mux, rt)

	// Mount the full operational route surface (auth, recovery, developer/LLM
	// keys, OIDC provider, mobile push, DDoS, abuse, security, legal, products,
	// boot, …) built against the injected seams. The commercial module appends
	// its own route registrars via deps.Routes.
	var ddosMWs []func(http.Handler) http.Handler
	opClosers := cproutes.RegisterOperational(mux, cproutes.OperationalDeps{
		AuthStore:        rt.AuthStore,
		AuthDB:           rt.DB,
		Entitlements:     rt.Entitlements,
		DBDir:            cfg.DBDir,
		AdminAccountID:   os.Getenv("CP_ADMIN_ACCOUNT_ID"),
		RelayProvisioner: rt.RelayProvisioner,
		DDoSMiddleware:   func(mws []func(http.Handler) http.Handler) { ddosMWs = mws },
	})

	for i, reg := range deps.Routes {
		if reg == nil {
			continue
		}
		if err := reg(mux, rt); err != nil {
			return nil, fmt.Errorf("cpserver: route registrar #%d: %w", i, err)
		}
	}

	return &Server{
		cfg:     cfg,
		rt:      rt,
		closers: opClosers,
		http: &http.Server{
			Addr:         cfg.Addr,
			Handler:      globalMiddleware(mux, ddosMWs),
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		},
		onStart: deps.OnStart,
	}, nil
}

// warnBillingSeamFailOpen implements the M2 fail-open-to-free guard. When the
// environment resolves to prod AND the free no-op entitlement resolver is wired,
// it logs a LOUD warning (every quota/storage cap disabled, metering off) — but
// never hard-fails, because a self-host prod deployment legitimately runs the
// no-op seams. A missing entitlement injection on the CLOUD control plane is a
// silent fail-open this surfaces. Split out from New so the predicate is
// unit-testable without booting the whole operational surface (some subsystems
// fail-closed / os.Exit in prod without their own secrets). No-op outside prod.
func warnBillingSeamFailOpen(logger *slog.Logger, prod bool, ent billingport.EntitlementResolver, bill billingport.BillingProvider, environment string) {
	if !prod {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	if billingport.IsNoopResolver(ent) {
		logger.Warn("BILLING SEAM FAIL-OPEN: prod is running the NO-OP entitlement resolver — all quota/storage caps are DISABLED and metering is OFF. This is correct for a self-host prod deployment; if this is the CLOUD control plane, the entitlement resolver injection has been dropped (a regression) and every account is silently uncapped. Verify the composition root injects a real EntitlementResolver.",
			"entitlements_rail", "noop",
			"billing_rail", bill.Name(),
			"environment", environment)
	}
	if billingport.IsNoopProvider(bill) {
		logger.Warn("BILLING SEAM: prod is running the NO-OP payment rail — no card is ever charged. Correct for self-host; a regression for cloud.",
			"billing_rail", "noop", "environment", environment)
	}
}

// globalMiddleware wraps the assembled mux in the same defence-in-depth chain the
// cloud composition root (backend/cp/cmd/server/main.go) applies, so the self-host
// binary is no longer a bare mux with no global protections (M1). Order, outermost
// → innermost:
//
//	RequestID → PanicRecovery → SecureHeaders → CORS → CSRFOriginCheck
//	  → RateLimit (when enabled) → DDoS layers → mux
//
// The CORS/CSRF allow-list and the rate-limit toggle come from the environment
// profile (env.Get): a self-host prod deployment enforces the full stack, while a
// local profile keeps rate-limiting off for curl-testing. CSRFOriginCheck degrades
// to pass-through when no origins are configured.
func globalMiddleware(h http.Handler, ddosMWs []func(http.Handler) http.Handler) http.Handler {
	defaults := env.Get()
	mws := []func(http.Handler) http.Handler{
		middleware.RequestID,
		middleware.PanicRecovery,
		middleware.SecureHeaders,
		middleware.CORSWithOrigins(defaults.CORSOrigins),
		middleware.CSRFOriginCheck(defaults.CORSOrigins),
	}
	if defaults.RateLimitEnabled {
		mws = append(mws, middleware.RateLimit(middleware.DefaultRateLimitConfig))
	}
	// DDoS layers run after CORS/CSRF so pre-flight OPTIONS are not counted as
	// attack traffic (matches cloud's ordering).
	mws = append(mws, ddosMWs...)
	return middleware.Chain(h, mws...)
}

// Runtime returns the shared runtime, primarily for tests and OnStart hooks.
func (s *Server) Runtime() *Runtime { return s.rt }

// Handler returns the assembled HTTP handler, primarily for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run starts serving and blocks until ctx is cancelled, then shuts down
// gracefully within Config.ShutdownTimeout.
func (s *Server) Run(ctx context.Context) error {
	for _, hook := range s.onStart {
		if hook != nil {
			hook(ctx, s.rt)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		s.rt.Logger.Info("control plane listening", "addr", s.cfg.Addr, "domain", s.cfg.Domain, "env", s.cfg.Environment)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	}
}

// Close releases server resources: the operational store closers (reverse
// order), then the auth store + its database handle.
func (s *Server) Close() error {
	for i := len(s.closers) - 1; i >= 0; i-- {
		if s.closers[i] != nil {
			s.closers[i]()
		}
	}
	if s.rt != nil && s.rt.AuthStore != nil {
		return s.rt.AuthStore.Close()
	}
	return nil
}

// openAuthStore builds the shared account/session store — the spine nearly every
// route group gates against. The DB backend env is already configured by New, so
// cpdb.Open("auth") resolves to the right place.
func openAuthStore(cfg Config) (*auth.Store, *cpdb.DB, error) {
	secret := []byte(cfg.SessionSecret)
	if len(secret) == 0 {
		secret = []byte(os.Getenv("SESSION_SECRET"))
	}
	if len(secret) == 0 {
		if env.IsProdResolved() {
			return nil, nil, fmt.Errorf("SESSION_SECRET is unset in prod — refusing to start")
		}
		secret = []byte("vulos-cp-dev-secret-change-me")
	}
	authDB, err := cpdb.Open("auth")
	if err != nil {
		return nil, nil, fmt.Errorf("open auth cpdb: %w", err)
	}
	store, err := auth.OpenAuthStore(authDB, secret)
	if err != nil {
		_ = authDB.Close()
		return nil, nil, fmt.Errorf("open auth store: %w", err)
	}
	return store, authDB, nil
}

// mountOperational installs the always-on operational endpoints every control
// plane exposes regardless of deployment: liveness, readiness, and version.
func mountOperational(mux *http.ServeMux, rt *Runtime) {
	// GET /healthz — lightweight liveness probe (always 200 once serving). Also
	// reports which billing/entitlement rails are wired so a fail-open-to-free
	// regression (M2: cloud booting with the no-op resolver) is observable on the
	// always-on liveness endpoint, not only in logs.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":            "ok",
			"billing_rail":      rt.Billing.Name(),
			"entitlements_rail": billingport.ResolverRail(rt.Entitlements),
		})
	})

	// GET /readyz — readiness probe: 200 only when the database answers.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if rt.DB == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"no-db"}`))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := rt.DB.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"db-unreachable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// GET /version — build/deploy metadata + which billing rail is wired (so an
	// operator can confirm a self-host is running the no-op rail, never charging).
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version":     rt.Config.Version,
			"environment": rt.Config.Environment,
			"domain":      rt.Config.Domain,
			// Billing + entitlement rail identity: "noop" = free self-host seams
			// (never charges / uncapped); "custom" resolver = an injected
			// commercial impl. Exposed so a fail-open-to-free composition
			// regression (M2) is machine-checkable, not just log-only.
			"billing_rail":      rt.Billing.Name(),
			"entitlements_rail": billingport.ResolverRail(rt.Entitlements),
		})
	})
}
