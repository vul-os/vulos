// Package webapp implements the control-plane side of the Vulos webapp
// publishing pipeline (WEBAPP-03) and per-app failure modes, circuit breaker,
// and dashboard telemetry (WEBAPP-06).
//
// WEBAPP-03 scope (cloud-side only):
//   - Track domain-connect + NS delegation state per customer instance.
//   - Record when TLS has been auto-provisioned by the existing acme-dns/wildcard flow.
//   - Store the runtime publish/unpublish toggle (manifest declares publishable;
//     dashboard flips public/private live).
//   - Support both "native" and "oci" app kinds through the same pipeline.
//
// WEBAPP-06 scope (cloud-side only):
//   - Persist per-app failure mode + quota settings (throttle|kill|burst).
//   - Store and serve circuit-breaker state per (account, app) pair.
//   - Accept and serve per-app telemetry snapshots (req/min, p50/p99, error rate,
//     CPU%, mem MB, last throttle/kill/burst events).
//   - Dashboard endpoints: GET list, PUT config, PATCH failure-mode/quota,
//     GET metrics, OS-agent metric ingest, GET/PUT circuit-breaker state.
//
// The on-instance cgroup enforcement and the actual Caddy/nginx edge are OSS vulos.
// This package owns only the control-plane policy and state record.
//
// MemStore defines canonical semantics; SQLStore is the production backend.
package webapp

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	ErrDomainNotFound      = errors.New("webapp: domain not found")
	ErrDomainAlreadyExists = errors.New("webapp: domain already connected for this ulid")
	ErrInvalidInput        = errors.New("webapp: invalid input")
)

// ─────────────────────────────────────────────────────────────────────────────
// Core types
// ─────────────────────────────────────────────────────────────────────────────

// AppKind distinguishes Vulos-native apps from customer-pushed OCI containers.
type AppKind string

const (
	AppKindNative AppKind = "native"
	AppKindOCI    AppKind = "oci"
)

