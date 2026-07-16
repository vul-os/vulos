// Package cpserver is the deployment-agnostic builder for the Vulos control
// plane. It turns a generic, host-neutral Config plus a set of injected provider
// seams (Deps) into a running HTTP control plane — WITHOUT any knowledge of a
// specific host, payment processor, or object-storage vendor.
//
// The contract is deliberately small:
//
//	srv, err := cpserver.New(cfg, deps)   // cfg = generic config, deps = seams
//	err = srv.Run(ctx)                    // serve until ctx is cancelled
//
//   - Config is populated from plain env/flags/file by whoever builds the
//     server. It carries NO provider-specific knowledge (no Fly, no the managed store, no
//     Paystack) — just an address, a domain, a database DSN, and the like.
//   - Deps carries the pluggable providers. A self-hoster leaves them nil and
//     New fills in the free no-op / bring-your-own defaults from pkg/billingport
//     and pkg/storageport. A commercial distributor injects real
//     implementations (a payment rail, a tier/quota resolver, a bucket
//     provisioner) built in its own module.
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

	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cpdb"
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

	// DatabaseDSN selects the control-plane database. When empty the server
	// opens a local SQLite database (DBSQLitePath, or an in-memory DB) — the
	// zero-config self-host default. A Postgres DSN selects Postgres.
	DatabaseDSN string

	// DBLogicalName is the logical schema/namespace passed to the database
	// backend (Postgres schema name). Defaults to "cp".
	DBLogicalName string

	// DBSQLitePath is the on-disk SQLite path used when DatabaseDSN is empty.
	// Empty means an in-memory database (non-durable; dev/smoke only).
	DBSQLitePath string

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
	if c.DBLogicalName == "" {
		c.DBLogicalName = "cp"
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
}

// Runtime is the shared state a RouteRegistrar builds handlers against. It
// exposes the resolved config, the database handle, the injected seams, and the
// logger. Extend it (not the New signature) when a new shared collaborator is
// added.
type Runtime struct {
	Config             Config
	DB                 *cpdb.DB
	Billing            billingport.BillingProvider
	Entitlements       billingport.EntitlementResolver
	StorageProvisioner storageport.StorageProvisioner
	Logger             *slog.Logger
}

// Server is an assembled control plane ready to Run.
type Server struct {
	cfg     Config
	rt      *Runtime
	http    *http.Server
	onStart []func(ctx context.Context, rt *Runtime)
}

// New assembles a control plane from generic config and injected seams. Nil
// Deps fields are filled with the free self-host defaults. New opens the
// configured database, mounts the always-on operational endpoints, then runs
// each provided RouteRegistrar in order.
func New(cfg Config, deps Deps) (*Server, error) {
	cfg.withDefaults()
	deps.withDefaults()

	db, err := openDB(cfg)
	if err != nil {
		return nil, fmt.Errorf("cpserver: open database: %w", err)
	}

	rt := &Runtime{
		Config:             cfg,
		DB:                 db,
		Billing:            deps.Billing,
		Entitlements:       deps.Entitlements,
		StorageProvisioner: deps.StorageProvisioner,
		Logger:             deps.Logger,
	}

	mux := http.NewServeMux()
	mountOperational(mux, rt)
	for i, reg := range deps.Routes {
		if reg == nil {
			continue
		}
		if err := reg(mux, rt); err != nil {
			return nil, fmt.Errorf("cpserver: route registrar #%d: %w", i, err)
		}
	}

	return &Server{
		cfg: cfg,
		rt:  rt,
		http: &http.Server{
			Addr:         cfg.Addr,
			Handler:      mux,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		},
		onStart: deps.OnStart,
	}, nil
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

// Close releases server resources (the database handle).
func (s *Server) Close() error {
	if s.rt != nil && s.rt.DB != nil {
		return s.rt.DB.Close()
	}
	return nil
}

// openDB opens the control-plane database per Config. Empty DatabaseDSN selects
// SQLite (on-disk when DBSQLitePath is set, else in-memory) — the zero-config
// self-host default. A non-empty DSN is passed to the Postgres backend via the
// standard DATABASE_URL env the cpdb runner honours.
func openDB(cfg Config) (*cpdb.DB, error) {
	if cfg.DatabaseDSN == "" {
		dsn := cfg.DBSQLitePath
		if dsn == "" {
			dsn = ":memory:"
		}
		return cpdb.OpenSQLiteDSN(dsn)
	}
	// The Postgres backend reads its DSN from DATABASE_URL; set it for this
	// process so cpdb.Open selects Postgres with the configured DSN.
	_ = os.Setenv("DATABASE_URL", cfg.DatabaseDSN)
	return cpdb.Open(cfg.DBLogicalName)
}

// mountOperational installs the always-on operational endpoints every control
// plane exposes regardless of deployment: liveness, readiness, and version.
func mountOperational(mux *http.ServeMux, rt *Runtime) {
	// GET /healthz — lightweight liveness probe (always 200 once serving).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
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
			"version":      rt.Config.Version,
			"environment":  rt.Config.Environment,
			"domain":       rt.Config.Domain,
			"billing_rail": rt.Billing.Name(),
		})
	})
}
