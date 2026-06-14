package multiinstance_test

// TOPO-02 tests: DedicatedInstanceMigrator — adopt identity, sync CRDT,
// peer back into the mesh.
//
// All tests are in-process: no real S3, no real network.  Uses the in-memory
// mockBootstrapS3 defined below, which satisfies sync.BootstrapS3 (and
// therefore MigratorBootstrapS3), and the temp-dir Registry helper already
// present in registry_test.go (same external test package).
//
// Tests directly verify the three migration steps:
//   1. AdoptIdentity — new instance carries the correct public key in the Registry.
//   2. SyncCRDT      — CRDT state from the bucket is applied to the new instance's DB.
//   3. PeerBack      — new instance is StatusOnline in the Registry after migration.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	syncsvc "vulos/backend/services/sync"

	"vulos/backend/internal/multiinstance"
)

// ── in-memory BootstrapS3 mock ────────────────────────────────────────────────
//
// Satisfies sync.BootstrapS3 (= multiinstance.MigratorBootstrapS3).

type mockBootstrapS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMockBootstrapS3() *mockBootstrapS3 {
	return &mockBootstrapS3{objects: make(map[string][]byte)}
}

func (m *mockBootstrapS3) PutEncrypted(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.objects[key] = cp
	return nil
}

func (m *mockBootstrapS3) GetEncrypted(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *mockBootstrapS3) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// DeleteObject satisfies syncsvc.SnapshotS3 if needed (unused in migrate path
// but required if the bucket is used with the Compactor in the same test).
func (m *mockBootstrapS3) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// seedLatest writes a fake latest.json + snapshot blob into the mock bucket.
// This mirrors snapshot_test.go's seedSnapshot / seedChangeset helpers but
// lives here so the external test package is self-contained.
func seedLatest(t *testing.T, s3 *mockBootstrapS3, version int64, blob []byte) {
	t.Helper()
	ctx := context.Background()
	blobKey := fmt.Sprintf("cluster/snapshot/%d.db.enc", version)
	if err := s3.PutEncrypted(ctx, blobKey, blob); err != nil {
		t.Fatalf("seed snapshot blob: %v", err)
	}
	type latestDoc struct {
		Version int64  `json:"version"`
		Key     string `json:"key"`
	}
	doc, _ := json.Marshal(latestDoc{Version: version, Key: blobKey})
	if err := s3.PutEncrypted(ctx, "cluster/snapshot/latest.json", doc); err != nil {
		t.Fatalf("seed latest.json: %v", err)
	}
}

// seedChangeset writes a fake changeset object into the mock bucket.
func seedChangeset(t *testing.T, s3 *mockBootstrapS3, peerID string, version int64) {
	t.Helper()
	key := fmt.Sprintf("nodes/%s/changes/%d.bin", peerID, version)
	if err := s3.PutEncrypted(context.Background(), key, []byte("changeset-payload")); err != nil {
		t.Fatalf("seed changeset %s: %v", key, err)
	}
}

// genTestKey generates a fresh ed25519 key pair.
func genTestKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// Construct a VulaID in the expected format (simplified for tests).
	vulaID := "vula:ed25519:" + base64.RawURLEncoding.EncodeToString(pub)
	return priv, vulaID
}

// fakeLocalDB simulates the new instance's local database.
type fakeLocalDB struct {
	data     []byte
	version  int64
	cleared  bool
	clearErr error
}

func (d *fakeLocalDB) clear(_ context.Context) error {
	if d.clearErr != nil {
		return d.clearErr
	}
	d.data = nil
	d.version = 0
	d.cleared = true
	return nil
}

func (d *fakeLocalDB) install(_ context.Context, data []byte, version int64) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	d.data = cp
	d.version = version
	return nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestMigrator_AdoptsIdentity_SyncsCRDT_PeersBack is the primary TOPO-02 test.