// Domain is the control-plane record for one customer-published domain.
type Domain struct {
	ID        int64  `json:"id"`
	AccountID string `json:"account_id"`
	ULID      string `json:"ulid"`   // customer instance ULID
	Domain    string `json:"domain"` // e.g. "myapp.example.com"

	// NS delegation state (customer delegates to ns1.vulos.cloud).
	NSDelegated  bool       `json:"ns_delegated"`
	NSVerifiedAt *time.Time `json:"ns_verified_at,omitempty"`

	// TLS state (auto-provisioned via acme-dns / wildcard flow).
	TLSProvisioned bool       `json:"tls_provisioned"`
	TLSIssuedAt    *time.Time `json:"tls_issued_at,omitempty"`

	// Runtime publish toggle: manifest declares publishable; dashboard flips.
	Published bool `json:"published"`

	// App kind: native (Vulos app) or oci (container).
	AppKind AppKind `json:"app_kind"`

	// Edge config inherited by the edge cache (WEBAPP-04).
	CacheTTLSec  int `json:"cache_ttl_sec"`
	RateLimitRPS int `json:"rate_limit_rps"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultCacheTTLSec is the default cache TTL (5 min, matching WEBAPP-04).
const DefaultCacheTTLSec = 300

// DefaultRateLimitRPS is the default per-domain rate limit (matching WEBAPP-04).
const DefaultRateLimitRPS = 100

// ConnectDomainRequest carries the fields for a new domain connection.
type ConnectDomainRequest struct {
	AccountID    string  `json:"account_id"`
	ULID         string  `json:"ulid"`
	Domain       string  `json:"domain"`
	AppKind      AppKind `json:"app_kind"`       // defaults to "native" if empty
	CacheTTLSec  int     `json:"cache_ttl_sec"`  // 0 → DefaultCacheTTLSec
	RateLimitRPS int     `json:"rate_limit_rps"` // 0 → DefaultRateLimitRPS
}

// ─────────────────────────────────────────────────────────────────────────────
// Store interface — frozen contract
// ─────────────────────────────────────────────────────────────────────────────

// Store is the persistence contract for the webapp publishing pipeline.
// MemStore defines semantics; SQLStore is the production backend.
type Store interface {
	// ConnectDomain registers a new domain for a customer instance.
	// Returns ErrDomainAlreadyExists if (ulid, domain) already exists.
	// Returns ErrInvalidInput if required fields are empty.
	ConnectDomain(ctx context.Context, req ConnectDomainRequest) (Domain, error)

	// GetDomain returns the Domain record for a (ulid, domain) pair.
	// Returns ErrDomainNotFound if not present.
	GetDomain(ctx context.Context, ulid, domain string) (Domain, error)

	// ListDomains returns all domains for a ULID, ordered by domain name.
	ListDomains(ctx context.Context, ulid string) ([]Domain, error)

	// ListDomainsByAccount returns all domains for an account, ordered by domain.
	ListDomainsByAccount(ctx context.Context, accountID string) ([]Domain, error)

	// DeleteDomain removes the domain record.
	// Returns ErrDomainNotFound if not present.
	DeleteDomain(ctx context.Context, ulid, domain string) error

	// SetNSDelegated marks NS delegation confirmed (or revoked) for a domain.
	// Returns ErrDomainNotFound if not present.
	SetNSDelegated(ctx context.Context, ulid, domain string, delegated bool) error

	// SetTLSProvisioned marks TLS as provisioned (cert issued) for a domain.
	// Returns ErrDomainNotFound if not present.
	SetTLSProvisioned(ctx context.Context, ulid, domain string, provisioned bool) error

	// SetPublished flips the runtime publish/unpublish toggle for a domain.
	// Returns ErrDomainNotFound if not present.
	SetPublished(ctx context.Context, ulid, domain string, published bool) error

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe.
type MemStore struct {
	mu      sync.Mutex
	domains []Domain
	nextID  int64
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{nextID: 1}
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

func (m *MemStore) ConnectDomain(_ context.Context, req ConnectDomainRequest) (Domain, error) {
	if req.AccountID == "" || req.ULID == "" || req.Domain == "" {
		return Domain{}, ErrInvalidInput
	}
	if req.AppKind == "" {
		req.AppKind = AppKindNative
	}
	if req.AppKind != AppKindNative && req.AppKind != AppKindOCI {
		return Domain{}, ErrInvalidInput
	}
	if req.CacheTTLSec <= 0 {
		req.CacheTTLSec = DefaultCacheTTLSec
	}
	if req.RateLimitRPS <= 0 {
		req.RateLimitRPS = DefaultRateLimitRPS
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.domains {
		if d.ULID == req.ULID && d.Domain == req.Domain {
			return Domain{}, ErrDomainAlreadyExists
		}
	}
	d := Domain{
		ID:           m.nextID,
		AccountID:    req.AccountID,
		ULID:         req.ULID,
		Domain:       req.Domain,
		AppKind:      req.AppKind,
		CacheTTLSec:  req.CacheTTLSec,
		RateLimitRPS: req.RateLimitRPS,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.nextID++
	m.domains = append(m.domains, d)
	return d, nil
}

func (m *MemStore) GetDomain(_ context.Context, ulid, domain string) (Domain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.domains {
		if d.ULID == ulid && d.Domain == domain {
			return d, nil
		}
	}
	return Domain{}, ErrDomainNotFound
}

func (m *MemStore) ListDomains(_ context.Context, ulid string) ([]Domain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Domain
	for _, d := range m.domains {
		if d.ULID == ulid {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

func (m *MemStore) ListDomainsByAccount(_ context.Context, accountID string) ([]Domain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Domain
	for _, d := range m.domains {
		if d.AccountID == accountID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

func (m *MemStore) DeleteDomain(_ context.Context, ulid, domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range m.domains {
		if d.ULID == ulid && d.Domain == domain {
			m.domains = append(m.domains[:i], m.domains[i+1:]...)
			return nil
		}
	}
	return ErrDomainNotFound
}

func (m *MemStore) SetNSDelegated(_ context.Context, ulid, domain string, delegated bool) error {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range m.domains {
		if d.ULID == ulid && d.Domain == domain {
			d.NSDelegated = delegated
			if delegated {
				d.NSVerifiedAt = &now
			} else {
				d.NSVerifiedAt = nil
			}
			d.UpdatedAt = now
			m.domains[i] = d
			return nil
		}
	}
	return ErrDomainNotFound
}

func (m *MemStore) SetTLSProvisioned(_ context.Context, ulid, domain string, provisioned bool) error {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range m.domains {
		if d.ULID == ulid && d.Domain == domain {
			d.TLSProvisioned = provisioned
			if provisioned {
				d.TLSIssuedAt = &now
			} else {
				d.TLSIssuedAt = nil
			}
			d.UpdatedAt = now
			m.domains[i] = d
			return nil
		}
	}
	return ErrDomainNotFound
}

func (m *MemStore) SetPublished(_ context.Context, ulid, domain string, published bool) error {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range m.domains {
		if d.ULID == ulid && d.Domain == domain {
			d.Published = published
			d.UpdatedAt = now
			m.domains[i] = d
			return nil
		}
	}
	return ErrDomainNotFound
}

func (m *MemStore) Close() error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// WEBAPP-06: Failure modes, circuit breaker state, per-app telemetry
// ─────────────────────────────────────────────────────────────────────────────

// FailureMode controls how the edge and OS respond when an app exceeds its quota.
type FailureMode string

const (
	// FailureThrottle: edge serves a static "high traffic" page above quota.
	// The app process stays up; no restart is triggered.
	FailureThrottle FailureMode = "throttle"
	// FailureKill: OS kills the process on OOM; restarts with exponential back-off.
	// Edge serves a "temporarily unavailable" page during the restart cycle.
	FailureKill FailureMode = "kill"
	// FailureBurst: app may consume headroom from burst.slice if available.
	// Excess consumption is metered and billed per CPU-/GB-second.
	FailureBurst FailureMode = "burst"
)

// ValidFailureMode reports whether m is a known failure mode.
func ValidFailureMode(m FailureMode) bool {
	return m == FailureThrottle || m == FailureKill || m == FailureBurst
}

// AppConfig is the control-plane record for one app's failure mode + quota.
// Key: (AccountID, AppID). AppID is the app's ULID or stable identifier.
type AppConfig struct {
	AccountID string `json:"account_id"`
	AppID     string `json:"app_id"`
	AppName   string `json:"app_name,omitempty"`

	// FailureMode is one of throttle|kill|burst.
	FailureMode FailureMode `json:"failure_mode"`

	// Quota — friendly units (never raw cgroup values in the UI).
	// Default: 0.5 CPU / 512 MB / 100 conns.
	QuotaCPUMillis int `json:"quota_cpu_millis"` // e.g. 500 = 0.5 core
	QuotaMemMB     int `json:"quota_mem_mb"`     // e.g. 512
	QuotaConnLimit int `json:"quota_conn_limit"` // max concurrent HTTP conns

	// Circuit-breaker configuration.
	// CBThresholdErrPct: error-rate % that trips the breaker (0 = disabled).
	CBThresholdErrPct float64 `json:"cb_threshold_err_pct,omitempty"`
	// CBCooldownSec: seconds the breaker holds open before auto-reset.
	CBCooldownSec int `json:"cb_cooldown_sec,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Default quota values per WEBAPPS.md.
const (
	DefaultQuotaCPUMillis = 500
	DefaultQuotaMemMB     = 512
	DefaultQuotaConnLimit = 100
	DefaultCBThresholdPct = 25.0
	DefaultCBCooldownSec  = 60
)

// CBState tracks the edge-side circuit breaker for one app.
type CBState string

const (
	// CBClosed: normal operation; traffic flows to the app.
	CBClosed CBState = "closed"
	// CBOpen: breaker tripped; edge serves static page.
	CBOpen CBState = "open"
	// CBHalfOpen: cooldown expired; edge allows one probe request.
	CBHalfOpen CBState = "half_open"
)

// CircuitBreakerRecord holds the persisted circuit-breaker state for one app.
type CircuitBreakerRecord struct {
	AccountID string    `json:"account_id"`
	AppID     string    `json:"app_id"`
	State     CBState   `json:"state"`
	TrippedAt time.Time `json:"tripped_at,omitempty"`
	ResetAt   time.Time `json:"reset_at,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AppMetrics is a telemetry snapshot pushed by the OS agent for one app.
// Values use friendly units matching the dashboard display.
type AppMetrics struct {
	ID        int64     `json:"id"`
	AccountID string    `json:"account_id"`
	AppID     string    `json:"app_id"`
	SampledAt time.Time `json:"sampled_at"`

	// Traffic
	ReqPerMin    float64 `json:"req_per_min"`
	P50Ms        float64 `json:"p50_ms"`
	P99Ms        float64 `json:"p99_ms"`
	ErrorRatePct float64 `json:"error_rate_pct"`

	// Resource usage (as % of quota at sample time)
	CPUPct float64 `json:"cpu_pct"`
	MemMB  float64 `json:"mem_mb"`

	// Quota at sample time (for headroom display)
	QuotaCPUMillis int `json:"quota_cpu_millis"`
	QuotaMemMB     int `json:"quota_mem_mb"`

	// Last failure-mode events
	LastThrottleAt *time.Time `json:"last_throttle_at,omitempty"`
	LastKillAt     *time.Time `json:"last_kill_at,omitempty"`
	LastBurstAt    *time.Time `json:"last_burst_at,omitempty"`
}

// AppConfigStore extends Store with the WEBAPP-06 persistence methods.
// Implemented by MemStore and (future) SQLStore extension.
type AppConfigStore interface {
	Store

	// SetAppConfig creates or fully replaces the config for a (account, app) pair.
	SetAppConfig(ctx context.Context, cfg AppConfig) (AppConfig, error)

	// GetAppConfig returns the config for a (account, app) pair.
	// Returns ErrInvalidInput if not found.
	GetAppConfig(ctx context.Context, accountID, appID string) (AppConfig, error)

	// ListAppConfigs returns all app configs for an account, ordered by app_id.
	ListAppConfigs(ctx context.Context, accountID string) ([]AppConfig, error)

	// DeleteAppConfig removes the config row for a (account, app) pair.
	DeleteAppConfig(ctx context.Context, accountID, appID string) error

	// SetCBState upserts the circuit-breaker record.
	SetCBState(ctx context.Context, rec CircuitBreakerRecord) error

	// GetCBState returns the circuit-breaker record.
	// Returns ErrInvalidInput if not found (caller should treat as CBClosed).
	GetCBState(ctx context.Context, accountID, appID string) (CircuitBreakerRecord, error)

	// RecordMetrics appends a telemetry snapshot (ID assigned by store).
	RecordMetrics(ctx context.Context, m AppMetrics) (AppMetrics, error)

	// LatestMetrics returns the most-recent snapshot for a (account, app) pair.
	// Returns ErrInvalidInput if none recorded.
	LatestMetrics(ctx context.Context, accountID, appID string) (AppMetrics, error)

	// ListMetrics returns recent snapshots, newest first. Limit ≤ 0 → 50.
	ListMetrics(ctx context.Context, accountID, appID string, limit int) ([]AppMetrics, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore WEBAPP-06 extension
// ─────────────────────────────────────────────────────────────────────────────

// appCfgKey returns the map key for a (accountID, appID) pair.
func appCfgKey(accountID, appID string) string { return accountID + "\x00" + appID }

// Extend MemStore with WEBAPP-06 fields.
// NOTE: Because Go does not allow embedding new fields after the type is
// declared, we store these on a parallel per-instance sidecar struct that is
// lazily initialised via the existing mu.
//
// We achieve this cleanly by adding a parallel map on the MemStore.
// The MemStore already has mu+domains; we piggyback additional maps.
// This avoids modifying the MemStore struct (which would be a breaking change
// if tests have already been compiled against it), so instead we use a
// separate extended memory store that embeds MemStore.

// ExtMemStore is an in-memory store that satisfies AppConfigStore.
// Use NewExtMemStore for tests and wire_stores.go.
// When sqlDelegate is set, WEBAPP-03 domain operations are proxied to it;
// WEBAPP-06 config/metrics/CB remain in-memory until SQL migration is added.
type ExtMemStore struct {
	*MemStore
	sqlDelegate Store // optional: proxies domain ops when non-nil
	cfgMu       sync.Mutex
	configs     map[string]AppConfig
	cbMap       map[string]CircuitBreakerRecord
	mets        []AppMetrics
	nextMID     int64
}

// NewExtMemStore returns a fully initialised ExtMemStore backed by an
// in-memory MemStore for both WEBAPP-03 domain ops and WEBAPP-06 ops.
func NewExtMemStore() *ExtMemStore {
	return &ExtMemStore{
		MemStore: NewMemStore(),
		configs:  make(map[string]AppConfig),
		cbMap:    make(map[string]CircuitBreakerRecord),
		nextMID:  1,
	}
}

// NewExtMemStoreWithSQL returns an ExtMemStore that uses sqlSt for WEBAPP-03
// domain ops and its own in-memory layer for WEBAPP-06 config/metrics/CB.
// This allows gradual migration: WEBAPP-03 is persisted in SQLite; WEBAPP-06
// data is in-memory until a SQL migration is added.
func NewExtMemStoreWithSQL(sqlSt Store) *ExtMemStore {
	return &ExtMemStore{
		MemStore: &MemStore{
			// Use a sentinel that delegates all domain calls to sqlSt.
			// We achieve this by storing sqlSt as the underlying store
			// and overriding the domain methods via composition.
			// Since Go does not support selective override of embedded
			// methods, we store sqlSt separately and proxy.
			domains: nil, // unused when sqlDelegate is set
			nextID:  1,
		},
		sqlDelegate: sqlSt,
		configs:     make(map[string]AppConfig),
		cbMap:       make(map[string]CircuitBreakerRecord),
		nextMID:     1,
	}
}

// compile-time assertion: ExtMemStore satisfies AppConfigStore.
var _ AppConfigStore = (*ExtMemStore)(nil)

// ── WEBAPP-03 domain proxy methods ────────────────────────────────────────────
// When sqlDelegate is set, delegate all domain ops to it; otherwise fall
// through to the embedded MemStore.

func (e *ExtMemStore) ConnectDomain(ctx context.Context, req ConnectDomainRequest) (Domain, error) {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.ConnectDomain(ctx, req)
	}
	return e.MemStore.ConnectDomain(ctx, req)
}

func (e *ExtMemStore) GetDomain(ctx context.Context, ulid, domain string) (Domain, error) {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.GetDomain(ctx, ulid, domain)
	}
	return e.MemStore.GetDomain(ctx, ulid, domain)
}

func (e *ExtMemStore) ListDomains(ctx context.Context, ulid string) ([]Domain, error) {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.ListDomains(ctx, ulid)
	}
	return e.MemStore.ListDomains(ctx, ulid)
}

func (e *ExtMemStore) ListDomainsByAccount(ctx context.Context, accountID string) ([]Domain, error) {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.ListDomainsByAccount(ctx, accountID)
	}
	return e.MemStore.ListDomainsByAccount(ctx, accountID)
}

func (e *ExtMemStore) DeleteDomain(ctx context.Context, ulid, domain string) error {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.DeleteDomain(ctx, ulid, domain)
	}
	return e.MemStore.DeleteDomain(ctx, ulid, domain)
}

func (e *ExtMemStore) SetNSDelegated(ctx context.Context, ulid, domain string, delegated bool) error {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.SetNSDelegated(ctx, ulid, domain, delegated)
	}
	return e.MemStore.SetNSDelegated(ctx, ulid, domain, delegated)
}

func (e *ExtMemStore) SetTLSProvisioned(ctx context.Context, ulid, domain string, provisioned bool) error {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.SetTLSProvisioned(ctx, ulid, domain, provisioned)
	}
	return e.MemStore.SetTLSProvisioned(ctx, ulid, domain, provisioned)
}

func (e *ExtMemStore) SetPublished(ctx context.Context, ulid, domain string, published bool) error {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.SetPublished(ctx, ulid, domain, published)
	}
	return e.MemStore.SetPublished(ctx, ulid, domain, published)
}

func (e *ExtMemStore) Close() error {
	if e.sqlDelegate != nil {
		return e.sqlDelegate.Close()
	}
	return e.MemStore.Close()
}

// ── WEBAPP-06 config/metrics/CB ───────────────────────────────────────────────

func (e *ExtMemStore) SetAppConfig(_ context.Context, cfg AppConfig) (AppConfig, error) {
	if cfg.AccountID == "" || cfg.AppID == "" {
		return AppConfig{}, ErrInvalidInput
	}
	if cfg.FailureMode != "" && !ValidFailureMode(cfg.FailureMode) {
		return AppConfig{}, ErrInvalidInput
	}
	now := time.Now().UTC()
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	key := appCfgKey(cfg.AccountID, cfg.AppID)
	if existing, ok := e.configs[key]; ok {
		cfg.CreatedAt = existing.CreatedAt
	} else {
		cfg.CreatedAt = now
	}
	cfg.UpdatedAt = now
	e.configs[key] = cfg
	return cfg, nil
}

func (e *ExtMemStore) GetAppConfig(_ context.Context, accountID, appID string) (AppConfig, error) {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	cfg, ok := e.configs[appCfgKey(accountID, appID)]
	if !ok {
		return AppConfig{}, ErrInvalidInput
	}
	return cfg, nil
}

func (e *ExtMemStore) ListAppConfigs(_ context.Context, accountID string) ([]AppConfig, error) {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	var out []AppConfig
	for _, cfg := range e.configs {
		if cfg.AccountID == accountID {
			out = append(out, cfg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out, nil
}

func (e *ExtMemStore) DeleteAppConfig(_ context.Context, accountID, appID string) error {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	key := appCfgKey(accountID, appID)
	if _, ok := e.configs[key]; !ok {
		return ErrInvalidInput
	}
	delete(e.configs, key)
	return nil
}

func (e *ExtMemStore) SetCBState(_ context.Context, rec CircuitBreakerRecord) error {
	if rec.AccountID == "" || rec.AppID == "" {
		return ErrInvalidInput
	}
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	rec.UpdatedAt = time.Now().UTC()
	e.cbMap[appCfgKey(rec.AccountID, rec.AppID)] = rec
	return nil
}

func (e *ExtMemStore) GetCBState(_ context.Context, accountID, appID string) (CircuitBreakerRecord, error) {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	rec, ok := e.cbMap[appCfgKey(accountID, appID)]
	if !ok {
		return CircuitBreakerRecord{}, ErrInvalidInput
	}
	return rec, nil
}

func (e *ExtMemStore) RecordMetrics(_ context.Context, m AppMetrics) (AppMetrics, error) {
	if m.AccountID == "" || m.AppID == "" {
		return AppMetrics{}, ErrInvalidInput
	}
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	m.ID = e.nextMID
	e.nextMID++
	if m.SampledAt.IsZero() {
		m.SampledAt = time.Now().UTC()
	}
	e.mets = append(e.mets, m)
	return m, nil
}

func (e *ExtMemStore) LatestMetrics(_ context.Context, accountID, appID string) (AppMetrics, error) {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	for i := len(e.mets) - 1; i >= 0; i-- {
		if e.mets[i].AccountID == accountID && e.mets[i].AppID == appID {
			return e.mets[i], nil
		}
	}
	return AppMetrics{}, ErrInvalidInput
}

func (e *ExtMemStore) ListMetrics(_ context.Context, accountID, appID string, limit int) ([]AppMetrics, error) {
	if limit <= 0 {
		limit = 50
	}
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	var out []AppMetrics
	for i := len(e.mets) - 1; i >= 0; i-- {
		if e.mets[i].AccountID == accountID && e.mets[i].AppID == appID {
			out = append(out, e.mets[i])
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
