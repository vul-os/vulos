// Package residency implements COMPLY-01: per-org data-residency region as a
// first-class control-plane setting.
//
// The region is stored in org_residency (SQLite, WAL, no-CGO) and is wired
// into bucket provisioning so new managed the managed store buckets land in the chosen
// region instead of "auto".
//
// Supported regions and data-flow annotations are published via DataFlowMap()
// so the dashboard and the public /api/residency/data-flows endpoint can
// surface them.
package residency

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrUnknownOrg is returned when no residency row exists for the given org.
	ErrUnknownOrg = errors.New("residency: unknown org")
	// ErrInvalidRegion is returned when the supplied region slug is not in the
	// supported set.
	ErrInvalidRegion = errors.New("residency: invalid or unsupported region")
	// ErrRegionLocked is returned when an attempt is made to CHANGE an org's
	// residency region after it was first set. The org region (home_region) is
	// immutable after first provisioning — flipping it freely across the several
	// region stores caused split-brain (COMPLY-01). Re-setting the SAME region is
	// an idempotent no-op; changing it is rejected. A deliberate, audited
	// migration must go through a separate operator path (not yet implemented).
	ErrRegionLocked = errors.New("residency: org region is immutable after first set")
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// OrgRegion is the per-org data-residency setting.
type OrgRegion struct {
	OrgID     string    `json:"org_id"`
	Region    string    `json:"region"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DataFlow describes a single control-plane data flow and the region(s) it
// touches.  This is the machine-readable documentation required by COMPLY-01 AC3.
type DataFlow struct {
	// ID is a stable slug, e.g. "bucket-storage".
	ID string `json:"id"`
	// Description is a human-readable summary.
	Description string `json:"description"`
	// RegionBinding indicates how the region is determined:
	//   "org-setting"  — follows the org's chosen residency region
	//   "control-plane" — fixed to the control-plane's own Fly region
	//   "global"        — distributed / no single region
	RegionBinding string `json:"region_binding"`
	// Notes is optional clarifying text.
	Notes string `json:"notes,omitempty"`
}

// ---------------------------------------------------------------------------
// Supported regions
// ---------------------------------------------------------------------------

// SupportedRegions is the canonical list of region slugs accepted by the API.
// Values are S3-compatible region identifiers as used by the managed store (which still
// uses the historical AWS-style codes regardless of the underlying compute
// provider). The list is intentionally small; add entries here + to
// DataFlowMap when a new PoP is provisioned.
var SupportedRegions = []string{
	"af-south-1",     // Africa (Johannesburg) — af-south
	"eu-west-1",      // Europe (Ireland)      — eu-west
	"us-east-1",      // US East (Virginia)    — us-east
	"ap-southeast-1", // Asia Pacific (Singapore)
	"auto",           // automatic placement (default, no residency guarantee)
}

// IsValidRegion reports whether r is in SupportedRegions.
func IsValidRegion(r string) bool {
	for _, s := range SupportedRegions {
		if s == r {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Data-flow map (COMPLY-01 AC3 — public documentation)
// ---------------------------------------------------------------------------

// DataFlowMap returns the authoritative list of control-plane data flows and
// which region each touches.  This is served publicly at
// GET /api/residency/data-flows so customers and procurement teams can verify
// residency guarantees without filing a support ticket.
func DataFlowMap() []DataFlow {
	return []DataFlow{
		{
			ID:            "bucket-storage",
			Description:   "Customer bucket objects (Vulos OS sync, webapp assets)",
			RegionBinding: "org-setting",
			Notes:         "Bucket is provisioned in the org's chosen residency region. BYO-bucket customers control their own region.",
		},
		{
			ID:            "managed-instance",
			Description:   "Fly managed Machine (compute) for hosted Vulos OS",
			RegionBinding: "org-setting",
			Notes:         "Fly volume and Machine are created in the org's chosen residency region.",
		},
		{
			ID:            "control-plane-db",
			Description:   "Control-plane SQLite databases (auth, billing, routing, fleet, …)",
			RegionBinding: "control-plane",
			Notes:         "Stored on the Fly persistent volume attached to the cp service. Region = the cp deployment region (typically closest to operator).",
		},
		{
			ID:            "relay-signaling",
			Description:   "WebRTC/TURN signaling and relay traffic",
			RegionBinding: "global",
			Notes:         "Relay PoPs are distributed; traffic is routed to the nearest PoP. No customer data is persisted on relay nodes.",
		},
		{
			ID:            "dns",
			Description:   "DNS records (Cloudflare-managed zone for *.vulos.org)",
			RegionBinding: "global",
			Notes:         "Cloudflare DNS is globally distributed. Customer custom-domain NS delegation records contain no customer data.",
		},
		{
			ID:            "billing-events",
			Description:   "Billing events and payment processing",
			RegionBinding: "control-plane",
			Notes:         "Your configured payment processor handles payments; transaction records are stored on the control-plane volume.",
		},
		{
			ID:            "audit-log",
			Description:   "OS-level audit log (COMPLY-03, when implemented)",
			RegionBinding: "org-setting",
			Notes:         "Audit logs are shipped to a separate retention bucket in the org's residency region.",
		},
	}
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store persists per-org residency settings.
// MemStore is the behavioural reference; SQLStore must match it.
type Store interface {
	// GetRegion returns the OrgRegion for the given org.
	// Returns ErrUnknownOrg if no row exists.
	GetRegion(ctx context.Context, orgID string) (OrgRegion, error)
	// SetRegion upserts the region for an org.
	// Returns ErrInvalidRegion if region is not in SupportedRegions.
	SetRegion(ctx context.Context, orgID, region string) error
	// AllRegions returns orgID → residency region for every org with a policy
	// (used by the region reconciler, REGION-SSOT-01).
	AllRegions(ctx context.Context) (map[string]string, error)
	// Close closes the underlying store.
	Close() error
}

// ---------------------------------------------------------------------------
// MemStore — in-memory reference (test double)
// ---------------------------------------------------------------------------

// MemStore is the concurrency-safe, in-memory reference Store.
type MemStore struct {
	mu   sync.Mutex
	rows map[string]OrgRegion // orgID → OrgRegion
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{rows: map[string]OrgRegion{}}
}

func (m *MemStore) GetRegion(_ context.Context, orgID string) (OrgRegion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[orgID]
	if !ok {
		return OrgRegion{}, ErrUnknownOrg
	}
	return r, nil
}

func (m *MemStore) SetRegion(_ context.Context, orgID, region string) error {
	if !IsValidRegion(region) {
		return ErrInvalidRegion
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// IMMUTABILITY (item 11): reject a change after first set; same value is a no-op.
	if existing, ok := m.rows[orgID]; ok {
		if existing.Region != region {
			return ErrRegionLocked
		}
		return nil
	}
	m.rows[orgID] = OrgRegion{
		OrgID:     orgID,
		Region:    region,
		UpdatedAt: time.Now().UTC(),
	}
	return nil
}

// AllRegions returns every org's residency region.
func (m *MemStore) AllRegions(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.rows))
	for org, r := range m.rows {
		out[org] = r.Region
	}
	return out, nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion.
var _ Store = (*MemStore)(nil)
