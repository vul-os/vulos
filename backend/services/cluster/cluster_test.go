package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock store — simulates S3 PutEncrypted / GetEncrypted / ListObjects.
// ---------------------------------------------------------------------------

// mockStore is a simple in-memory object store used to exercise cluster.go
// without requiring real S3 credentials.
type mockStore struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func newMockStore() *mockStore {
	return &mockStore{objects: make(map[string][]byte)}
}

func (m *mockStore) put(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.objects[key] = cp
}

func (m *mockStore) get(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.objects[key]
	if !ok {
		return nil, false
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, true
}

func (m *mockStore) keys(prefix string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for k := range m.objects {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// mockCluster wraps a Cluster replacing its S3 calls with the mock store.
// We do NOT call NewClient — we build the struct fields directly to test the
// business logic in isolation.
// ---------------------------------------------------------------------------

// mockCluster embeds a Cluster-like state and re-implements Register / Peers
// using a mockStore, exercising the same JSON marshal/unmarshal and stale
// detection logic as the real Cluster.
type mockCluster struct {
	meta  NodeMeta
	store *mockStore
}

func newMockCluster(id, mode string, storage bool) *mockCluster {
	return &mockCluster{
		meta: NodeMeta{
			ID:       id,
			Mode:     mode,
			Hostname: "test-host",
			Storage:  storage,
		},
		store: newMockStore(),
	}
}

// register writes the node meta to the mock store (mirrors Cluster.Register logic).
func (mc *mockCluster) register() error {
	mc.meta.LastSeen = time.Now().UTC()
	data, err := json.Marshal(mc.meta)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	key := nodeMetaPrefix + mc.meta.ID + "/meta.json"
	mc.store.put(key, data)
	return nil
}

// registerWithLastSeen writes node meta with a custom LastSeen (for stale tests).
func (mc *mockCluster) registerWithLastSeen(t time.Time) error {
	meta := mc.meta
	meta.LastSeen = t
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	key := nodeMetaPrefix + meta.ID + "/meta.json"
	mc.store.put(key, data)
	return nil
}

// peers lists all node metas from the mock store (mirrors Cluster.Peers logic).
func (mc *mockCluster) peers() ([]Node, error) {
	keys := mc.store.keys(nodeMetaPrefix)
	now := time.Now().UTC()
	var nodes []Node
	for _, key := range keys {
		data, ok := mc.store.get(key)
		if !ok {
			continue
		}
		var meta NodeMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		stale := now.Sub(meta.LastSeen) > staleThreshold
		nodes = append(nodes, Node{NodeMeta: meta, Stale: stale})
	}
	return nodes, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRegisterWritesMeta ensures Register stores id, mode, hostname, storage,
// and a recent last_seen.
func TestRegisterWritesMeta(t *testing.T) {
	mc := newMockCluster("home-server", "server", true)

	before := time.Now().UTC().Add(-time.Second)
	if err := mc.register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	key := nodeMetaPrefix + "home-server/meta.json"
	data, ok := mc.store.get(key)
	if !ok {
		t.Fatal("meta.json not found in store")
	}

	var meta NodeMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if meta.ID != "home-server" {
		t.Errorf("ID = %q, want %q", meta.ID, "home-server")
	}
	if meta.Mode != "server" {
		t.Errorf("Mode = %q, want %q", meta.Mode, "server")
	}
	if meta.Hostname != "test-host" {
		t.Errorf("Hostname = %q, want %q", meta.Hostname, "test-host")
	}
	if !meta.Storage {
		t.Error("Storage = false, want true")
	}
	if meta.LastSeen.Before(before) || meta.LastSeen.After(after) {
		t.Errorf("LastSeen %v not in [%v, %v]", meta.LastSeen, before, after)
	}
}

// TestRegisterRefreshesLastSeen calls register twice and confirms that
// the second call advances LastSeen.
func TestRegisterRefreshesLastSeen(t *testing.T) {
	mc := newMockCluster("node-a", "local", false)

	if err := mc.register(); err != nil {
		t.Fatalf("first register: %v", err)
	}

	key := nodeMetaPrefix + "node-a/meta.json"
	data1, _ := mc.store.get(key)
	var m1 NodeMeta
	json.Unmarshal(data1, &m1)
	first := m1.LastSeen

	// Small delay to ensure a distinct timestamp.
	time.Sleep(10 * time.Millisecond)

	if err := mc.register(); err != nil {
		t.Fatalf("second register: %v", err)
	}

	data2, _ := mc.store.get(key)
	var m2 NodeMeta
	json.Unmarshal(data2, &m2)
	second := m2.LastSeen

	if !second.After(first) {
		t.Errorf("second LastSeen (%v) not after first (%v)", second, first)
	}
}

// TestPeersReturnsAll verifies that Peers returns all registered nodes.
func TestPeersReturnsAll(t *testing.T) {
	// All nodes share the same mock store.
	store := newMockStore()

	nodes := []*mockCluster{
		{meta: NodeMeta{ID: "node-1", Mode: "server", Hostname: "h1"}, store: store},
		{meta: NodeMeta{ID: "node-2", Mode: "local", Hostname: "h2"}, store: store},
		{meta: NodeMeta{ID: "node-3", Mode: "server", Hostname: "h3", Storage: true}, store: store},
	}

	for _, n := range nodes {
		if err := n.register(); err != nil {
			t.Fatalf("register %s: %v", n.meta.ID, err)
		}
	}

	peers, err := nodes[0].peers()
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	if len(peers) != 3 {
		t.Errorf("got %d peers, want 3", len(peers))
	}

	// Build a set for quick lookup.
	seen := make(map[string]bool)
	for _, p := range peers {
		seen[p.ID] = true
	}
	for _, n := range nodes {
		if !seen[n.meta.ID] {
			t.Errorf("node %q missing from peers", n.meta.ID)
		}
	}
}

// TestPeersStaleFlag verifies that nodes with an old LastSeen are flagged stale.
func TestPeersStaleFlag(t *testing.T) {
	store := newMockStore()

	fresh := &mockCluster{meta: NodeMeta{ID: "fresh", Mode: "local", Hostname: "h1"}, store: store}
	staleNode := &mockCluster{meta: NodeMeta{ID: "stale", Mode: "local", Hostname: "h2"}, store: store}

	if err := fresh.register(); err != nil {
		t.Fatalf("fresh register: %v", err)
	}

	// Write stale node with LastSeen well beyond staleThreshold.
	oldTime := time.Now().UTC().Add(-(staleThreshold + 5*time.Minute))
	if err := staleNode.registerWithLastSeen(oldTime); err != nil {
		t.Fatalf("stale register: %v", err)
	}

	peers, err := fresh.peers()
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(peers))
	}

	peerMap := make(map[string]Node)
	for _, p := range peers {
		peerMap[p.ID] = p
	}

	if peerMap["fresh"].Stale {
		t.Error("fresh node incorrectly marked stale")
	}
	if !peerMap["stale"].Stale {
		t.Error("stale node not marked stale")
	}
}

// TestDisabledClusterNoError verifies that a disabled cluster (no S3) does not
// return errors from Register, Peers, or Health.
func TestDisabledClusterNoError(t *testing.T) {
	c := newDisabled()

	if c.Enabled() {
		t.Fatal("disabled cluster reports Enabled() = true")
	}

	ctx := context.Background()

	if err := c.Register(ctx); err != nil {
		t.Errorf("Register on disabled cluster: %v", err)
	}

	peers, err := c.Peers(ctx)
	if err != nil {
		t.Errorf("Peers on disabled cluster: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("Peers on disabled cluster returned %d peers, want 0", len(peers))
	}

	h := c.Health()
	if h["enabled"] != false {
		t.Errorf("Health[enabled] = %v, want false", h["enabled"])
	}
}

// TestNewDisabledWhenNotConfigured verifies New() returns a disabled cluster
// (not an error) when S3 credentials are absent.
func TestNewDisabledWhenNotConfigured(t *testing.T) {
	// Temporarily clear S3 env vars so the config appears unconfigured.
	restore := func(key, val string, had bool) func() {
		return func() {
			if had {
				os.Setenv(key, val)
			} else {
				os.Unsetenv(key)
			}
		}
	}

	oldAccess, hadAccess := os.LookupEnv("VULOS_S3_ACCESS_KEY")
	oldSecret, hadSecret := os.LookupEnv("VULOS_S3_SECRET_KEY")
	oldBucket, hadBucket := os.LookupEnv("VULOS_S3_BUCKET")

	defer restore("VULOS_S3_ACCESS_KEY", oldAccess, hadAccess)()
	defer restore("VULOS_S3_SECRET_KEY", oldSecret, hadSecret)()
	defer restore("VULOS_S3_BUCKET", oldBucket, hadBucket)()

	os.Unsetenv("VULOS_S3_ACCESS_KEY")
	os.Unsetenv("VULOS_S3_SECRET_KEY")
	os.Unsetenv("VULOS_S3_BUCKET")

	cfg := LoadS3Config()
	c, err := New(cfg, "passphrase")
	if err != nil {
		t.Fatalf("New with unconfigured S3 returned error: %v", err)
	}
	if c.Enabled() {
		t.Error("cluster should be disabled when S3 is not configured")
	}
}

// TestStartExitsWhenDisabled ensures Start() returns immediately for a
// disabled cluster (no blocking goroutine).
func TestStartExitsWhenDisabled(t *testing.T) {
	c := newDisabled()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Good — Start returned without blocking.
	case <-time.After(500 * time.Millisecond):
		t.Error("Start() did not return promptly for disabled cluster")
	}
}

// TestIntegrationRegisterPeers is an integration test that runs only when
// VULOS_S3_ACCESS_KEY, VULOS_S3_SECRET_KEY, and VULOS_S3_BUCKET are set.
// It exercises the full Register → Peers round-trip against a real S3/MinIO.
func TestIntegrationRegisterPeers(t *testing.T) {
	cfg := LoadS3Config()
	if !cfg.Configured() {
		t.Skip("VULOS_S3_* env vars not set — skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	passphrase := os.Getenv("VULOS_CLUSTER_PASSPHRASE")
	if passphrase == "" {
		passphrase = "test-passphrase"
	}

	c, err := New(cfg, passphrase)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !c.Enabled() {
		t.Fatal("cluster should be enabled with configured S3")
	}

	if err := c.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}

	peers, err := c.Peers(ctx)
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(peers) == 0 {
		t.Error("Peers returned empty — expected at least this node")
	}

	// Confirm this node appears in the peer list.
	found := false
	for _, p := range peers {
		if p.ID == c.meta.ID {
			found = true
			if p.Stale {
				t.Errorf("self node %q reported as stale immediately after Register", p.ID)
			}
			break
		}
	}
	if !found {
		t.Errorf("self node %q not found in Peers()", c.meta.ID)
	}

	h := c.Health()
	if h["enabled"] != true {
		t.Errorf("Health[enabled] = %v, want true", h["enabled"])
	}
}
