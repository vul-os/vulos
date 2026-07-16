package georoute

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RegionConfig describes one region in the cloud topology. Endpoint is the
// public base URL the geo-redirect points clients at (e.g.
// "https://eu.cp.vulos.org"). Regions are configured at startup — the ring is
// frozen at NewService time (call NewService again to reconfigure).
type RegionConfig struct {
	// Name is the region ID echoed in the tenants_regions table (e.g. "eu",
	// "us", "af").
	Name string
	// Endpoint is the public base URL of the region's CP / bundle-node pool.
	// Format: scheme://host[:port], no trailing slash.
	Endpoint string
}

// Config tunes Service behaviour. All fields are optional except Regions.
type Config struct {
	// Regions is the ordered list of configured regions. Order defines the
	// alternate-region ring: AlternateRegion walks this list deterministically
	// starting at the slot AFTER the home region.
	Regions []RegionConfig
	// DefaultRegion is assigned to tenants when no hint / geo-IP match.
	// If empty, defaults to Regions[0].Name.
	DefaultRegion string
	// LocalRegion is the region THIS CP instance is serving. The geo-route
	// middleware pass-through path checks (homeRegion == LocalRegion).
	LocalRegion string
}

// Service is the georoute orchestrator. It composes a Store with a region
// topology (the ring), a HealthProbe, and a SyncTrigger to drive failover +
// cross-region convergence.
//
// IMPORTANT: Service.HomeRegion / Service.Assign are thin wrappers over the
// Store; the value the Service adds is the RING (alternate-region selection)
// + the FAILOVER glue (AlternateRegion → SyncTrigger.Trigger).
type Service struct {
	Store         Store
	Probe         HealthProbe
	Trigger       SyncTrigger
	regions       []RegionConfig
	regionsByName map[string]RegionConfig
	defaultRegion string
	localRegion   string
}

// NewService builds a Service. Returns ErrNoRegions when cfg.Regions is
// empty.
func NewService(st Store, probe HealthProbe, trig SyncTrigger, cfg Config) (*Service, error) {
	if len(cfg.Regions) == 0 {
		return nil, ErrNoRegions
	}
	if probe == nil {
		probe = StaticHealth{}
	}
	if trig == nil {
		trig = NoopSyncTrigger{}
	}
	// Copy + freeze the region list to lock in the ring order.
	regs := make([]RegionConfig, len(cfg.Regions))
	copy(regs, cfg.Regions)
	byName := make(map[string]RegionConfig, len(regs))
	for _, r := range regs {
		byName[r.Name] = r
	}
	defReg := cfg.DefaultRegion
	if defReg == "" {
		defReg = regs[0].Name
	}
	return &Service{
		Store:         st,
		Probe:         probe,
		Trigger:       trig,
		regions:       regs,
		regionsByName: byName,
		defaultRegion: defReg,
		localRegion:   cfg.LocalRegion,
	}, nil
}

// Regions returns a defensive copy of the configured region list.
func (s *Service) Regions() []RegionConfig {
	out := make([]RegionConfig, len(s.regions))
	copy(out, s.regions)
	return out
}

// LocalRegion returns the region this CP instance is configured to serve.
func (s *Service) LocalRegion() string { return s.localRegion }

// DefaultRegion returns the fallback region assigned when no hint matches.
func (s *Service) DefaultRegion() string { return s.defaultRegion }

// Assign assigns a tenant to a region. The region MUST be one of the
// configured regions; an unknown region is rejected with ErrInvalidInput so
// we never persist a region that has no endpoint.
func (s *Service) Assign(ctx context.Context, tenantID, region string) (Assignment, error) {
	if _, ok := s.regionsByName[region]; !ok {
		return Assignment{}, fmt.Errorf("%w: unknown region %q", ErrInvalidInput, region)
	}
	return s.Store.Assign(ctx, tenantID, region)
}

// ReassignForFailover overwrites a tenant's home region — the EXPLICIT, gated
// failover/migration path that the immutable Assign intentionally forbids
// (REGION-SSOT-01). The region must be one of the configured regions. This is the
// ONLY sanctioned way to mutate an already-set home region; ordinary callers must
// use Assign (which is immutable after first set).
func (s *Service) ReassignForFailover(ctx context.Context, tenantID, region string) (Assignment, error) {
	if _, ok := s.regionsByName[region]; !ok {
		return Assignment{}, fmt.Errorf("%w: unknown region %q", ErrInvalidInput, region)
	}
	return s.Store.ForceAssign(ctx, tenantID, region)
}

// AssignFromHint picks a home region from the hints in priority order:
//
//  1. explicit hint string (e.g. signup form value),
//  2. region resolved from clientIP via the supplied resolver,
//  3. s.DefaultRegion.
//
// Pass an empty hint / IP to skip a step. Returns the persisted assignment.
//
// resolver may be nil — in which case only the hint + default are considered.
func (s *Service) AssignFromHint(ctx context.Context, tenantID, hint, clientIP string, resolver IPRegionResolver) (Assignment, error) {
	if tenantID == "" {
		return Assignment{}, ErrInvalidInput
	}
	region := s.pickRegionFromHints(hint, clientIP, resolver)
	return s.Store.Assign(ctx, tenantID, region)
}

