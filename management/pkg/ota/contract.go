// Package ota is the vulos.cloud control-plane OTA (over-the-air update) subsystem.
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every OTA agent builds against these types, this Store interface, the
// reference MemStore (which DEFINES Store semantics), and the reference
// MemSigner (which DEFINES Signer semantics). The SQL Store and real
// KeyProvider implementations must match MemStore/MemSigner behaviourally;
// handlers depend only on the interfaces.
//
// See ../../OTA_API.md and roadmap/OTA.md for the full specification.
package ota

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// Release is an OTA release record as stored in the control plane.
type Release struct {
	ID           int64
	Version      string
	Channel      string // "stable" | "beta" | "pinned"
	ArtifactURL  string
	Sha256       string
	MinFrom      string
	Security     bool
	RolloutPct   int
	SignatureB64 string
	DeferMaxSec  int
	CreatedAt    time.Time
	Halted       bool
}

// DevicePolicy is the per-device OTA configuration set by the device owner or
// an org admin.
type DevicePolicy struct {
	ULID           string
	Channel        string
	PinVersion     string     // "" for none
	DeferUntil     *time.Time // nil for none
	OptOutFeatures bool
	UpdatedAt      time.Time
}

// Manifest is the wire shape returned by GET /api/ota/feed. It contains only
// the fields the client needs to decide whether to apply and to verify
// the artifact.
type Manifest struct {
	Version      string `json:"version"`
	ArtifactURL  string `json:"artifact_url"`
	Sha256       string `json:"sha256"`
	Security     bool   `json:"security"`
	SignatureB64 string `json:"signature"`
	DeferMaxSec  int    `json:"defer_max_seconds,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	ErrInvalidVersion   = errors.New("ota: invalid version")
	ErrInvalidChannel   = errors.New("ota: invalid channel")
	ErrInvalidSignature = errors.New("ota: signature verification failed")
	ErrUnknownULID      = errors.New("ota: unknown ULID")
	ErrNoUpgrade        = errors.New("ota: no applicable upgrade")
	ErrDuplicateRelease = errors.New("ota: release already exists for (version, channel)")
	ErrReleaseNotFound  = errors.New("ota: release not found")
)

// ─────────────────────────────────────────────────────────────────────────────
// Store interface
// ─────────────────────────────────────────────────────────────────────────────

// Adoption holds the adoption-count breakdown for a single OTA release.
// Counts are derived from ota_device_reports for the release's version.
// total_devices and in_cohort are computed from the report log: total_devices
// is the count of distinct ULIDs that have submitted any report for the release
// version, and in_cohort is the subset whose CohortHash < release.RolloutPct.
type Adoption struct {
	TotalDevices int `json:"total_devices"`
	InCohort     int `json:"in_cohort"`
	Applied      int `json:"applied"`
	Failed       int `json:"failed"`
	RolledBack   int `json:"rolled_back"`
}

// Store is the OTA persistence contract. The SQL implementation (SQLStore) and
// MemStore both satisfy it; handlers depend ONLY on this interface.
// MemStore is the behavioural source of truth.
type Store interface {
	// InsertRelease persists a new release. Returns ErrDuplicateRelease if a
	// release with the same (version, channel) already exists.
	InsertRelease(ctx context.Context, r Release) (int64, error)

	// HaltRelease marks a release as halted (disabled for delivery).
	// Returns ErrReleaseNotFound if id is unknown.
	HaltRelease(ctx context.Context, releaseID int64) error

	// GetPolicy returns the device policy for ulid.
	// Returns ErrUnknownULID if the ULID has no policy record.
	GetPolicy(ctx context.Context, ulid string) (DevicePolicy, error)

	// SetPolicy creates or replaces the device policy for p.ULID.
	SetPolicy(ctx context.Context, p DevicePolicy) error

	// FeedFor returns the highest-version non-halted release on channel that:
	//   1. ulid's cohort hash is within the release's rollout_pct,
	//   2. release.min_from <= currentVersion (semver prefix compare),
	//   3. release.version > currentVersion,
	//   4. not suppressed by pin/defer — UNLESS security=true AND
	//      now > policy.defer_until (for defer) or now > pin_set_at+DeferMaxSec
	//      (approximated: security releases always override pin/defer).
	//
	// Returns ErrNoUpgrade when no applicable release exists.
	// Does NOT return ErrUnknownULID — a ULID with no policy uses the default
	// policy (channel=stable, no pin, no defer, opt_out_features=false).
	FeedFor(ctx context.Context, ulid, channel, currentVersion string) (Release, error)

	// InsertReport persists a device update-status report (logs only in v1).
	InsertReport(ctx context.Context, ulid, version, result string) error

	// ListReleases returns all releases ordered by id descending, with
	// pagination via limit/offset. limit <= 0 defaults to 50; max 200.
	ListReleases(ctx context.Context, limit, offset int) ([]Release, error)

	// AdoptionCounts returns the adoption-count breakdown for the release
	// identified by releaseID. Returns ErrReleaseNotFound if unknown.
	// Counts are derived from ota_device_reports for the release version.
	AdoptionCounts(ctx context.Context, releaseID int64) (Adoption, error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// Signer interface + KeyProvider
// ─────────────────────────────────────────────────────────────────────────────

// Signer signs and verifies OTA manifest JSON. The signing key is never
// exposed outside the KeyProvider; handlers call Sign/Verify only.
type Signer interface {
	// Sign signs manifestJSON and returns the base64-encoded ed25519 signature.
	Sign(manifestJSON []byte) (signatureB64 string, err error)
	// Verify returns ErrInvalidSignature if the signature does not verify.
	Verify(manifestJSON []byte, sigB64 string) error
}

// KeyProvider is the signing-key custody abstraction. Implementations:
//   - EnvKeyProvider   — loads key from OTA_SIGNING_KEY_BASE64 / OTA_VERIFY_KEY_BASE64
//   - FileKeyProvider  — loads key from a path (0o600 required)
//   - (future) HSMKeyProvider — delegates to an HSM; satisfies the same interface
//
// The KeyProvider is used only by NewSigner; handlers never call it directly.
type KeyProvider interface {
	// PrivateKey returns the ed25519 private key (64 bytes) for signing.
	// Returns an error if the key is unavailable or malformed.
	PrivateKey() (ed25519.PrivateKey, error)

	// PublicKey returns the ed25519 public key (32 bytes) for verification.
	// Returns an error if the key is unavailable or malformed.
	PublicKey() (ed25519.PublicKey, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// CohortHash — deterministic, exported so tests + clients agree
// ─────────────────────────────────────────────────────────────────────────────

// CohortHash returns a deterministic value in [0, 99] for (ulid, version).
// The cohort selection rule is: include in rollout if CohortHash(ulid, version)
// < rollout_pct. This is exported and deterministic so the OSS client and the
// server agree without coordination.
//
// Implementation: FNV-1a 64-bit hash of "ulid:version", result mod 100.
// FNV-1a is dependency-free, fast, and has good distribution for short strings.
func CohortHash(ulid, version string) uint8 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(ulid))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(version))
	return uint8(h.Sum64() % 100)
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidChannel
// ─────────────────────────────────────────────────────────────────────────────

// ValidChannel reports whether channel is a known OTA channel identifier.
func ValidChannel(ch string) bool {
	return ch == "stable" || ch == "beta" || ch == "pinned"
}

// ─────────────────────────────────────────────────────────────────────────────
// semverGTE / semverGT — simple semver prefix comparison (no pre-release)
// ─────────────────────────────────────────────────────────────────────────────

// semverComponents splits a "vX.Y.Z" or "X.Y.Z" string into [X, Y, Z] ints.
// Returns [0,0,0] on parse failure (permissive; callers validate upstream).
func semverComponents(v string) [3]int {
	if len(v) > 0 && v[0] == 'v' {
		v = v[1:]
	}
	var major, minor, patch int
	n := parseThreeParts(v, &major, &minor, &patch)
	if n < 1 {
		return [3]int{}
	}
	return [3]int{major, minor, patch}
}

// parseThreeParts fills the three int pointers from "X.Y.Z"; returns how many
// parts were parsed successfully.
func parseThreeParts(s string, a, b, c *int) (n int) {
	for i, part := range splitDot(s, 3) {
		val := 0
		ok := true
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				ok = false
				break
			}
			val = val*10 + int(ch-'0')
		}
		if !ok {
			return i
		}
		switch i {
		case 0:
			*a = val
		case 1:
			*b = val
		case 2:
			*c = val
		}
		n = i + 1
	}
	return n
}

func splitDot(s string, max int) []string {
	var out []string
	for i := 0; i < max; i++ {
		idx := -1
		for j := 0; j < len(s); j++ {
			if s[j] == '.' {
				idx = j
				break
			}
		}
		if idx < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:idx])
		s = s[idx+1:]
	}
	return out
}

// semverGT reports whether a > b (simple X.Y.Z comparison, no pre-release).
func semverGT(a, b string) bool {
	ca, cb := semverComponents(a), semverComponents(b)
	for i := 0; i < 3; i++ {
		if ca[i] > cb[i] {
			return true
		}
		if ca[i] < cb[i] {
			return false
		}
	}
	return false
}

// semverGTE reports whether a >= b.
func semverGTE(a, b string) bool {
	return a == b || semverGT(a, b)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES the
// semantics every other implementation must match.
type MemStore struct {
	mu       sync.Mutex
	releases []Release
	nextID   int64
	policies map[string]DevicePolicy // ulid -> policy
	reports  []struct {
		ulid, version, result string
		receivedAt            time.Time
	}
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{
		nextID:   1,
		policies: map[string]DevicePolicy{},
	}
}

func (m *MemStore) InsertRelease(_ context.Context, r Release) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.releases {
		if existing.Version == r.Version && existing.Channel == r.Channel {
			return 0, ErrDuplicateRelease
		}
	}
	r.ID = m.nextID
	m.nextID++
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	m.releases = append(m.releases, r)
	return r.ID, nil
}

func (m *MemStore) HaltRelease(_ context.Context, releaseID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.releases {
		if r.ID == releaseID {
			m.releases[i].Halted = true
			return nil
		}
	}
	return ErrReleaseNotFound
}

func (m *MemStore) GetPolicy(_ context.Context, ulid string) (DevicePolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.policies[ulid]
	if !ok {
		return DevicePolicy{}, ErrUnknownULID
	}
	return p, nil
}

func (m *MemStore) SetPolicy(_ context.Context, p DevicePolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.UpdatedAt = time.Now().UTC()
	m.policies[p.ULID] = p
	return nil
}

// FeedFor implements the full upgrade eligibility logic per the contract doc.
func (m *MemStore) FeedFor(_ context.Context, ulid, channel, currentVersion string) (Release, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Resolve device policy (missing ULID → default policy).
	policy, hasPol := m.policies[ulid]
	if !hasPol {
		policy = DevicePolicy{
			ULID:    ulid,
			Channel: "stable",
		}
	}

	// Effective channel: caller-supplied wins; fall back to policy channel.
	effectiveChannel := channel
	if effectiveChannel == "" {
		effectiveChannel = policy.Channel
	}

	now := time.Now().UTC()

	// Gather candidate releases sorted descending by semver (highest first).
	type candidate struct {
		r Release
	}
	var candidates []candidate
	for _, r := range m.releases {
		if r.Halted {
			continue
		}
		if r.Channel != effectiveChannel {
			continue
		}
		// Must be a genuine upgrade.
		if !semverGT(r.Version, currentVersion) {
			continue
		}
		// min_from constraint: currentVersion must be >= min_from.
		if !semverGTE(currentVersion, r.MinFrom) {
			continue
		}
		// Cohort check.
		if int(CohortHash(ulid, r.Version)) >= r.RolloutPct {
			continue
		}
		// Pin/defer suppression — security releases override everything.
		if !r.Security {
			// Pin: device is pinned to a specific version.
			if policy.PinVersion != "" && policy.PinVersion != r.Version {
				continue
			}
			// Defer: device is deferring updates until a date.
			if policy.DeferUntil != nil && now.Before(*policy.DeferUntil) {
				continue
			}
			// opt_out_features suppresses non-security updates.
			if policy.OptOutFeatures {
				continue
			}
		}
		candidates = append(candidates, candidate{r})
	}

	if len(candidates) == 0 {
		return Release{}, ErrNoUpgrade
	}

	// Sort descending by version — pick highest applicable.
	sort.Slice(candidates, func(i, j int) bool {
		return semverGT(candidates[i].r.Version, candidates[j].r.Version)
	})

	return candidates[0].r, nil
}

func (m *MemStore) InsertReport(_ context.Context, ulid, version, result string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports = append(m.reports, struct {
		ulid, version, result string
		receivedAt            time.Time
	}{ulid, version, result, time.Now().UTC()})
	return nil
}

// ListReleases returns all releases ordered by id descending with pagination.
func (m *MemStore) ListReleases(_ context.Context, limit, offset int) ([]Release, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	limit = clampLimit(limit)

	// Copy and sort descending by ID.
	all := make([]Release, len(m.releases))
	copy(all, m.releases)
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })

	if offset >= len(all) {
		return []Release{}, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

// AdoptionCounts returns the adoption breakdown for the given release.
// total_devices and in_cohort are computed from ota_device_reports.
func (m *MemStore) AdoptionCounts(_ context.Context, releaseID int64) (Adoption, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find the release.
	var rel Release
	found := false
	for _, r := range m.releases {
		if r.ID == releaseID {
			rel = r
			found = true
			break
		}
	}
	if !found {
		return Adoption{}, ErrReleaseNotFound
	}

	// Gather distinct ULIDs that reported for this release version and their
	// latest result.
	type entry struct {
		result     string
		receivedAt time.Time
	}
	latest := map[string]entry{} // ulid -> latest report entry
	for _, rpt := range m.reports {
		if rpt.version != rel.Version {
			continue
		}
		if e, ok := latest[rpt.ulid]; !ok || rpt.receivedAt.After(e.receivedAt) {
			latest[rpt.ulid] = entry{result: rpt.result, receivedAt: rpt.receivedAt}
		}
	}

	var a Adoption
	a.TotalDevices = len(latest)
	for ulid, e := range latest {
		if int(CohortHash(ulid, rel.Version)) < rel.RolloutPct {
			a.InCohort++
		}
		switch e.result {
		case "applied":
			a.Applied++
		case "failed":
			a.Failed++
		case "rolled-back":
			a.RolledBack++
		}
	}
	return a, nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// clampLimit normalises a pagination limit: 0 or negative → 50; >200 → 200.
func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

// ─────────────────────────────────────────────────────────────────────────────
// MemSigner — reference Signer with a generated test key
// ─────────────────────────────────────────────────────────────────────────────

// MemSigner implements Signer with an ephemeral ed25519 key pair. Intended for
// tests. The key is generated once at construction; never persisted.
type MemSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewMemSigner returns a MemSigner with a freshly generated ed25519 key pair.
func NewMemSigner() *MemSigner {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("ota: MemSigner key gen: " + err.Error())
	}
	return &MemSigner{priv: priv, pub: pub}
}

func (s *MemSigner) Sign(manifestJSON []byte) (string, error) {
	sig := ed25519.Sign(s.priv, manifestJSON)
	return base64.StdEncoding.EncodeToString(sig), nil
}

func (s *MemSigner) Verify(manifestJSON []byte, sigB64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return ErrInvalidSignature
	}
	if !ed25519.Verify(s.pub, manifestJSON, sig) {
		return ErrInvalidSignature
	}
	return nil
}

// PublicKeyBytes returns the raw 32-byte public key (for documentation/export).
func (s *MemSigner) PublicKeyBytes() []byte {
	b := make([]byte, len(s.pub))
	copy(b, s.pub)
	return b
}

// compile-time assertion: MemSigner satisfies Signer.
var _ Signer = (*MemSigner)(nil)
