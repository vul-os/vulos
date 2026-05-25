package fabric_test

// FABRIC-P2P-01 tests: REAL same-LAN peer-to-peer CRDT sync.
//
// The headline test (TestTwoInstancesConvergeOverLAN) stands up TWO in-process
// Vulos instances, each with its own registry/AppSync and its own HTTPS
// listener (httptest.NewTLSServer — exactly the LAN-listener shape: TLS over a
// loopback socket). Each instance makes a DIFFERENT local app change, then a
// single sync round runs in BOTH directions, and we assert that both instances
// end with IDENTICAL converged registry state. That proves the discovery →
// authenticated changeset exchange → deterministic merge pipeline actually
// works end-to-end over the wire, not just in the in-memory merge unit tests.
//
// TestUnauthenticatedPeerRejected proves the auth gate: a request without (or
// with a wrong) X-Fabric-Auth secret is rejected with 401 before any registry
// access, so a random LAN host cannot inject or read registry state.

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/internal/fabric"
	"vulos/backend/internal/multiinstance"
)

const fabricSecret = "shared-fabric-secret-abc123"

// instance bundles one box's registry + AppSync + fabric service + LAN listener.
type instance struct {
	id     string
	reg    *multiinstance.Registry
	as     *multiinstance.AppSync
	svc    *fabric.Service
	server *httptest.Server
}

// newInstance builds a fully-wired in-process Vulos instance whose fabric
// handlers are served over a TLS httptest listener (the LAN-listener shape).
func newInstance(t *testing.T, id string, peers ...fabric.Peer) *instance {
	t.Helper()
	dir := t.TempDir()
	reg, err := multiinstance.Open(filepath.Join(dir, "instances.db"))
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

	// httptest.Server.Client() trusts the server's self-signed cert — that is
	// our LAN-client seam (production uses fabric.NewLANClient).
	svc, err := fabric.New(fabric.Config{
		InstanceID:   id,
		Secret:       fabricSecret,
		AppSync:      as,
		Discoverer:   fabric.NewStaticDiscoverer(peers...),
		HTTPClient:   server.Client(),
		SyncInterval: time.Hour, // we drive SyncOnce manually in tests
		SelfBaseURLs: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("[%s] new fabric service: %v", id, err)
	}
	svc.RegisterHandlers(mux)

	return &instance{id: id, reg: reg, as: as, svc: svc, server: server}
}

// install records a local app install on this instance's registry at ts.
func (in *instance) install(t *testing.T, appID, version string, ts time.Time) {
	t.Helper()
	cs, err := in.as.EmitChangeset(in.id, []multiinstance.AppRegistryEntry{{
		InstanceULID: in.id,
		AppID:        appID,
		AppVersion:   version,
		Installed:    true,
		InstalledBy:  in.id,
		UpdatedAt:    ts.UTC(),
	}})
	if err != nil {
		t.Fatalf("[%s] emit local install: %v", in.id, err)
	}
	if err := in.as.ApplyChangeset(cs); err != nil {
		t.Fatalf("[%s] apply local install: %v", in.id, err)
	}
}

// snapshot returns a deterministic comparable view of the full registry.
func (in *instance) snapshot(t *testing.T) map[string]multiinstance.AppRegistryEntry {
	t.Helper()
	all, err := in.as.ListAllApps()
	if err != nil {
		t.Fatalf("[%s] list all apps: %v", in.id, err)
	}
	out := make(map[string]multiinstance.AppRegistryEntry, len(all))
	for _, e := range all {
		out[e.InstanceULID+"/"+e.AppID] = e
	}
	return out
}

// ── Headline convergence test ────────────────────────────────────────────────

func TestTwoInstancesConvergeOverLAN(t *testing.T) {
	const (
		idA = "01HWZFABRIC0000000000000A"
		idB = "01HWZFABRIC0000000000000B"
	)

	// Build A first without peers, then B pointing at A, then tell A about B.
	a := newInstance(t, idA)
	b := newInstance(t, idB, fabric.Peer{InstanceID: idA, BaseURL: a.server.URL})
	// Point A at B now that B's server URL is known.
	a.svc = nil // discard the no-peer service; rebuild A with B as a peer
	a = rebuildWithPeers(t, a, fabric.Peer{InstanceID: idB, BaseURL: b.server.URL})

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Each instance makes a DIFFERENT local change.
	a.install(t, "browser", "1.0.0", now)
	b.install(t, "notes", "2.1.0", now.Add(time.Second))

	ctx := context.Background()

	// One sync round each direction. A pulls+pushes with B; B pulls+pushes with A.
	a.svc.SyncOnce(ctx)
	b.svc.SyncOnce(ctx)
	// A second pass guarantees the row each side learned in round 1 is reflected
	// back (pull cursor semantics): convergence within a couple of rounds.
	a.svc.SyncOnce(ctx)
	b.svc.SyncOnce(ctx)

	snapA := a.snapshot(t)
	snapB := b.snapshot(t)

	// Both must now know about BOTH apps.
	for _, key := range []string{idA + "/browser", idB + "/notes"} {
		if _, ok := snapA[key]; !ok {
			t.Errorf("instance A missing converged row %q; snapshot=%v", key, snapA)
		}
		if _, ok := snapB[key]; !ok {
			t.Errorf("instance B missing converged row %q; snapshot=%v", key, snapB)
		}
	}

	// And the two snapshots must be IDENTICAL (deterministic convergence).
	if len(snapA) != len(snapB) {
		t.Fatalf("snapshot size mismatch: A=%d B=%d", len(snapA), len(snapB))
	}
	for key, ea := range snapA {
		eb, ok := snapB[key]
		if !ok {
			t.Fatalf("key %q present on A, absent on B", key)
		}
		if ea.AppVersion != eb.AppVersion || ea.Installed != eb.Installed || ea.InstalledBy != eb.InstalledBy || !ea.UpdatedAt.Equal(eb.UpdatedAt) {
			t.Errorf("diverged on %q: A=%+v B=%+v", key, ea, eb)
		}
	}
}

// rebuildWithPeers swaps an instance's fabric service for one that knows about
// the given peers, reusing the same registry/AppSync/server/mux is not possible
// (handlers are already registered), so we register on a fresh mux+server. The
// previous server is closed by its own t.Cleanup.
func rebuildWithPeers(t *testing.T, old *instance, peers ...fabric.Peer) *instance {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	svc, err := fabric.New(fabric.Config{
		InstanceID:   old.id,
		Secret:       fabricSecret,
		AppSync:      old.as,
		Discoverer:   fabric.NewStaticDiscoverer(peers...),
		HTTPClient:   server.Client(),
		SyncInterval: time.Hour,
		SelfBaseURLs: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("[%s] rebuild fabric service: %v", old.id, err)
	}
	svc.RegisterHandlers(mux)
	return &instance{id: old.id, reg: old.reg, as: old.as, svc: svc, server: server}
}

// ── Auth rejection test ───────────────────────────────────────────────────────

func TestUnauthenticatedPeerRejected(t *testing.T) {
	a := newInstance(t, "01HWZFABRIC00000000000AUTH")
	a.install(t, "vault", "1.0.0", time.Now())

	client := a.server.Client()

	cases := []struct {
		name   string
		secret string
		setHdr bool
	}{
		{"no auth header", "", false},
		{"empty secret", "", true},
		{"wrong secret", "not-the-secret", true},
	}

	for _, tc := range cases {
		t.Run("GET/"+tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/fabric/changeset", nil)
			if tc.setHdr {
				req.Header.Set("X-Fabric-Auth", tc.secret)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET with %s: got status %d, want 401", tc.name, resp.StatusCode)
			}
		})

		t.Run("POST/"+tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, a.server.URL+"/api/fabric/changeset", nil)
			if tc.setHdr {
				req.Header.Set("X-Fabric-Auth", tc.secret)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("POST with %s: got status %d, want 401", tc.name, resp.StatusCode)
			}
		})
	}

	// And the correct secret is accepted (200) — proves we reject only the bad ones.
	req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/fabric/changeset", nil)
	req.Header.Set("X-Fabric-Auth", fabricSecret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authorized request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authorized GET: got status %d, want 200", resp.StatusCode)
	}
}

