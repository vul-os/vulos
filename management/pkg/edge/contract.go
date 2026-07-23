// Package edge implements the WEBAPP-04 control-plane side of the Vulos edge
// cache layer: configuration of multi-region edge PoPs, per-domain cache TTL +
// rate-limit settings, and billable bandwidth metering.
//
// This package owns ONLY the control-plane record of edge state. Actual
// Caddy/nginx PoP nodes pull their config from the edge API and report
// bandwidth usage back. The on-instance app-publishing pipeline lives in the
// OSS vulos repo and is NOT rebuilt here.
//
// Store interface:
//   - EdgeNode CRUD (admin-only): UpsertNode, GetNode, ListNodes, DrainNode, UndainNode
//   - DomainConfig CRUD (per-tenant): SetDomainConfig, GetDomainConfig, ListDomainConfigs, DeleteDomainConfig
//   - Bandwidth metering: EmitBandwidthEvent, ListBandwidthEvents, BandwidthTotal
//
// MemStore defines canonical semantics; SQLStore is the production backend.
package edge

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
	ErrNodeNotFound        = errors.New("edge: node not found")
	ErrDomainNotFound      = errors.New("edge: domain config not found")
	ErrDomainAlreadyExists = errors.New("edge: domain config already exists for this ulid+domain")
	ErrInvalidConfig       = errors.New("edge: invalid config")
)

// ─────────────────────────────────────────────────────────────────────────────
// Core types
// ─────────────────────────────────────────────────────────────────────────────

