// Package dnsplane is the vulos.cloud control-plane DNS zone-management
// subsystem (see ../../DNSPLANE_API.md and roadmap/ROUTING.md).
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every dnsplane agent builds against these types, this Store interface, this
// Provider interface, the reference MemProvider (which DEFINES Provider
// semantics), and sentinel errors. Implementations must match the declared
// behaviours; handlers depend only on these interfaces.
//
// R-D risk note (from CLOUD_BUILDOUT_PLAN §5):
// v1 is an HTTP resolver API + zone-management abstraction. It MUST NOT run
// BIND/Knot/CoreDNS in-process. The Provider interface abstracts the actual
// DNS host (Cloudflare, Route53, etc.) so the choice stays swappable.
package dnsplane

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

// Record is a desired or applied DNS record in the dnsplane cache.
type Record struct {
	ID         int64
	FQDN       string
	Type       string // "A" | "CNAME" | "TXT"
	Value      string
	TTL        int
	ProviderID string // set after applied; empty until then
	ULID       string // back-reference; empty for non-ULID records
	AppliedAt  *time.Time
	State      string // "desired" | "applied" | "failed"
	LastError  string
}

// PoolMember is a geo-DNS pool entry derived from routing.PoP.
type PoolMember struct {
	PoPID, Region, IP string
	Lat, Lon          float64
	Healthy           bool
}

// Sentinel errors — handlers map these to HTTP status codes (see DNSPLANE_API.md).
var (
	ErrInvalidFQDN     = errors.New("dnsplane: invalid FQDN (must be under vulos.org)")
	ErrInvalidType     = errors.New("dnsplane: invalid record type")
	ErrProviderRefused = errors.New("dnsplane: provider refused change")
	ErrUnknownULID     = errors.New("dnsplane: unknown ULID")
)

// validFQDN reports whether fqdn is a subdomain of the deployment base domain
// (env.Domain(); non-empty prefix). Derived from env so a custom VULOS_DOMAIN
// deployment is not rejected by a hardcoded vulos.org check.
func validFQDN(fqdn string) bool {
	base := strings.ToLower(env.Domain())
	fqdn = strings.ToLower(strings.TrimSuffix(fqdn, "."))
	if fqdn == base {
		return false // apex itself is not managed here
	}
	return strings.HasSuffix(fqdn, "."+base)
}

// validType reports whether t is one of the supported DNS record types.
func validType(t string) bool {
	switch strings.ToUpper(t) {
	case "A", "CNAME", "TXT":
		return true
	}
	return false
}

// Store is the dnsplane persistence contract.
// The SQL implementation and MemStore both satisfy it; handlers depend ONLY on
// this interface. MemStore is the behavioural source of truth.
type Store interface {
	// Upsert inserts or replaces a record matched by (fqdn, type, value).
	// Returns the record with its assigned ID and updated_at populated.
	// Returns ErrInvalidFQDN / ErrInvalidType for bad input.
	Upsert(ctx context.Context, r Record) (Record, error)
	// Delete removes a record by ID.
	Delete(ctx context.Context, id int64) error
	// ByULID returns all records whose ulid field matches.
	// Returns an empty slice (not ErrUnknownULID) if none found.
	ByULID(ctx context.Context, ulid string) ([]Record, error)
	// DesiredUnapplied returns up to limit records in state "desired".
	DesiredUnapplied(ctx context.Context, limit int) ([]Record, error)
	// MarkApplied marks a record as applied and stores the provider-assigned ID.
	MarkApplied(ctx context.Context, id int64, providerID string) error
	// MarkFailed marks a record as failed and stores the error message.
	MarkFailed(ctx context.Context, id int64, errMsg string) error
	// CountApplied returns the count of records in state "applied".
	CountApplied(ctx context.Context) (int, error)
	// CountFailed returns the count of records in state "failed".
	CountFailed(ctx context.Context) (int, error)
	// Close releases the underlying database connection.
	Close() error
}

// Provider abstracts the external DNS host (Cloudflare, Route53, …).
// v1 ships a stub CloudflareProvider + a MemProvider for tests.
// The abstraction means the cp never commits to in-process authoritative DNS.
type Provider interface {
	// Name returns the provider identifier (e.g. "cloudflare", "mem").
	Name() string
	// CreateRecord creates a new record at the provider and returns its provider-assigned ID.
	// Returns ErrProviderRefused if the provider rejects the change.
	CreateRecord(ctx context.Context, r Record) (providerID string, err error)
	// UpdateRecord updates an existing record identified by r.ProviderID.
	// Returns ErrProviderRefused if the provider rejects the change.
	UpdateRecord(ctx context.Context, r Record) error
	// DeleteRecord deletes the provider record identified by providerID.
	// Returns ErrProviderRefused if the provider rejects the change.
	DeleteRecord(ctx context.Context, providerID string) error
	// PoolUpsert manages a geo-DNS pool record set (e.g. Cloudflare LB pool).
	// name is the pool FQDN (default "pop.vulos.org"); members is the full
	// desired set — the provider reconciles to this list.
	PoolUpsert(ctx context.Context, name string, members []PoolMember) error
}