// It verifies all three steps:
//  1. The new instance entry in the registry carries the correct public key.
//  2. CRDT state (snapshot + changeset tail) is synced to the new instance's DB.
//  3. The new instance is StatusOnline in the registry after migration.
func TestMigrator_AdoptsIdentity_SyncsCRDT_PeersBack(t *testing.T) {
	ctx := context.Background()
	reg := openTempRegistry(t)
	migrator := multiinstance.NewMigrator(reg)

	// ── Source identity (same key used by the shared-pool instance) ───────────
	priv, vulaID := genTestKey(t)
	pubKeyB64 := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))

	// ── Durable bucket state ──────────────────────────────────────────────────
	s3 := newMockBootstrapS3()
	durableBlob := []byte("crdt-merged-state-v15")
	seedLatest(t, s3, 15, durableBlob)
	seedChangeset(t, s3, "peer-X", 16)
	seedChangeset(t, s3, "peer-X", 17)

	// ── New instance local DB (starts empty on a fresh machine) ───────────────
	localDB := &fakeLocalDB{}
	var appliedTail []string

	result, err := migrator.Run(ctx, multiinstance.MigrationConfig{
		NewULID:         "01TOPO020000000000000001",
		DisplayName:     "Dedicated Instance",
		EndpointURL:     "https://dedicated.example.com:8080",
		PrivKey:         priv,
		VulaID:          vulaID,
		ClearLocalCache: localDB.clear,
		Install:         localDB.install,
		Applier: syncsvc.ChangesetApplier(func(_ context.Context, _ syncsvc.BootstrapS3, key string) error {
			appliedTail = append(appliedTail, key)
			return nil
		}),
	}, s3)
	if err != nil {
		t.Fatalf("migrator.Run: %v", err)
	}

	// ── Step 1: AdoptIdentity — registry entry carries the correct public key ─
	inst, ok := reg.Get("01TOPO020000000000000001")
	if !ok {
		t.Fatal("new instance not found in registry after migration")
	}
	if inst.Ed25519PublicKey != pubKeyB64 {
		t.Errorf("registry public key mismatch:\n got=%q\nwant=%q", inst.Ed25519PublicKey, pubKeyB64)
	}
	if inst.Role != multiinstance.RoleOwner {
		t.Errorf("registry role = %q, want %q", inst.Role, multiinstance.RoleOwner)
	}

	// ── Step 2: SyncCRDT — snapshot installed + tail replayed ────────────────
	if !localDB.cleared {
		t.Error("local DB was not cleared before CRDT sync")
	}
	if localDB.version != 15 {
		t.Errorf("local DB version = %d, want 15 (snapshot version)", localDB.version)
	}
	if string(localDB.data) != string(durableBlob) {
		t.Errorf("local DB data = %q, want %q", localDB.data, durableBlob)
	}
	if result.RehydrateResult.SnapshotVersion != 15 {
		t.Errorf("RehydrateResult.SnapshotVersion = %d, want 15", result.RehydrateResult.SnapshotVersion)
	}
	if result.RehydrateResult.ChangesetsTailCount != 2 {
		t.Errorf("RehydrateResult.ChangesetsTailCount = %d, want 2", result.RehydrateResult.ChangesetsTailCount)
	}
	if len(appliedTail) != 2 {
		t.Errorf("tail changeset applier called %d times, want 2: %v", len(appliedTail), appliedTail)
	}

	// ── Step 3: PeerBack — new instance is StatusOnline in the registry ────────
	if result.NewInstance.Status != multiinstance.StatusOnline {
		t.Errorf("new instance status = %q, want %q", result.NewInstance.Status, multiinstance.StatusOnline)
	}
	if inst2, _ := reg.Get("01TOPO020000000000000001"); inst2.Status != multiinstance.StatusOnline {
		t.Errorf("registry status after migration = %q, want %q", inst2.Status, multiinstance.StatusOnline)
	}
}

