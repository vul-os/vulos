package fabric_test

// Additional FABRIC-P2P-01 tests covering the gap fixes:
//
//   - Nudge() wiring: a LOCAL app-registry change (AppSync.LocalInstall) fires
//     the registered hook (the fabric Service.Nudge), so the running sync loop
//     converges IMMEDIATELY rather than waiting the (here: very long) tick.
//   - Multi-peer (>2 box) convergence: three in-process instances, each making a
//     distinct local change, all converge to identical registry state.
//   - GET /api/fabric/status returns peer + cursor + last-sync info.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vulos/backend/internal/fabric"
	"vulos/backend/internal/multiinstance"
)

// ── Nudge: a local change converges without waiting the tick ──────────────────

func TestNudgeTriggersImmediateSync(t *testing.T) {
	const (
		idA = "01HWZNUDGE000000000000000A"
		idB = "01HWZNUDGE000000000000000B"
	)

	// Build B first (no peers), then A pointing at B, with a DELIBERATELY long
	// sync interval so the ONLY way A's change reaches B in time is via Nudge.
	b := newInstance(t, idB)
	a := newInstanceWithInterval(t, idA, time.Hour, fabric.Peer{InstanceID: idB, BaseURL: b.server.URL})

	// Wire A's local-change hook to A's fabric Nudge (the production wiring).
	a.as.SetOnLocalChange(a.svc.Nudge)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.svc.Run(ctx) // immediate first round runs against B (nothing to send yet)

	// Give the loop a moment to perform its initial round and settle on the tick
	// before we make the local change, so convergence can only come from Nudge.
	time.Sleep(100 * time.Millisecond)

	// Now make a LOCAL install on A. This must Nudge the loop and push to B
	// WITHOUT waiting the 1h tick.
	if err := a.as.LocalInstall(idA, "browser", "3.0.0"); err != nil {
		t.Fatalf("local install: %v", err)
	}

	// B must learn about A/browser promptly (well under the 1h tick).
	if !waitFor(t, 5*time.Second, func() bool {
		snap := b.snapshot(t)
		_, ok := snap[idA+"/browser"]
		return ok
	}) {
		t.Fatalf("B did not learn A/browser via Nudge within the deadline; snapshot=%v", b.snapshot(t))
	}
}

// ── Multi-peer (>2 box) convergence ──────────────────────────────────────────

func TestThreeInstancesConverge(t *testing.T) {
	const (
		idA = "01HWZ3PEER000000000000000A"
		idB = "01HWZ3PEER000000000000000B"
		idC = "01HWZ3PEER000000000000000C"
	)

	// Stand up three bare instances, then rebuild each one knowing the other two
	// as peers (full mesh via StaticDiscoverer — the same seam the mDNS roster
	// path feeds in production).
	a0 := newInstance(t, idA)
	b0 := newInstance(t, idB)
	c0 := newInstance(t, idC)

	a := rebuildWithPeers(t, a0,
		fabric.Peer{InstanceID: idB, BaseURL: b0.server.URL},
		fabric.Peer{InstanceID: idC, BaseURL: c0.server.URL})
	b := rebuildWithPeers(t, b0,
		fabric.Peer{InstanceID: idA, BaseURL: a.server.URL},
		fabric.Peer{InstanceID: idC, BaseURL: c0.server.URL})
	c := rebuildWithPeers(t, c0,
		fabric.Peer{InstanceID: idA, BaseURL: a.server.URL},
		fabric.Peer{InstanceID: idB, BaseURL: b.server.URL})

	now := time.Now().UTC().Truncate(time.Millisecond)
	a.install(t, "browser", "1.0.0", now)
	b.install(t, "notes", "2.0.0", now.Add(time.Second))
	c.install(t, "mail", "3.0.0", now.Add(2*time.Second))

	ctx := context.Background()
	// A few full rounds: in a 3-node mesh, gossip needs more than one round to
	// propagate transitively (A learns C's row from B's push, etc.). Three
	// rounds each comfortably converges this size.
	for i := 0; i < 3; i++ {
		a.svc.SyncOnce(ctx)
		b.svc.SyncOnce(ctx)
		c.svc.SyncOnce(ctx)
	}

	want := []string{idA + "/browser", idB + "/notes", idC + "/mail"}
	for _, in := range []*instanceAlias{{"A", a}, {"B", b}, {"C", c}} {
		snap := in.inst.snapshot(t)
		for _, key := range want {
			if _, ok := snap[key]; !ok {
				t.Errorf("instance %s missing converged row %q; snapshot=%v", in.name, key, snap)
			}
		}
	}

	// All three snapshots must be identical.
	sa, sb, sc := a.snapshot(t), b.snapshot(t), c.snapshot(t)
	if len(sa) != len(sb) || len(sb) != len(sc) {
		t.Fatalf("snapshot size mismatch: A=%d B=%d C=%d", len(sa), len(sb), len(sc))
	}
	for key, ea := range sa {
		eb, ec := sb[key], sc[key]
		if ea.AppVersion != eb.AppVersion || ea.AppVersion != ec.AppVersion ||
			ea.Installed != eb.Installed || ea.Installed != ec.Installed {
			t.Errorf("diverged on %q: A=%+v B=%+v C=%+v", key, ea, eb, ec)
		}
	}
}

