package georoute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// helper: assemble a 3-region service with a counting trigger + static
// health probe. Returns (svc, probe, trigger).
func newTestService(t *testing.T, st Store, local string) (*Service, StaticHealth, *CountingSyncTrigger) {
	t.Helper()
	probe := StaticHealth{
		"eu": true,
		"us": true,
		"af": true,
	}
	trig := &CountingSyncTrigger{}
	cfg := Config{
		Regions: []RegionConfig{
			{Name: "eu", Endpoint: "https://eu.cp.example"},
			{Name: "us", Endpoint: "https://us.cp.example"},
			{Name: "af", Endpoint: "https://af.cp.example"},
		},
		DefaultRegion: "eu",
		LocalRegion:   local,
	}
	svc, err := NewService(st, probe, trig, cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, probe, trig
}

// ────────────────────────────────────────────────────────────────────────────
// Store: assignment + retrieval (MemStore + SQLStore parity)
// ────────────────────────────────────────────────────────────────────────────

func TestStore_AssignAndHomeRegion_Mem(t *testing.T) {
	testAssignAndHomeRegion(t, NewMemStore())
}

func TestStore_AssignAndHomeRegion_SQL(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "georoute.db") + "?_pragma=journal_mode(WAL)"
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	testAssignAndHomeRegion(t, st)
}

