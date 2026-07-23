package cloudstatus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

/* ─── fakes ──────────────────────────────────────────────────────────────── */

type fakeDB struct {
	err   error
	calls int
}

func (f *fakeDB) Ping(context.Context) error { f.calls++; return f.err }

type fakePoPs struct {
	pops  []PoP
	err   error
	panic bool
}

func (f fakePoPs) ListPoPs(context.Context) ([]PoP, error) {
	if f.panic {
		panic("boom")
	}
	return f.pops, f.err
}

type fakeDevices struct {
	byAccount map[string][]Device
	err       error
	panic     bool
}

func (f fakeDevices) Devices(_ context.Context, accountID string) ([]Device, error) {
	if f.panic {
		panic("boom")
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.byAccount[accountID], nil
}

type fakeRelay struct {
	usage RelayUsage
	err   error
}

func (f fakeRelay) Usage(context.Context, string) (RelayUsage, error) { return f.usage, f.err }

func okProbe(context.Context, string) bool   { return true }
func failProbe(context.Context, string) bool { return false }

func t0() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) }

/* ─── public snapshot ────────────────────────────────────────────────────── */

func TestPublicSnapshot_AllHealthy(t *testing.T) {
	agg := New(Config{
		DB: &fakeDB{},
		PoPs: fakePoPs{pops: []PoP{
			{ID: "pop-eu-fra-01", Region: "eu-central", Healthy: true},
			{ID: "pop-eu-ams-01", Region: "europe", Healthy: true},
			{ID: "pop-jhb-01", Region: "africa-south", Healthy: true},
		}},
		Probe:        okProbe,
		ProvisionURL: "https://prov.example.test/healthz",
		Now:          t0,
	})
	snap := agg.PublicSnapshot(context.Background())
	if snap.Overall != StatusOperational {
		t.Fatalf("overall = %q, want operational; comps=%+v", snap.Overall, snap.Components)
	}
	byID := map[string]Component{}
	for _, c := range snap.Components {
		byID[c.ID] = c
	}
	for _, id := range []string{"control_plane", "relay_eu", "relay_jhb", "provisioning"} {
		c, ok := byID[id]
		if !ok {
			t.Fatalf("component %q missing", id)
		}
		if c.Status != StatusOperational {
			t.Errorf("component %q = %q, want operational (detail %q)", id, c.Status, c.Detail)
		}
	}
}

// A probe that always fails + an unreachable DB must degrade the affected
// components to down — never panic, never leak the underlying error.
func TestPublicSnapshot_FailSafeOnProbeError(t *testing.T) {
	agg := New(Config{
		DB:    &fakeDB{err: errors.New("dial tcp: connection refused to 10.0.0.5:5432")},
		PoPs:  fakePoPs{pops: []PoP{{ID: "p", Region: "eu", Healthy: true}}},
		Probe: failProbe,
		Now:   t0,
	})
	snap := agg.PublicSnapshot(context.Background())
	byID := map[string]Component{}
	for _, c := range snap.Components {
		byID[c.ID] = c
	}
	if byID["control_plane"].Status != StatusDown {
		t.Errorf("control_plane = %q, want down", byID["control_plane"].Status)
	}
	if byID["provisioning"].Status != StatusDown {
		t.Errorf("provisioning = %q, want down", byID["provisioning"].Status)
	}
	if snap.Overall != StatusDown {
		t.Errorf("overall = %q, want down", snap.Overall)
	}
	// The raw error (host:port) must never surface in any detail.
	for _, c := range snap.Components {
		if strings.Contains(c.Detail, "10.0.0.5") || strings.Contains(c.Detail, "5432") || strings.Contains(c.Detail, "connection refused") {
			t.Errorf("component %q leaked error text into detail: %q", c.ID, c.Detail)
		}
	}
}

// The public snapshot must never carry infra addresses, URLs, or any field
// beyond the fixed contract.
func TestPublicSnapshot_NoLeak(t *testing.T) {
	agg := New(Config{
		DB:    &fakeDB{},
		PoPs:  fakePoPs{pops: []PoP{{ID: "pop-eu", Region: "eu", Healthy: true}}},
		Probe: okProbe,
		Now:   t0,
	})
	snap := agg.PublicSnapshot(context.Background())
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{"://", "8443", "internal.example.test", "healthz"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("public snapshot leaked %q: %s", forbidden, body)
		}
	}
	// Pin the field set — a future addition that leaks internals fails loudly.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode top: %v", err)
	}
	allowedTop := map[string]bool{"generated_at": true, "overall": true, "components": true}
	for k := range top {
		if !allowedTop[k] {
			t.Errorf("unexpected top-level field %q", k)
		}
	}
	var comps []map[string]json.RawMessage
	_ = json.Unmarshal(top["components"], &comps)
	allowedComp := map[string]bool{"id": true, "name": true, "status": true, "detail": true}
	for _, c := range comps {
		for k := range c {
			if !allowedComp[k] {
				t.Errorf("unexpected per-component field %q", k)
			}
		}
	}
}

