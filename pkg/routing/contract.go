// Package routing is the vulos.cloud control-plane routing/identity subsystem
// (see ../../ROUTING_API.md and roadmap/ROUTING.md).
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every routing agent builds against these types, this Store interface, the
// reference MemStore (which DEFINES Store semantics), and the security-
// critical ParseHost/CanonULID parity functions. The SQL Store implementation
// must match MemStore behaviourally; handlers depend only on the interface.
package routing

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/idents"
)

// ConnMode mirrors the OSS NET-* connection-mode spec. Mode D (local-only)
// never contacts the cloud by design and is intentionally absent here.
type ConnMode string

const (
	ModeFabric    ConnMode = "fabric"     // A — relay/PoP fabric
	ModeDirect    ConnMode = "direct"     // B — instance's own public IP
	ModeOwnDomain ConnMode = "own-domain" // C — customer-provided domain
)

// ValidConnMode reports whether m is a cloud-relevant mode (A/B/C).
func ValidConnMode(m ConnMode) bool {
	return m == ModeFabric || m == ModeDirect || m == ModeOwnDomain
}

// Account owns one or more ULIDs. Tier feeds BILLING and quota.
type Account struct {
	ID        string
	Tier      string
	CreatedAt time.Time
}

// Binding is the ULID ↔ account binding plus its routing state. ULID is stored
// in canonical 26-char uppercase Crockford form (see CanonULID).
type Binding struct {
	ULID      string
	AccountID string
	Mode      ConnMode
	DirectIP  string // only meaningful for ModeDirect
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Device is a single enrolled device under a ULID (feeds FLEET).
type Device struct {
	ID        string
	ULID      string
	AccountID string
	Mode      ConnMode
	LastSeen  time.Time
}

// PoP is a relay point-of-presence in the geo-DNS pool.
type PoP struct {
	ID         string
	Region     string
	IP         string
	Healthy    bool
	Lat, Lon   float64
	LastHealth time.Time
}

// Vanity is a paid-tier vanity name → ULID redirect record.
type Vanity struct {
	Name      string
	ULID      string
	AccountID string
}

// Sentinel errors. Handlers map these to HTTP status codes (see ROUTING_API.md).
var (
	ErrInvalidULID             = errors.New("routing: invalid ULID")
	ErrInvalidName             = errors.New("routing: invalid name")
	ErrInvalidMode             = errors.New("routing: invalid connection mode")
	ErrULIDBoundToOtherAccount = errors.New("routing: ULID already bound to a different account")
	ErrNotEnrolled             = errors.New("routing: ULID not enrolled")
	ErrNotOwner                = errors.New("routing: account does not own this ULID")
	ErrVanityTaken             = errors.New("routing: vanity name already taken")
	ErrNoHealthyPoP            = errors.New("routing: no healthy PoP available")
	ErrNotFound                = errors.New("routing: not found")
)

// CanonULID validates s with the shared idents parser (rejects wrong length,
// Crockford-excluded I/L/O/U, 128-bit overflow) and returns the canonical
// 26-char UPPERCASE form. This is the ONLY accepted ULID spelling downstream.
func CanonULID(s string) (string, error) {
	if _, err := idents.ParseULID(s); err != nil {
		return "", ErrInvalidULID
	}
	return strings.ToUpper(s), nil
}

// ValidDirectIP reports whether s is a usable direct-mode address. Direct mode
// (B) is the instance's own reachable IP, so plainly non-routable targets are
// rejected: unspecified (0.0.0.0 / ::), loopback, link-local, and multicast.
// Private / CGNAT ranges are permitted (legitimate in some topologies). This
// closes the security-suite finding that net.ParseIP alone accepts 0.0.0.0/::.
func ValidDirectIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	return true
}