func testAssignAndHomeRegion(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Assign(ctx, "tenant-1", "eu"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	got, err := st.HomeRegion(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("HomeRegion: %v", err)
	}
	if got != "eu" {
		t.Fatalf("home region = %q; want %q", got, "eu")
	}

	// REGION-SSOT-01: home_region is immutable after first set. Re-Assigning the
	// SAME region is an idempotent no-op; changing it via Assign is rejected.
	if _, err := st.Assign(ctx, "tenant-1", "eu"); err != nil {
		t.Fatalf("idempotent re-Assign same region: %v", err)
	}
	if _, err := st.Assign(ctx, "tenant-1", "us"); err != ErrRegionLocked {
		t.Fatalf("Assign to a different region: %v; want ErrRegionLocked", err)
	}
	got, _ = st.HomeRegion(ctx, "tenant-1")
	if got != "eu" {
		t.Fatalf("after rejected re-assign home region = %q; want %q (unchanged)", got, "eu")
	}

	// The EXPLICIT failover/migration path (ForceAssign) DOES move the row.
	if _, err := st.ForceAssign(ctx, "tenant-1", "us"); err != nil {
		t.Fatalf("ForceAssign: %v", err)
	}
	got, _ = st.HomeRegion(ctx, "tenant-1")
	if got != "us" {
		t.Fatalf("after ForceAssign home region = %q; want %q", got, "us")
	}

	// Unknown tenant.
	if _, err := st.HomeRegion(ctx, "missing"); err != ErrUnknownTenant {
		t.Fatalf("HomeRegion(missing): %v; want ErrUnknownTenant", err)
	}

	// Blank inputs rejected.
	if _, err := st.Assign(ctx, "", "eu"); err != ErrInvalidInput {
		t.Fatalf("Assign(blank tenant): %v; want ErrInvalidInput", err)
	}
	if _, err := st.Assign(ctx, "t", ""); err != ErrInvalidInput {
		t.Fatalf("Assign(blank region): %v; want ErrInvalidInput", err)
	}
}

func TestStore_List(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	_, _ = st.Assign(ctx, "a", "eu")
	_, _ = st.Assign(ctx, "b", "us")
	got, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d; want 2", len(got))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Service: hint assignment, region-pick logic
// ────────────────────────────────────────────────────────────────────────────

func TestService_AssignFromHint_ExplicitHintWins(t *testing.T) {
	svc, _, _ := newTestService(t, NewMemStore(), "eu")
	a, err := svc.AssignFromHint(context.Background(), "t-1", "us", "196.1.1.1", StaticIPResolver{"196.": "af"})
	if err != nil {
		t.Fatalf("AssignFromHint: %v", err)
	}
	if a.HomeRegion != "us" {
		t.Fatalf("region = %q; want us (explicit hint must win)", a.HomeRegion)
	}
}

func TestService_AssignFromHint_GeoIPFallback(t *testing.T) {
	svc, _, _ := newTestService(t, NewMemStore(), "eu")
	resolver := StaticIPResolver{"196.": "af"}
	a, err := svc.AssignFromHint(context.Background(), "t-1", "", "196.5.5.5", resolver)
	if err != nil {
		t.Fatalf("AssignFromHint: %v", err)
	}
	if a.HomeRegion != "af" {
		t.Fatalf("region = %q; want af (geo-IP fallback)", a.HomeRegion)
	}
}

func TestService_AssignFromHint_Default(t *testing.T) {
	svc, _, _ := newTestService(t, NewMemStore(), "eu")
	a, err := svc.AssignFromHint(context.Background(), "t-1", "", "", nil)
	if err != nil {
		t.Fatalf("AssignFromHint: %v", err)
	}
	if a.HomeRegion != "eu" {
		t.Fatalf("region = %q; want eu (default)", a.HomeRegion)
	}
}

func TestService_Assign_RejectsUnknownRegion(t *testing.T) {
	svc, _, _ := newTestService(t, NewMemStore(), "eu")
	if _, err := svc.Assign(context.Background(), "t-1", "antarctica"); err == nil {
		t.Fatalf("expected error assigning to unknown region")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// AlternateRegion: deterministic ring + failover skips sick
// ────────────────────────────────────────────────────────────────────────────

func TestAlternateRegion_DeterministicRing(t *testing.T) {
	svc, _, _ := newTestService(t, NewMemStore(), "eu")
	ctx := context.Background()
	// All healthy: alt of eu is us (next in ring).
	if got := svc.AlternateRegion(ctx, "eu"); got != "us" {
		t.Fatalf("alt(eu) = %q; want us", got)
	}
	if got := svc.AlternateRegion(ctx, "us"); got != "af" {
		t.Fatalf("alt(us) = %q; want af", got)
	}
	if got := svc.AlternateRegion(ctx, "af"); got != "eu" {
		t.Fatalf("alt(af) = %q; want eu (wraps)", got)
	}
	// Repeat — must be SAME (determinism).
	for i := 0; i < 5; i++ {
		if got := svc.AlternateRegion(ctx, "eu"); got != "us" {
			t.Fatalf("alt(eu) iter %d = %q; want us", i, got)
		}
	}
}

func TestAlternateRegion_SkipsUnhealthy(t *testing.T) {
	svc, probe, _ := newTestService(t, NewMemStore(), "eu")
	probe["us"] = false // mark next-in-ring sick
	ctx := context.Background()
	if got := svc.AlternateRegion(ctx, "eu"); got != "af" {
		t.Fatalf("alt(eu) with us sick = %q; want af", got)
	}
	// All non-home unhealthy → "".
	probe["af"] = false
	if got := svc.AlternateRegion(ctx, "eu"); got != "" {
		t.Fatalf("alt(eu) all-sick = %q; want \"\"", got)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Failover: triggers sync via SyncTrigger seam
// ────────────────────────────────────────────────────────────────────────────

func TestFailover_HealthyHome_NoTrigger(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, _, trig := newTestService(t, st, "eu")
	if _, err := svc.Assign(ctx, "t-1", "eu"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	region, triggered, err := svc.Failover(ctx, "t-1", "default")
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if region != "eu" {
		t.Fatalf("region = %q; want eu", region)
	}
	if triggered {
		t.Fatalf("triggered = true; want false (home healthy)")
	}
	if len(trig.Calls) != 0 {
		t.Fatalf("SyncTrigger calls = %d; want 0", len(trig.Calls))
	}
}

func TestFailover_UnhealthyHome_TriggersSync(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, probe, trig := newTestService(t, st, "eu")
	if _, err := svc.Assign(ctx, "t-1", "eu"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	probe["eu"] = false // home sick

	region, triggered, err := svc.Failover(ctx, "t-1", "mailstore")
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if region != "us" {
		t.Fatalf("region = %q; want us (deterministic alt)", region)
	}
	if !triggered {
		t.Fatalf("triggered = false; want true")
	}
	if len(trig.Calls) != 1 {
		t.Fatalf("SyncTrigger calls = %d; want 1", len(trig.Calls))
	}
	c := trig.Calls[0]
	if c.TenantID != "t-1" || c.SyncKey != "mailstore" {
		t.Fatalf("call = %+v; want tenant=t-1 syncKey=mailstore", c)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// DR-FAILOVER-01: multi-region failover (home down → ring picks next healthy)
// ────────────────────────────────────────────────────────────────────────────

// newTestServiceN assembles a service with an N-region ring r0..r{n-1} (all
// healthy) so the multi-region failover scenarios can walk several hops.
func newTestServiceN(t *testing.T, st Store, names ...string) (*Service, StaticHealth, *CountingSyncTrigger) {
	t.Helper()
	probe := StaticHealth{}
	regs := make([]RegionConfig, 0, len(names))
	for _, n := range names {
		probe[n] = true
		regs = append(regs, RegionConfig{Name: n, Endpoint: "https://" + n + ".cp.example"})
	}
	trig := &CountingSyncTrigger{}
	svc, err := NewService(st, probe, trig, Config{Regions: regs, DefaultRegion: names[0], LocalRegion: names[0]})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, probe, trig
}

// TestMultiRegionFailover_HomeDown_PicksNextHealthy is the headline DR test: a
// tenant's home region goes down and the failover ring deterministically routes
// to the NEXT healthy region — walking PAST any intervening sick regions — and
// fires the cross-region convergence trigger exactly once.
func TestMultiRegionFailover_HomeDown_PicksNextHealthy(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	// 4-region ring: eu → us → af → ap (ring order is the config order).
	svc, probe, trig := newTestServiceN(t, st, "eu", "us", "af", "ap")
	if _, err := svc.Assign(ctx, "tenant-dr", "eu"); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	// Steady state: home healthy → no failover.
	if region, isFO, _ := svc.EffectiveRegion(ctx, "tenant-dr"); region != "eu" || isFO {
		t.Fatalf("steady state: got (%q, failover=%v), want (eu, false)", region, isFO)
	}

	// Home (eu) AND the first alternate (us) go down. The ring must SKIP us and
	// land on af — the next healthy region after the home slot.
	probe["eu"] = false
	probe["us"] = false

	region, triggered, err := svc.Failover(ctx, "tenant-dr", "default")
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if region != "af" {
		t.Fatalf("failover region = %q; want af (ring skips the sick us)", region)
	}
	if !triggered {
		t.Fatalf("triggered = false; want true (failed over to a non-home region)")
	}
	if len(trig.Calls) != 1 {
		t.Fatalf("SyncTrigger calls = %d; want exactly 1", len(trig.Calls))
	}

	// Determinism: the SAME (home, healthy-set) always resolves to the SAME alt.
	for i := 0; i < 5; i++ {
		if alt := svc.AlternateRegion(ctx, "eu"); alt != "af" {
			t.Fatalf("AlternateRegion(eu) iter %d = %q; want af (deterministic)", i, alt)
		}
	}

	// Endpoint resolves to the failover region's public URL (what a redirect uses).
	if ep := svc.EndpointForRegion(region); ep != "https://af.cp.example" {
		t.Fatalf("EndpointForRegion(af) = %q; want https://af.cp.example", ep)
	}
}

// TestMultiRegionFailover_RingWrapsAround verifies the ring wraps: when the home
// is the LAST region in the ring and it is down, failover wraps to the first
// healthy region from the top.
func TestMultiRegionFailover_RingWrapsAround(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, probe, _ := newTestServiceN(t, st, "eu", "us", "af")
	if _, err := svc.Assign(ctx, "tenant-wrap", "af"); err != nil { // home is the last slot
		t.Fatalf("Assign: %v", err)
	}
	probe["af"] = false // home (last) down

	region, isFO, err := svc.EffectiveRegion(ctx, "tenant-wrap")
	if err != nil {
		t.Fatalf("EffectiveRegion: %v", err)
	}
	if !isFO {
		t.Fatalf("isFailover = false; want true (home af down)")
	}
	if region != "eu" {
		t.Fatalf("failover wrapped to %q; want eu (ring wraps to the top)", region)
	}
}

// TestMultiRegionFailover_AllAltsDown_FallsBackToHome verifies the safety net:
// if NO alternate is healthy, the service bounces the caller to home anyway
// (better to hit a recovering home than to route nowhere).
func TestMultiRegionFailover_AllAltsDown_FallsBackToHome(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, probe, trig := newTestServiceN(t, st, "eu", "us", "af")
	if _, err := svc.Assign(ctx, "tenant-allsick", "eu"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	// Everything is down.
	probe["eu"] = false
	probe["us"] = false
	probe["af"] = false

	region, triggered, err := svc.Failover(ctx, "tenant-allsick", "default")
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if region != "eu" {
		t.Fatalf("no healthy alt: region = %q; want eu (bounce to home)", region)
	}
	if triggered {
		t.Fatalf("triggered = true; want false (no alternate, no convergence)")
	}
	if len(trig.Calls) != 0 {
		t.Fatalf("SyncTrigger calls = %d; want 0 (no failover occurred)", len(trig.Calls))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Middleware: pass-through, redirect, opt-in gate
// ────────────────────────────────────────────────────────────────────────────

func TestMiddleware_PassThroughOnHomeRegion(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, _, _ := newTestService(t, st, "eu")
	_, _ = svc.Assign(ctx, "t-1", "eu")

	served := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	mw := Middleware(svc, MiddlewareConfig{})(next)

	req := httptest.NewRequest("GET", "/api/whatever", nil)
	req.Header.Set("X-Tenant-ID", "t-1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !served {
		t.Fatalf("local handler NOT called; expected pass-through")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
}

func TestMiddleware_RedirectsToHomeRegion(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, _, _ := newTestService(t, st, "eu") // local=eu
	_, _ = svc.Assign(ctx, "t-1", "us")      // home=us — must bounce

	served := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { served = true })
	mw := Middleware(svc, MiddlewareConfig{})(next)

	req := httptest.NewRequest("POST", "/api/mail/push?x=1", nil)
	req.Header.Set("X-Tenant-ID", "t-1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if served {
		t.Fatalf("local handler called; expected 307 redirect")
	}
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d; want 307", rec.Code)
	}
	loc := rec.Header().Get("Location")
	want := "https://us.cp.example/api/mail/push?x=1"
	if loc != want {
		t.Fatalf("Location = %q; want %q", loc, want)
	}
	if got := rec.Header().Get("X-Georoute"); got != WireProto {
		t.Fatalf("X-Georoute = %q; want %q", got, WireProto)
	}
	if got := rec.Header().Get("X-Georoute-Region"); got != "us" {
		t.Fatalf("X-Georoute-Region = %q; want us", got)
	}
}

func TestMiddleware_RedirectsOnFailover(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, probe, trig := newTestService(t, st, "eu") // local=eu
	_, _ = svc.Assign(ctx, "t-1", "eu")             // home=eu
	probe["eu"] = false                             // home sick → alt = us

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatalf("local handler called; expected failover redirect")
	})
	mw := Middleware(svc, MiddlewareConfig{SyncKey: "office"})(next)

	req := httptest.NewRequest("GET", "/api/sync/pull", nil)
	req.Header.Set("X-Tenant-ID", "t-1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d; want 307", rec.Code)
	}
	if got := rec.Header().Get("X-Georoute-Failover"); got != "1" {
		t.Fatalf("X-Georoute-Failover = %q; want 1", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "https://us.cp.example/") {
		t.Fatalf("Location = %q; want https://us.cp.example/ prefix", rec.Header().Get("Location"))
	}
	// Sync trigger fired.
	if len(trig.Calls) != 1 || trig.Calls[0].SyncKey != "office" {
		t.Fatalf("SyncTrigger calls = %+v; want 1 with SyncKey=office", trig.Calls)
	}
}

func TestMiddleware_NoTenantHeader_PassThrough(t *testing.T) {
	svc, _, _ := newTestService(t, NewMemStore(), "eu")
	served := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusNoContent)
	})
	mw := Middleware(svc, MiddlewareConfig{})(next)

	req := httptest.NewRequest("GET", "/api/whatever", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if !served {
		t.Fatalf("expected pass-through for pre-tenant request")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; want 204", rec.Code)
	}
}

func TestMiddleware_SkipsHealthPaths(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, _, _ := newTestService(t, st, "eu")
	_, _ = svc.Assign(ctx, "t-1", "us") // would otherwise redirect

	served := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	mw := Middleware(svc, MiddlewareConfig{})(next)

	for _, p := range []string{"/api/ha/health", "/healthz", "/readyz", "/metrics"} {
		served = false
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("X-Tenant-ID", "t-1")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if !served {
			t.Fatalf("path %s was redirected; expected pass-through", p)
		}
	}
}

func TestHandlerNoop_OptInGateDefault(t *testing.T) {
	// The opt-in gate in main.go: when CP_GEOROUTE_ENABLE is unset, the
	// wiring uses HandlerNoop() so requests pass through untouched. This test
	// asserts the no-op wrapper does NOT touch the response.
	served := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusTeapot)
	})
	wrapped := HandlerNoop()(next)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/anything", nil))
	if !served {
		t.Fatalf("noop wrapper did not call next")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d; want 418 (untouched)", rec.Code)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Service constructor: empty regions rejected
// ────────────────────────────────────────────────────────────────────────────

func TestNewService_RejectsEmptyRegions(t *testing.T) {
	_, err := NewService(NewMemStore(), nil, nil, Config{})
	if err != ErrNoRegions {
		t.Fatalf("NewService(empty) = %v; want ErrNoRegions", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// EffectiveRegion: no-trigger read path
// ────────────────────────────────────────────────────────────────────────────

func TestEffectiveRegion_FailoverDoesNotTrigger(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc, probe, trig := newTestService(t, st, "eu")
	_, _ = svc.Assign(ctx, "t-1", "eu")
	probe["eu"] = false

	region, isFailover, err := svc.EffectiveRegion(ctx, "t-1")
	if err != nil {
		t.Fatalf("EffectiveRegion: %v", err)
	}
	if region != "us" || !isFailover {
		t.Fatalf("EffectiveRegion = (%q, %v); want (us, true)", region, isFailover)
	}
	if len(trig.Calls) != 0 {
		t.Fatalf("EffectiveRegion fired trigger; expected READ-only")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// StaticIPResolver: longest-prefix wins
// ────────────────────────────────────────────────────────────────────────────

func TestStaticIPResolver_LongestPrefixWins(t *testing.T) {
	r := StaticIPResolver{
		"196.":     "af",
		"196.1.":   "af-west",
		"196.1.1.": "af-west-1",
		"":         "default",
	}
	if got := r.RegionForIP("196.1.1.5"); got != "af-west-1" {
		t.Fatalf("RegionForIP = %q; want af-west-1", got)
	}
	if got := r.RegionForIP("196.2.5.5"); got != "af" {
		t.Fatalf("RegionForIP = %q; want af", got)
	}
	if got := r.RegionForIP("8.8.8.8"); got != "default" {
		t.Fatalf("RegionForIP = %q; want default", got)
	}
}