// TestMigrator_RetireOldPresence verifies that RetireOldPresence is called
// after migration and that OldPresenceRetired is true in the result.
func TestMigrator_RetireOldPresence(t *testing.T) {
	ctx := context.Background()
	reg := openTempRegistry(t)
	migrator := multiinstance.NewMigrator(reg)

	priv, vulaID := genTestKey(t)
	s3 := newMockBootstrapS3() // empty bucket → from_empty

	localDB := &fakeLocalDB{}
	var retireCalled bool

	result, err := migrator.Run(ctx, multiinstance.MigrationConfig{
		NewULID:         "01TOPO020000000000000002",
		PrivKey:         priv,
		VulaID:          vulaID,
		ClearLocalCache: localDB.clear,
		Install:         localDB.install,
		Applier:         syncsvc.ChangesetApplier(func(_ context.Context, _ syncsvc.BootstrapS3, _ string) error { return nil }),
		RetireOldPresence: func(_ context.Context) error {
			retireCalled = true
			return nil
		},
	}, s3)
	if err != nil {
		t.Fatalf("migrator.Run: %v", err)
	}

	if !retireCalled {
		t.Error("RetireOldPresence was not called")
	}
	if !result.OldPresenceRetired {
		t.Error("OldPresenceRetired must be true when RetireOldPresence succeeds")
	}
}

// TestMigrator_RetireOldPresence_NonFatal verifies that a RetireOldPresence
// failure does not abort the migration — the new instance is already live.
func TestMigrator_RetireOldPresence_NonFatal(t *testing.T) {
	ctx := context.Background()
	reg := openTempRegistry(t)
	migrator := multiinstance.NewMigrator(reg)

	priv, vulaID := genTestKey(t)
	s3 := newMockBootstrapS3()
	localDB := &fakeLocalDB{}

	retireErr := errors.New("network unreachable")
	result, err := migrator.Run(ctx, multiinstance.MigrationConfig{
		NewULID:         "01TOPO020000000000000003",
		PrivKey:         priv,
		VulaID:          vulaID,
		ClearLocalCache: localDB.clear,
		Install:         localDB.install,
		Applier:         syncsvc.ChangesetApplier(func(_ context.Context, _ syncsvc.BootstrapS3, _ string) error { return nil }),
		RetireOldPresence: func(_ context.Context) error {
			return retireErr
		},
	}, s3)
	// Migration itself must succeed even though retire failed.
	if err != nil {
		t.Fatalf("migrator.Run: %v (want nil — retire error is non-fatal)", err)
	}
	if result.OldPresenceRetired {
		t.Error("OldPresenceRetired must be false when RetireOldPresence errors")
	}
	// New instance is still online.
	inst, ok := reg.Get("01TOPO020000000000000003")
	if !ok || inst.Status != multiinstance.StatusOnline {
		t.Errorf("new instance status after failed retire = %q, want %q", inst.Status, multiinstance.StatusOnline)
	}
}

// TestMigrator_ValidationErrors verifies that missing required fields are
// rejected before any state is touched.
func TestMigrator_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	reg := openTempRegistry(t)
	migrator := multiinstance.NewMigrator(reg)

	priv, vulaID := genTestKey(t)
	s3 := newMockBootstrapS3()
	localDB := &fakeLocalDB{}
	noopApplier := syncsvc.ChangesetApplier(func(_ context.Context, _ syncsvc.BootstrapS3, _ string) error { return nil })

	base := multiinstance.MigrationConfig{
		NewULID:         "01TOPO020000000000000010",
		PrivKey:         priv,
		VulaID:          vulaID,
		ClearLocalCache: localDB.clear,
		Install:         localDB.install,
		Applier:         noopApplier,
	}

	cases := []struct {
		name  string
		mutFn func(c *multiinstance.MigrationConfig)
	}{
		{"empty NewULID", func(c *multiinstance.MigrationConfig) { c.NewULID = "" }},
		{"empty VulaID", func(c *multiinstance.MigrationConfig) { c.VulaID = "" }},
		{"nil PrivKey", func(c *multiinstance.MigrationConfig) { c.PrivKey = nil }},
		{"nil ClearLocalCache", func(c *multiinstance.MigrationConfig) { c.ClearLocalCache = nil }},
		{"nil Install", func(c *multiinstance.MigrationConfig) { c.Install = nil }},
		{"nil Applier", func(c *multiinstance.MigrationConfig) { c.Applier = nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutFn(&cfg)
			_, err := migrator.Run(ctx, cfg, s3)
			if err == nil {
				t.Errorf("expected validation error for %q, got nil", tc.name)
			}
		})
	}
}