// -----------------------------------------------------------------------
// MemProvider — in-memory reference Provider. Concurrency-safe.
// It DEFINES the semantics every other implementation must match.
// -----------------------------------------------------------------------

// MemProvider is the in-memory stub Provider for tests and dev environments.
type MemProvider struct {
	mu      sync.Mutex
	records map[string]Record // providerID → Record
	pool    []PoolMember
	nextID  int
}

// NewMemProvider returns an empty MemProvider.
func NewMemProvider() *MemProvider {
	return &MemProvider{
		records: map[string]Record{},
	}
}

// Name implements Provider.
func (m *MemProvider) Name() string { return "mem" }

// CreateRecord implements Provider. Assigns a synthetic provider ID.
func (m *MemProvider) CreateRecord(_ context.Context, r Record) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	pid := strings.ToLower(r.Type) + "-" + r.FQDN
	m.records[pid] = r
	return pid, nil
}

// UpdateRecord implements Provider.
func (m *MemProvider) UpdateRecord(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[r.ProviderID]; !ok {
		return ErrProviderRefused
	}
	m.records[r.ProviderID] = r
	return nil
}

// DeleteRecord implements Provider.
func (m *MemProvider) DeleteRecord(_ context.Context, providerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[providerID]; !ok {
		return ErrProviderRefused
	}
	delete(m.records, providerID)
	return nil
}

// PoolUpsert implements Provider. Replaces the in-memory pool list.
func (m *MemProvider) PoolUpsert(_ context.Context, _ string, members []PoolMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pool = make([]PoolMember, len(members))
	copy(m.pool, members)
	return nil
}

// Pool returns a snapshot of the current pool (for test assertions).
func (m *MemProvider) Pool() []PoolMember {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PoolMember, len(m.pool))
	copy(out, m.pool)
	return out
}

// Records returns a snapshot of all stored records (for test assertions).
func (m *MemProvider) Records() map[string]Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Record, len(m.records))
	for k, v := range m.records {
		out[k] = v
	}
	return out
}

// compile-time assertions
var _ Provider = (*MemProvider)(nil)

// -----------------------------------------------------------------------
// CloudflareProvider — stub backed by Cloudflare REST API v4.
// -----------------------------------------------------------------------

// CloudflareConfig holds the configuration for the Cloudflare provider.
// All secrets come from env (see DNSPLANE_API.md).
//
// When AccountID is non-empty, PoolUpsert uses the real Cloudflare Load
// Balancer API (account-scoped, requires a paid Cloudflare plan with the
// Load Balancing add-on and an account-level token with "Load Balancers: Edit"
// permission). When AccountID is empty, PoolUpsert falls back to individual
// A records (the v1 behaviour).
type CloudflareConfig struct {
	APIToken   string       // env DNSPLANE_CF_TOKEN
	ZoneID     string       // env DNSPLANE_CF_ZONE_ID (the vulos.org zone)
	PoolName   string       // env DNSPLANE_POOL_NAME (default "pop.vulos.org")
	AccountID  string       // env DNSPLANE_CF_ACCOUNT_ID (required for LB pool semantics)
	HTTPClient *http.Client // optional; defaults to http.DefaultClient
}

// CloudflareProvider implements Provider using the Cloudflare v4 REST API.
// It uses plain net/http — no external SDK added as a dependency.
type CloudflareProvider struct {
	cfg CloudflareConfig
	hc  *http.Client
}

// NewCloudflareProvider validates cfg and returns a ready *CloudflareProvider.
// Returns an error if APIToken or ZoneID are empty.
func NewCloudflareProvider(cfg CloudflareConfig) (*CloudflareProvider, error) {
	if cfg.APIToken == "" {
		return nil, errors.New("dnsplane: DNSPLANE_CF_TOKEN required")
	}
	if cfg.ZoneID == "" {
		return nil, errors.New("dnsplane: DNSPLANE_CF_ZONE_ID required")
	}
	if cfg.PoolName == "" {
		cfg.PoolName = "pop." + env.Domain()
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &CloudflareProvider{cfg: cfg, hc: hc}, nil
}

// Name implements Provider.
func (c *CloudflareProvider) Name() string { return "cloudflare" }

// compile-time assertion
var _ Provider = (*CloudflareProvider)(nil)