// ── Auth rejection at the merge boundary: a hostile peer cannot inject ─────────

func TestPushFromUnauthenticatedPeerDoesNotMerge(t *testing.T) {
	a := newInstance(t, "01HWZFABRIC0000000000INJ1")

	// Hostile changeset that, if merged, would seed a fake app on a victim ULID.
	hostile := &multiinstance.AppChangeset{
		OriginULID: "attacker",
		PeerCount:  1,
		Entries: []multiinstance.AppRegistryEntry{{
			InstanceULID: "victim-ulid",
			AppID:        "malware",
			AppVersion:   "9.9.9",
			Installed:    true,
			InstalledBy:  "attacker",
			UpdatedAt:    time.Now().UTC(),
		}},
	}
	body, _ := multiinstance.MarshalChangeset(hostile)

	req, _ := http.NewRequest(http.MethodPost, a.server.URL+"/api/fabric/changeset",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Fabric-Auth — must be rejected.
	resp, err := a.server.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated push: got status %d, want 401", resp.StatusCode)
	}

	// The hostile row must NOT have been merged.
	all, err := a.as.ListAllApps()
	if err != nil {
		t.Fatalf("list all apps: %v", err)
	}
	for _, e := range all {
		if e.AppID == "malware" {
			t.Fatalf("hostile row was merged despite missing auth: %+v", e)
		}
	}
}

// ── Sanity: the exchange really happens over TLS (LAN-listener shape) ─────────

func TestListenerIsTLS(t *testing.T) {
	a := newInstance(t, "01HWZFABRIC0000000000TLS1")

	// A client that does NOT trust the listener's self-signed cert must fail the
	// handshake — proving the listener is genuinely TLS-protected, not plaintext.
	untrusting := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, // system roots only
		},
	}
	req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/fabric/changeset", nil)
	req.Header.Set("X-Fabric-Auth", fabricSecret)
	if _, err := untrusting.Do(req); err == nil {
		t.Fatal("expected TLS verification error from a client that does not trust the LAN cert")
	}

	// The trusting (LAN-cert-aware) client succeeds — the same role
	// fabric.NewLANClient fills in production.
	req2, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/fabric/changeset", nil)
	req2.Header.Set("X-Fabric-Auth", fabricSecret)
	resp, err := a.server.Client().Do(req2)
	if err != nil {
		t.Fatalf("trusting client over TLS: %v", err)
	}
	_ = resp.Body.Close()
}