// TestMigrator_LeaderlessNoBrainSplit verifies that two new instances can
// adopt the SAME identity and sync the SAME durable bucket simultaneously
// without conflict — leaderless CRDT, no split-brain.
func TestMigrator_LeaderlessNoBrainSplit(t *testing.T) {
	ctx := context.Background()

	priv, vulaID := genTestKey(t)
	pubKeyB64 := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))

	// Shared durable bucket.
	s3 := newMockBootstrapS3()
	durableBlob := []byte("shared-crdt-state-v20")
	seedLatest(t, s3, 20, durableBlob)

	// Two SEPARATE registries (two distinct instances/machines).
	reg1 := openTempRegistry(t)
	reg2 := openTempRegistry(t)
	mig1 := multiinstance.NewMigrator(reg1)
	mig2 := multiinstance.NewMigrator(reg2)

	local1 := &fakeLocalDB{}
	local2 := &fakeLocalDB{}

	base := func(ulid string, local *fakeLocalDB) multiinstance.MigrationConfig {
		return multiinstance.MigrationConfig{
			NewULID:         ulid,
			DisplayName:     "Instance " + ulid[len(ulid)-2:],
			PrivKey:         priv, // SAME identity
			VulaID:          vulaID,
			ClearLocalCache: local.clear,
			Install:         local.install,
			Applier:         syncsvc.ChangesetApplier(func(_ context.Context, _ syncsvc.BootstrapS3, _ string) error { return nil }),
		}
	}

	r1, err := mig1.Run(ctx, base("01TOPO020000000000000020", local1), s3)
	if err != nil {
		t.Fatalf("migrator1.Run: %v", err)
	}
	r2, err := mig2.Run(ctx, base("01TOPO020000000000000021", local2), s3)
	if err != nil {
		t.Fatalf("migrator2.Run: %v", err)
	}

	// Both instances must have converged to the same durable snapshot.
	if string(local1.data) != string(durableBlob) {
		t.Errorf("instance1 data = %q, want %q", local1.data, durableBlob)
	}
	if string(local2.data) != string(durableBlob) {
		t.Errorf("instance2 data = %q, want %q", local2.data, durableBlob)
	}
	if r1.RehydrateResult.SnapshotVersion != 20 || r2.RehydrateResult.SnapshotVersion != 20 {
		t.Errorf("snapshot version mismatch: r1=%d r2=%d, both want 20",
			r1.RehydrateResult.SnapshotVersion, r2.RehydrateResult.SnapshotVersion)
	}

	// Both carry the same identity (public key) — leaderless, no cutover needed.
	inst1, _ := reg1.Get("01TOPO020000000000000020")
	inst2, _ := reg2.Get("01TOPO020000000000000021")
	if inst1.Ed25519PublicKey != pubKeyB64 || inst2.Ed25519PublicKey != pubKeyB64 {
		t.Errorf("public key mismatch: inst1=%q inst2=%q, both want %q",
			inst1.Ed25519PublicKey, inst2.Ed25519PublicKey, pubKeyB64)
	}
	// Both online.
	if inst1.Status != multiinstance.StatusOnline || inst2.Status != multiinstance.StatusOnline {
		t.Errorf("status: inst1=%q inst2=%q, both want online", inst1.Status, inst2.Status)
	}
}