// ParseHost parses an OSS-parity fabric host into its components.
//
// Scheme (must match OSS NETWORK.md exactly so cloud never contradicts the
// instance):  {app}--{profile}.{ulid}.{base}
//   - separator between app and profile is "--"
//   - a missing app/profile label defaults to "default"
//   - bare {ulid}.{base} → app=default, profile=default
//   - app and profile each validated by idents.ValidName (same regex as OSS)
//   - ulid validated/canonicalised by CanonULID
//   - exactly zero or one sub-label before the ULID label (no deeper nesting)
//   - an optional :port is stripped; matching is case-insensitive
//
// Returned ulid is canonical (uppercase). Any deviation → error (fail closed).
func ParseHost(host, base string) (app, profile, ulid string, err error) {
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	base = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(base), "."))
	if host == "" || base == "" {
		return "", "", "", ErrNotFound
	}
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return "", "", "", ErrNotFound
	}
	rest := strings.TrimSuffix(host, suffix)
	if rest == "" {
		return "", "", "", ErrNotFound
	}
	parts := strings.Split(rest, ".")
	if len(parts) == 0 || len(parts) > 2 {
		return "", "", "", ErrNotFound // no deeper nesting than {sub}.{ulid}
	}
	rawULID := parts[len(parts)-1]
	cu, cErr := CanonULID(rawULID)
	if cErr != nil {
		return "", "", "", ErrInvalidULID
	}
	app, profile = "default", "default"
	if len(parts) == 2 {
		sub := parts[0]
		if sub == "" {
			return "", "", "", ErrInvalidName
		}
		if i := strings.Index(sub, "--"); i >= 0 {
			app = sub[:i]
			profile = sub[i+2:]
		} else {
			app = sub
		}
		if !idents.ValidName(app) || !idents.ValidName(profile) {
			return "", "", "", ErrInvalidName
		}
	}
	return app, profile, cu, nil
}

// Store is the routing persistence contract. The SQL implementation
// (routing-store) and MemStore both satisfy it; handlers depend ONLY on this
// interface. MemStore is the behavioural source of truth.
type Store interface {
	// Enroll is idempotent. Binding an already-bound ULID to the SAME account
	// returns the existing binding (ULID preserved, Mode refreshed). Binding to
	// a DIFFERENT account returns ErrULIDBoundToOtherAccount. ulid must be
	// canonical; mode must be valid.
	Enroll(ctx context.Context, ulid, accountID string, mode ConnMode) (Binding, error)
	GetBinding(ctx context.Context, ulid string) (Binding, error)
	SetConnMode(ctx context.Context, ulid string, mode ConnMode) error
	// SetDirectIP requires accountID to own ulid (else ErrNotOwner).
	SetDirectIP(ctx context.Context, ulid, accountID, ip string) error

	UpsertDevice(ctx context.Context, d Device) error
	ListDevices(ctx context.Context, ulid string) ([]Device, error)

	UpsertPoP(ctx context.Context, p PoP) error
	SetPoPHealth(ctx context.Context, popID string, healthy bool) error
	HealthyPoPs(ctx context.Context) ([]PoP, error)

	// ClaimVanity is idempotent for the same (name,ulid,account); a different
	// owner/target for an existing name returns ErrVanityTaken.
	ClaimVanity(ctx context.Context, name, ulid, accountID string) error
	ResolveVanity(ctx context.Context, name string) (Vanity, error)

	Close() error
}

// NearestPoP returns the healthy PoP closest to (lat,lon) by great-circle
// distance. Unhealthy PoPs are excluded. ErrNoHealthyPoP if none. This is the
// frozen selection semantics the DNS/route resolver must use.
func NearestPoP(pops []PoP, lat, lon float64) (PoP, error) {
	best := PoP{}
	bestD := -1.0
	for _, p := range pops {
		if !p.Healthy {
			continue
		}
		d := haversine(lat, lon, p.Lat, p.Lon)
		if bestD < 0 || d < bestD {
			bestD, best = d, p
		}
	}
	if bestD < 0 {
		return PoP{}, ErrNoHealthyPoP
	}
	return best, nil
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	// Cheap squared-ish metric is enough for nearest selection; keep it
	// dependency-free and monotonic in true distance for small fleets.
	dLat := lat1 - lat2
	dLon := lon1 - lon2
	return dLat*dLat + dLon*dLon
}

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES the
// semantics every other implementation must match.
type MemStore struct {
	mu       sync.Mutex
	bindings map[string]Binding // ulid -> binding
	devices  map[string]Device  // device id -> device
	pops     map[string]PoP     // pop id -> pop
	vanity   map[string]Vanity  // name -> vanity
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{
		bindings: map[string]Binding{},
		devices:  map[string]Device{},
		pops:     map[string]PoP{},
		vanity:   map[string]Vanity{},
	}
}