func TestPublicSnapshot_UnknownWhenUnconfigured(t *testing.T) {
	agg := New(Config{Now: t0}) // no DB, no PoPs, no meet URL
	snap := agg.PublicSnapshot(context.Background())
	for _, c := range snap.Components {
		if c.Status != StatusUnknown {
			t.Errorf("component %q = %q, want unknown when unconfigured", c.ID, c.Status)
		}
	}
	// Unknown must not be reported as an outage.
	if snap.Overall != StatusOperational {
		t.Errorf("overall = %q, want operational (unknown ⇒ not an outage)", snap.Overall)
	}
}

// A source that panics must be caught: the snapshot degrades to all-unknown.
func TestPublicSnapshot_PanicGuard(t *testing.T) {
	agg := New(Config{DB: &fakeDB{}, PoPs: fakePoPs{panic: true}, Probe: okProbe, Now: t0})
	snap := agg.PublicSnapshot(context.Background()) // must not panic
	if len(snap.Components) != 4 {
		t.Fatalf("components = %d, want 4", len(snap.Components))
	}
	if snap.Overall != StatusUnknown {
		t.Errorf("overall = %q, want unknown after panic-guard", snap.Overall)
	}
}

// Within the TTL, a second call is served from cache (no extra DB ping).
func TestPublicSnapshot_Cache(t *testing.T) {
	now := t0()
	db := &fakeDB{}
	agg := New(Config{DB: db, Probe: okProbe, PublicCacheTTL: time.Minute, Now: func() time.Time { return now }})
	_ = agg.PublicSnapshot(context.Background())
	first := db.calls
	_ = agg.PublicSnapshot(context.Background())
	if db.calls != first {
		t.Errorf("cache miss: DB pinged again within TTL (calls %d → %d)", first, db.calls)
	}
	// Advance beyond TTL → recompute.
	now = now.Add(2 * time.Minute)
	_ = agg.PublicSnapshot(context.Background())
	if db.calls <= first {
		t.Errorf("expected recompute after TTL, calls still %d", db.calls)
	}
}

func TestRelayComponent_DegradedAndDown(t *testing.T) {
	// One healthy, one unhealthy EU PoP → degraded.
	agg := New(Config{PoPs: fakePoPs{pops: []PoP{
		{ID: "a", Region: "eu", Healthy: true},
		{ID: "b", Region: "eu-west", Healthy: false},
	}}, Now: t0})
	c := agg.relayComponent(context.Background(), "relay_eu", "Relay — EU", euRegions)
	if c.Status != StatusDegraded {
		t.Errorf("mixed health = %q, want degraded", c.Status)
	}
	// All unhealthy JHB → down.
	agg2 := New(Config{PoPs: fakePoPs{pops: []PoP{{ID: "j", Region: "africa", Healthy: false}}}, Now: t0})
	c2 := agg2.relayComponent(context.Background(), "relay_jhb", "Relay — JHB", jhbRegions)
	if c2.Status != StatusDown {
		t.Errorf("all-unhealthy = %q, want down", c2.Status)
	}
}

/* ─── account snapshot ───────────────────────────────────────────────────── */

func TestAccountSnapshot_Scoped(t *testing.T) {
	hbA := t0().Add(-2 * time.Minute)
	hbB := t0().Add(-2 * time.Hour)
	agg := New(Config{
		Devices: fakeDevices{byAccount: map[string][]Device{
			"acct-A": {{ULID: "01A", Name: "boxA", Health: "ok", LastHeartbeat: &hbA}},
			"acct-B": {{ULID: "01B", Name: "boxB", Health: "ok", LastHeartbeat: &hbB}},
		}},
		Relay:          fakeRelay{usage: RelayUsage{Bytes: 1234, Sessions: 3}},
		PoPs:           fakePoPs{pops: []PoP{{ID: "p", Region: "eu", Healthy: true}}},
		HeartbeatFresh: 10 * time.Minute,
		Now:            t0,
	})
	a := agg.AccountSnapshot(context.Background(), "acct-A")
	if len(a.Boxes) != 1 || a.Boxes[0].ULID != "01A" {
		t.Fatalf("acct-A boxes = %+v, want only 01A", a.Boxes)
	}
	if !a.Boxes[0].Reachable {
		t.Errorf("boxA recent heartbeat should be reachable")
	}
	if a.Relay.Bytes != 1234 || a.Relay.Sessions != 3 {
		t.Errorf("relay usage = %+v, want 1234/3", a.Relay)
	}
	if a.Relay.Health != "ok" {
		t.Errorf("relay health = %q, want ok", a.Relay.Health)
	}
	// Cross-account isolation: acct-A's view must never contain acct-B's box.
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "01B") || strings.Contains(string(raw), "boxB") {
		t.Errorf("acct-A snapshot leaked acct-B resources: %s", raw)
	}

	// acct-B's stale heartbeat → not reachable → degraded overall.
	b := agg.AccountSnapshot(context.Background(), "acct-B")
	if b.Boxes[0].Reachable {
		t.Errorf("boxB stale heartbeat should be unreachable")
	}
	if b.Overall != StatusDegraded {
		t.Errorf("acct-B overall = %q, want degraded", b.Overall)
	}
}