// Node represents one edge PoP (pull-through cache node).
type Node struct {
	ID        string    `json:"id"`         // e.g. "iad1"
	Region    string    `json:"region"`     // e.g. "us-east"
	OriginURL string    `json:"origin_url"` // base URL edge uses to pull from instance
	Drained   bool      `json:"drained"`    // true = no new traffic sent here
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DomainConfig holds the cache + rate-limit settings for one customer domain.
type DomainConfig struct {
	ID           int64     `json:"id"`
	ULID         string    `json:"ulid"`           // customer instance ULID
	Domain       string    `json:"domain"`         // public domain name
	CacheTTLSec  int       `json:"cache_ttl_sec"`  // seconds; default 300 (5 min)
	RateLimitRPS int       `json:"rate_limit_rps"` // req/s before 429; 0 = disabled
	Enabled      bool      `json:"enabled"`        // false = edge bypass (BYO CDN)
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DefaultCacheTTLSec is the default TTL when none is specified.
const DefaultCacheTTLSec = 300

// DefaultRateLimitRPS is the default per-domain rate limit.
const DefaultRateLimitRPS = 100

// BandwidthEvent is one metered bandwidth record reported by an edge node.
type BandwidthEvent struct {
	ID          int64     `json:"id"`
	AccountID   string    `json:"account_id"`
	ULID        string    `json:"ulid"`
	Domain      string    `json:"domain"`
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// BandwidthTotals aggregates bytes for a query.
type BandwidthTotals struct {
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Store interface — frozen contract
// ─────────────────────────────────────────────────────────────────────────────

// Store is the persistence contract for the edge control-plane.
// MemStore defines semantics; SQLStore is the production backend.
type Store interface {
	// ── Edge nodes (admin) ───────────────────────────────────────────────────

	// UpsertNode creates or fully replaces an edge node record.
	UpsertNode(ctx context.Context, n Node) (Node, error)

	// GetNode returns the node with the given ID.
	// Returns ErrNodeNotFound if not present.
	GetNode(ctx context.Context, id string) (Node, error)

	// ListNodes returns all nodes, active-first, then by ID.
	ListNodes(ctx context.Context) ([]Node, error)

	// DrainNode marks a node as drained (no new traffic).
	// Returns ErrNodeNotFound if the node does not exist.
	DrainNode(ctx context.Context, id string) error

	// UndrainNode re-activates a drained node.
	// Returns ErrNodeNotFound if the node does not exist.
	UndrainNode(ctx context.Context, id string) error

	// ── Domain configs (per-tenant) ──────────────────────────────────────────

	// SetDomainConfig creates or fully replaces the domain config for a
	// (ulid, domain) pair.  CacheTTLSec and RateLimitRPS must be ≥ 0.
	SetDomainConfig(ctx context.Context, cfg DomainConfig) (DomainConfig, error)

	// GetDomainConfig returns the config for a (ulid, domain) pair.
	// Returns ErrDomainNotFound if not present.
	GetDomainConfig(ctx context.Context, ulid, domain string) (DomainConfig, error)

	// ListDomainConfigs returns all configs for a ULID, ordered by domain.
	ListDomainConfigs(ctx context.Context, ulid string) ([]DomainConfig, error)

	// DeleteDomainConfig removes a domain config.
	// Returns ErrDomainNotFound if not present.
	DeleteDomainConfig(ctx context.Context, ulid, domain string) error

	// ── Bandwidth metering ───────────────────────────────────────────────────

	// EmitBandwidthEvent appends one metered bandwidth record.
	EmitBandwidthEvent(ctx context.Context, ev BandwidthEvent) (BandwidthEvent, error)

	// ListBandwidthEvents returns bandwidth events for an account, newest first.
	// If limit ≤ 0 it defaults to 50.
	ListBandwidthEvents(ctx context.Context, accountID string, limit int) ([]BandwidthEvent, error)

	// BandwidthTotal returns the total bytes in/out for a ULID since since.
	BandwidthTotal(ctx context.Context, ulid string, since time.Time) (BandwidthTotals, error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe.
type MemStore struct {
	mu      sync.Mutex
	nodes   map[string]Node
	domains []DomainConfig
	events  []BandwidthEvent
	nextDID int64
	nextEID int64
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		nodes:   make(map[string]Node),
		nextDID: 1,
		nextEID: 1,
	}
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// ── Node methods ─────────────────────────────────────────────────────────────

func (m *MemStore) UpsertNode(_ context.Context, n Node) (Node, error) {
	if n.ID == "" || n.Region == "" || n.OriginURL == "" {
		return Node{}, ErrInvalidConfig
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.nodes[n.ID]; ok {
		n.CreatedAt = existing.CreatedAt
	} else {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	m.nodes[n.ID] = n
	return n, nil
}

func (m *MemStore) GetNode(_ context.Context, id string) (Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return Node{}, ErrNodeNotFound
	}
	return n, nil
}

func (m *MemStore) ListNodes(_ context.Context) ([]Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Drained != out[j].Drained {
			return !out[i].Drained // active first
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemStore) DrainNode(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return ErrNodeNotFound
	}
	n.Drained = true
	n.UpdatedAt = time.Now().UTC()
	m.nodes[id] = n
	return nil
}

func (m *MemStore) UndrainNode(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return ErrNodeNotFound
	}
	n.Drained = false
	n.UpdatedAt = time.Now().UTC()
	m.nodes[id] = n
	return nil
}

// ── Domain methods ────────────────────────────────────────────────────────────

func (m *MemStore) SetDomainConfig(_ context.Context, cfg DomainConfig) (DomainConfig, error) {
	if cfg.ULID == "" || cfg.Domain == "" {
		return DomainConfig{}, ErrInvalidConfig
	}
	if cfg.CacheTTLSec < 0 || cfg.RateLimitRPS < 0 {
		return DomainConfig{}, ErrInvalidConfig
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range m.domains {
		if d.ULID == cfg.ULID && d.Domain == cfg.Domain {
			cfg.ID = d.ID
			cfg.CreatedAt = d.CreatedAt
			cfg.UpdatedAt = now
			m.domains[i] = cfg
			return cfg, nil
		}
	}
	cfg.ID = m.nextDID
	m.nextDID++
	cfg.CreatedAt = now
	cfg.UpdatedAt = now
	m.domains = append(m.domains, cfg)
	return cfg, nil
}

func (m *MemStore) GetDomainConfig(_ context.Context, ulid, domain string) (DomainConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.domains {
		if d.ULID == ulid && d.Domain == domain {
			return d, nil
		}
	}
	return DomainConfig{}, ErrDomainNotFound
}

func (m *MemStore) ListDomainConfigs(_ context.Context, ulid string) ([]DomainConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DomainConfig
	for _, d := range m.domains {
		if d.ULID == ulid {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

func (m *MemStore) DeleteDomainConfig(_ context.Context, ulid, domain string) error {
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

// ── Bandwidth methods ─────────────────────────────────────────────────────────

func (m *MemStore) EmitBandwidthEvent(_ context.Context, ev BandwidthEvent) (BandwidthEvent, error) {
	if ev.AccountID == "" || ev.ULID == "" || ev.Domain == "" {
		return BandwidthEvent{}, ErrInvalidConfig
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	ev.ID = m.nextEID
	m.nextEID++
	if ev.RecordedAt.IsZero() {
		ev.RecordedAt = now
	}
	m.events = append(m.events, ev)
	return ev, nil
}

func (m *MemStore) ListBandwidthEvents(_ context.Context, accountID string, limit int) ([]BandwidthEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []BandwidthEvent
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].AccountID == accountID {
			out = append(out, m.events[i])
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemStore) BandwidthTotal(_ context.Context, ulid string, since time.Time) (BandwidthTotals, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var tot BandwidthTotals
	for _, ev := range m.events {
		if ev.ULID == ulid && !ev.PeriodEnd.Before(since) {
			tot.BytesIn += ev.BytesIn
			tot.BytesOut += ev.BytesOut
		}
	}
	return tot, nil
}

func (m *MemStore) Close() error { return nil }
