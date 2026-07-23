// Package cdn implements BYO-CDN configuration and origin-firewall support
// for vulos.cloud WEBAPP-05.
//
// Control-plane responsibilities:
//   - Store per-account CDN provider selection (Cloudflare/Fastly/Bunny).
//   - Fetch and cache the CDN's published IP ranges.
//   - Provide the effective firewall rule set (allowed CIDRs + Host header) to
//     the OS agent when it applies origin-firewall rules.
//   - Enforce the Pro+ tier gate.
//
// The OS agent enforces the actual iptables/nftables rules; the control plane
// is the authoritative source for the rule set.
package cdn

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"time"
)

// Provider identifies a supported CDN vendor.
type Provider string

const (
	ProviderCloudflare Provider = "cloudflare"
	ProviderFastly     Provider = "fastly"
	ProviderBunny      Provider = "bunny"
)

// ValidProvider reports whether p is a supported CDN provider.
func ValidProvider(p Provider) bool {
	return p == ProviderCloudflare || p == ProviderFastly || p == ProviderBunny
}

// Config is a per-account BYO CDN configuration record.
type Config struct {
	AccountID   string    `json:"account_id"`
	Provider    Provider  `json:"provider"`
	OriginHost  string    `json:"origin_host"`  // e.g. "origin-<cust>.vulos.cloud"
	MTLSEnabled bool      `json:"mtls_enabled"` // authenticated-origin-pulls
	HostHeader  string    `json:"host_header"`  // Host header to enforce on origin requests
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FirewallRules is the computed rule set for an account's origin firewall.
type FirewallRules struct {
	AccountID   string    `json:"account_id"`
	Provider    Provider  `json:"provider"`
	AllowCIDRs  []string  `json:"allow_cidrs"`  // CDN egress CIDRs to allowlist
	HostHeader  string    `json:"host_header"`  // Host header to validate
	MTLSEnabled bool      `json:"mtls_enabled"` // require client cert from CDN
	FetchedAt   time.Time `json:"fetched_at"`
}

// IPRange is a cached CDN IP CIDR entry.
type IPRange struct {
	Provider  Provider  `json:"provider"`
	CIDR      string    `json:"cidr"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Sentinel errors.
var (
	ErrNotFound        = errors.New("cdn: config not found")
	ErrInvalidProvider = errors.New("cdn: invalid provider (must be cloudflare|fastly|bunny)")
	ErrOriginRequired  = errors.New("cdn: origin_host is required")
	ErrNotPro          = errors.New("cdn: BYO CDN requires Pro or higher tier")
)

// Store is the cdn persistence contract.
// MemStore and SQLStore both satisfy it.
type Store interface {
	// GetConfig returns the BYO-CDN config for an account.
	// Returns ErrNotFound if no config exists.
	GetConfig(ctx context.Context, accountID string) (Config, error)

	// SetConfig creates or updates the BYO-CDN config for an account.
	SetConfig(ctx context.Context, cfg Config) error

	// DeleteConfig removes the BYO-CDN config for an account.
	// Returns ErrNotFound if no config exists.
	DeleteConfig(ctx context.Context, accountID string) error

	// GetIPRanges returns cached CDN IP ranges for the given provider.
	GetIPRanges(ctx context.Context, provider Provider) ([]IPRange, error)

	// SetIPRanges replaces all cached IP ranges for the given provider.
	// FetchedAt is set to now on all entries.
	SetIPRanges(ctx context.Context, provider Provider, cidrs []string) error

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe.
type MemStore struct {
	mu       sync.Mutex
	configs  map[string]Config // key: accountID
	ipRanges map[Provider][]IPRange
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		configs:  map[string]Config{},
		ipRanges: map[Provider][]IPRange{},
	}
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

func (m *MemStore) GetConfig(_ context.Context, accountID string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.configs[accountID]
	if !ok {
		return Config{}, ErrNotFound
	}
	return cfg, nil
}

func (m *MemStore) SetConfig(_ context.Context, cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if cfg.CreatedAt.IsZero() {
		if existing, ok := m.configs[cfg.AccountID]; ok {
			cfg.CreatedAt = existing.CreatedAt
		} else {
			cfg.CreatedAt = now
		}
	}
	cfg.UpdatedAt = now
	m.configs[cfg.AccountID] = cfg
	return nil
}

func (m *MemStore) DeleteConfig(_ context.Context, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.configs[accountID]; !ok {
		return ErrNotFound
	}
	delete(m.configs, accountID)
	return nil
}

func (m *MemStore) GetIPRanges(_ context.Context, provider Provider) ([]IPRange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ranges := m.ipRanges[provider]
	out := make([]IPRange, len(ranges))
	copy(out, ranges)
	return out, nil
}

func (m *MemStore) SetIPRanges(_ context.Context, provider Provider, cidrs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	out := make([]IPRange, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, IPRange{Provider: provider, CIDR: c, FetchedAt: now})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CIDR < out[j].CIDR })
	m.ipRanges[provider] = out
	return nil
}

func (m *MemStore) Close() error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// FirewallRules builder
// ─────────────────────────────────────────────────────────────────────────────

// BuildFirewallRules assembles the FirewallRules for an account from its
// Config and the cached IP ranges for its provider.
// Returns ErrNotFound if the account has no BYO-CDN config or it is disabled.
func BuildFirewallRules(ctx context.Context, st Store, accountID string) (FirewallRules, error) {
	cfg, err := st.GetConfig(ctx, accountID)
	if err != nil {
		return FirewallRules{}, err
	}
	if !cfg.Enabled {
		return FirewallRules{}, ErrNotFound
	}

	ranges, err := st.GetIPRanges(ctx, cfg.Provider)
	if err != nil {
		return FirewallRules{}, err
	}

	cidrs := make([]string, 0, len(ranges))
	var fetchedAt time.Time
	for _, r := range ranges {
		cidrs = append(cidrs, r.CIDR)
		if r.FetchedAt.After(fetchedAt) {
			fetchedAt = r.FetchedAt
		}
	}

	return FirewallRules{
		AccountID:   accountID,
		Provider:    cfg.Provider,
		AllowCIDRs:  cidrs,
		HostHeader:  cfg.HostHeader,
		MTLSEnabled: cfg.MTLSEnabled,
		FetchedAt:   fetchedAt,
	}, nil
}

// ValidateCIDRs returns the CIDRs that failed net.ParseCIDR validation.
func ValidateCIDRs(cidrs []string) []string {
	var bad []string
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			bad = append(bad, c)
		}
	}
	return bad
}