func TestAccountSnapshot_BlankAccount(t *testing.T) {
	agg := New(Config{Devices: fakeDevices{byAccount: map[string][]Device{"x": {{ULID: "1"}}}}, Now: t0})
	a := agg.AccountSnapshot(context.Background(), "")
	if len(a.Boxes) != 0 {
		t.Errorf("blank account returned boxes: %+v", a.Boxes)
	}
}

func TestAccountSnapshot_FailSafe(t *testing.T) {
	// Erroring + panicking sources must not crash and must return empty sections.
	agg := New(Config{Devices: fakeDevices{err: errors.New("db down")}, Relay: fakeRelay{err: errors.New("db down")}, Now: t0})
	a := agg.AccountSnapshot(context.Background(), "acct-A")
	if a.Boxes == nil || a.Services == nil || a.Events == nil {
		t.Errorf("nil slices must be empty, not null: %+v", a)
	}
	if len(a.Boxes) != 0 {
		t.Errorf("erroring device source should yield no boxes")
	}

	aggPanic := New(Config{Devices: fakeDevices{panic: true}, Now: t0})
	got := aggPanic.AccountSnapshot(context.Background(), "acct-A") // must not panic
	if got.Overall != StatusUnknown {
		t.Errorf("panic-guarded account overall = %q, want unknown", got.Overall)
	}
}

func TestAccountSnapshot_NilSourcesSafe(t *testing.T) {
	agg := New(Config{Now: t0})
	a := agg.AccountSnapshot(context.Background(), "acct-A")
	if len(a.Boxes) != 0 || len(a.Services) != 0 || len(a.Events) != 0 {
		t.Errorf("nil sources should produce empty snapshot, got %+v", a)
	}
	if a.Relay.Health != "unavailable" {
		t.Errorf("relay health = %q, want unavailable", a.Relay.Health)
	}
}

/* ─── helpers ────────────────────────────────────────────────────────────── */

func TestClassifyPoPs(t *testing.T) {
	cases := []struct {
		healthy, total int
		want           string
	}{
		{0, 0, StatusUnknown},
		{0, 2, StatusDown},
		{1, 2, StatusDegraded},
		{2, 2, StatusOperational},
	}
	for _, c := range cases {
		if got, _ := classifyPoPs(c.healthy, c.total); got != c.want {
			t.Errorf("classifyPoPs(%d,%d) = %q, want %q", c.healthy, c.total, got, c.want)
		}
	}
}

func TestRegionMatches(t *testing.T) {
	if !regionMatches("eu-central-1", euRegions) {
		t.Error("eu-central-1 should match EU")
	}
	if !regionMatches("africa-jhb", jhbRegions) {
		t.Error("africa-jhb should match JHB")
	}
	if regionMatches("us-east-1", euRegions) {
		t.Error("us-east-1 must not match EU")
	}
	if regionMatches("", euRegions) {
		t.Error("empty region must not match")
	}
}

func TestBoxReachable(t *testing.T) {
	now := t0()
	fresh := 10 * time.Minute
	recent := now.Add(-time.Minute)
	stale := now.Add(-time.Hour)
	if !boxReachable(Device{Health: "ok", LastHeartbeat: &recent}, now, fresh) {
		t.Error("recent + ok should be reachable")
	}
	if boxReachable(Device{Health: "ok", LastHeartbeat: &stale}, now, fresh) {
		t.Error("stale heartbeat should be unreachable")
	}
	if boxReachable(Device{Health: "down", LastHeartbeat: &recent}, now, fresh) {
		t.Error("explicit down should be unreachable regardless of heartbeat")
	}
	if !boxReachable(Device{Health: "healthy"}, now, fresh) {
		t.Error("no heartbeat but healthy signal should be reachable")
	}
	if boxReachable(Device{}, now, fresh) {
		t.Error("no signal at all should be unreachable")
	}
}