func (m *MemStore) Enroll(_ context.Context, ulid, accountID string, mode ConnMode) (Binding, error) {
	if ulid == "" || accountID == "" {
		return Binding{}, ErrInvalidULID
	}
	if !ValidConnMode(mode) {
		return Binding{}, ErrInvalidMode
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.bindings[ulid]; ok {
		if b.AccountID != accountID {
			return Binding{}, ErrULIDBoundToOtherAccount
		}
		b.Mode = mode
		b.UpdatedAt = time.Now().UTC()
		m.bindings[ulid] = b
		return b, nil
	}
	now := time.Now().UTC()
	b := Binding{ULID: ulid, AccountID: accountID, Mode: mode, CreatedAt: now, UpdatedAt: now}
	m.bindings[ulid] = b
	return b, nil
}

func (m *MemStore) GetBinding(_ context.Context, ulid string) (Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[ulid]
	if !ok {
		return Binding{}, ErrNotEnrolled
	}
	return b, nil
}

func (m *MemStore) SetConnMode(_ context.Context, ulid string, mode ConnMode) error {
	if !ValidConnMode(mode) {
		return ErrInvalidMode
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[ulid]
	if !ok {
		return ErrNotEnrolled
	}
	b.Mode = mode
	b.UpdatedAt = time.Now().UTC()
	m.bindings[ulid] = b
	return nil
}

func (m *MemStore) SetDirectIP(_ context.Context, ulid, accountID, ip string) error {
	if !ValidDirectIP(ip) {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[ulid]
	if !ok {
		return ErrNotEnrolled
	}
	if b.AccountID != accountID {
		return ErrNotOwner
	}
	b.DirectIP = ip
	b.UpdatedAt = time.Now().UTC()
	m.bindings[ulid] = b
	return nil
}

func (m *MemStore) UpsertDevice(_ context.Context, d Device) error {
	if d.ID == "" || d.ULID == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[d.ID] = d
	return nil
}

func (m *MemStore) ListDevices(_ context.Context, ulid string) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Device
	for _, d := range m.devices {
		if d.ULID == ulid {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemStore) UpsertPoP(_ context.Context, p PoP) error {
	if p.ID == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pops[p.ID] = p
	return nil
}

func (m *MemStore) SetPoPHealth(_ context.Context, popID string, healthy bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pops[popID]
	if !ok {
		return ErrNotFound
	}
	p.Healthy = healthy
	p.LastHealth = time.Now().UTC()
	m.pops[popID] = p
	return nil
}

func (m *MemStore) HealthyPoPs(_ context.Context) ([]PoP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PoP
	for _, p := range m.pops {
		if p.Healthy {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemStore) ClaimVanity(_ context.Context, name, ulid, accountID string) error {
	if !idents.ValidName(name) {
		return ErrInvalidName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.vanity[name]; ok {
		if v.ULID != ulid || v.AccountID != accountID {
			return ErrVanityTaken
		}
		return nil // idempotent re-claim by same owner/target
	}
	m.vanity[name] = Vanity{Name: name, ULID: ulid, AccountID: accountID}
	return nil
}

func (m *MemStore) ResolveVanity(_ context.Context, name string) (Vanity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vanity[name]
	if !ok {
		return Vanity{}, ErrNotFound
	}
	return v, nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)