// ── Status endpoint ──────────────────────────────────────────────────────────

func TestStatusEndpointReportsPeersAndCursor(t *testing.T) {
	const (
		idA = "01HWZSTATUS00000000000000A"
		idB = "01HWZSTATUS00000000000000B"
	)
	b := newInstance(t, idB)
	a := newInstance(t, idA, fabric.Peer{InstanceID: idB, BaseURL: b.server.URL})

	// Drive one sync round so A records a cursor + last-sync time for B.
	b.install(t, "notes", "1.0.0", time.Now().UTC().Truncate(time.Millisecond))
	a.svc.SyncOnce(context.Background())

	// GET /api/fabric/status on A with the shared secret.
	req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/fabric/status", nil)
	req.Header.Set("X-Fabric-Auth", fabricSecret)
	resp, err := a.server.Client().Do(req)
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	var st fabric.FabricStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.InstanceID != idA {
		t.Errorf("status instance id: got %q want %q", st.InstanceID, idA)
	}
	if st.LastRunAt == nil {
		t.Errorf("status missing last_run_at after a sync round")
	}
	if st.PeerCount < 1 || len(st.Peers) < 1 {
		t.Fatalf("status reported no peers; %+v", st)
	}
	var bPeer *fabric.PeerSyncStatus
	for i := range st.Peers {
		if st.Peers[i].BaseURL == b.server.URL {
			bPeer = &st.Peers[i]
		}
	}
	if bPeer == nil {
		t.Fatalf("status missing peer B (%s); peers=%+v", b.server.URL, st.Peers)
	}
	if bPeer.LastSyncAt == nil {
		t.Errorf("peer B missing last_sync_at")
	}
	if bPeer.LastError != "" {
		t.Errorf("peer B unexpected last_error: %q", bPeer.LastError)
	}
	if bPeer.Cursor == nil {
		t.Errorf("peer B missing cursor after a successful sync that merged a row")
	}
}

func TestStatusEndpointRequiresAuth(t *testing.T) {
	a := newInstance(t, "01HWZSTATUS00000000000AUTH")
	req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/fabric/status", nil)
	// No X-Fabric-Auth.
	resp, err := a.server.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: got %d want 401", resp.StatusCode)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// instanceAlias pairs a label with an instance for clearer multi-peer assertions.
type instanceAlias struct {
	name string
	inst *instance
}

// newInstanceWithInterval is newInstance but with an explicit (long) sync
// interval so a test can prove Nudge — not the tick — drove convergence.
func newInstanceWithInterval(t *testing.T, id string, interval time.Duration, peers ...fabric.Peer) *instance {
	t.Helper()
	dir := t.TempDir()
	reg, err := multiinstance.Open(dir + "/instances.db")
	if err != nil {
		t.Fatalf("[%s] open registry: %v", id, err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	as, err := multiinstance.OpenAppSync(reg)
	if err != nil {
		t.Fatalf("[%s] open appsync: %v", id, err)
	}
	mux := http.NewServeMux()
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	svc, err := fabric.New(fabric.Config{
		InstanceID:   id,
		Secret:       fabricSecret,
		AppSync:      as,
		Discoverer:   fabric.NewStaticDiscoverer(peers...),
		HTTPClient:   server.Client(),
		SyncInterval: interval,
		SelfBaseURLs: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("[%s] new fabric service: %v", id, err)
	}
	svc.RegisterHandlers(mux)
	return &instance{id: id, reg: reg, as: as, svc: svc, server: server}
}

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