// pickRegionFromHints applies the priority order without touching the store.
// Exposed via AssignFromHint; isolated for unit testing.
func (s *Service) pickRegionFromHints(hint, clientIP string, resolver IPRegionResolver) string {
	if hint != "" {
		if _, ok := s.regionsByName[hint]; ok {
			return hint
		}
	}
	if clientIP != "" && resolver != nil {
		if r := resolver.RegionForIP(clientIP); r != "" {
			if _, ok := s.regionsByName[r]; ok {
				return r
			}
		}
	}
	return s.defaultRegion
}

// HomeRegion returns the home region for tenantID — thin Store wrapper.
func (s *Service) HomeRegion(ctx context.Context, tenantID string) (string, error) {
	return s.Store.HomeRegion(ctx, tenantID)
}

// EndpointForRegion returns the base URL for region, or empty when region is
// unknown.
func (s *Service) EndpointForRegion(region string) string {
	if r, ok := s.regionsByName[region]; ok {
		return r.Endpoint
	}
	return ""
}

// AlternateRegion returns the next healthy region in the ring after home.
// Deterministic: walks s.regions starting at index(home)+1, wrapping.
// Skips the home region itself + any region the probe reports unhealthy.
// Returns "" only when NO alternate is healthy.
//
// Determinism is the property the failover test asserts: for a given (home,
// healthy-set), AlternateRegion(home) always returns the SAME region.
func (s *Service) AlternateRegion(ctx context.Context, home string) string {
	if len(s.regions) == 0 {
		return ""
	}
	startIdx := -1
	for i, r := range s.regions {
		if r.Name == home {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		// Unknown home — walk from 0.
		startIdx = -1
	}
	n := len(s.regions)
	for off := 1; off <= n; off++ {
		idx := (startIdx + off) % n
		if idx < 0 {
			idx += n
		}
		cand := s.regions[idx].Name
		if cand == home {
			continue
		}
		if s.Probe.IsHealthy(ctx, cand) {
			return cand
		}
	}
	return ""
}

// EffectiveRegion returns (region, isFailover) for tenantID: the home region
// if healthy, else the deterministic alternate. When isFailover is true and
// the SyncTrigger is non-nil, the caller MAY call s.Trigger.Trigger to
// converge cross-region — Failover does this for them. EffectiveRegion is a
// READ-only helper that does NOT fire the trigger.
func (s *Service) EffectiveRegion(ctx context.Context, tenantID string) (region string, isFailover bool, err error) {
	home, err := s.Store.HomeRegion(ctx, tenantID)
	if err != nil {
		return "", false, err
	}
	if s.Probe.IsHealthy(ctx, home) {
		return home, false, nil
	}
	alt := s.AlternateRegion(ctx, home)
	if alt == "" {
		return home, false, nil // no healthy alt — bounce caller to home anyway
	}
	return alt, true, nil
}

// Failover resolves the effective region for tenantID + fires the cross-
// region sync trigger when the resolved region is NOT the home region.
// Returns the effective region. The sync trigger error is logged via the
// caller; we still return the routed region so the redirect can proceed.
//
// syncKey is the per-(tenant, store-kind) key that maps to the rendezvous
// row. Callers pass the syncKey appropriate to the call site (e.g. "default"
// for opaque-store accounts, or a per-app key).
func (s *Service) Failover(ctx context.Context, tenantID, syncKey string) (region string, triggered bool, err error) {
	region, isFailover, err := s.EffectiveRegion(ctx, tenantID)
	if err != nil {
		return "", false, err
	}
	if !isFailover {
		return region, false, nil
	}
	reason := "georoute: failover " + tenantID + " → " + region
	if trigErr := s.Trigger.Trigger(ctx, tenantID, syncKey, reason); trigErr != nil {
		// Convergence trigger is best-effort: the rendezvous already does
		// pull-on-cycle, so failing to push a hint is not a route-blocking
		// error.
		return region, false, fmt.Errorf("georoute: trigger sync: %w", trigErr)
	}
	return region, true, nil
}

// IPRegionResolver maps a client IP to a region name. Implementations may
// wrap MaxMind, a static prefix table (see internal/routing/geoip.go), or a
// CDN-injected header. The georoute package itself only depends on this
// interface — geo-IP details live elsewhere.
type IPRegionResolver interface {
	RegionForIP(ip string) string
}

// StaticIPResolver is a deterministic IPRegionResolver for tests + static
// configs. Keys are IP-prefix strings (e.g. "10.", "196.") that are matched
// with strings.HasPrefix; first match wins. The empty-string key is the
// catch-all when no prefix matches.
type StaticIPResolver map[string]string

// RegionForIP returns the region for the longest matching prefix, or the
// "" key if present, else "".
func (s StaticIPResolver) RegionForIP(ip string) string {
	// Walk keys longest-first for deterministic longest-prefix wins.
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if k == "" {
			continue
		}
		if strings.HasPrefix(ip, k) {
			return s[k]
		}
	}
	if fallback, ok := s[""]; ok {
		return fallback
	}
	return ""
}
