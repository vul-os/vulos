//go:build e2e

// Package e2e provides a seeded end-to-end test harness for Vulos OSS.
//
// The harness seeds a fresh instance with a complete dataset and exercises
// the full bm-init → first-boot wizard → create account → join cluster →
// peer message → OTA stage → reboot lifecycle without any external
// dependencies (no real S3, no real network).
//
// Seed data:
//   - 2 OS accounts (alice/admin, bob/user)
//   - 1 cluster passphrase + storage config
//   - 1 bucket config (in-memory mock)
//   - 3 peer contacts (charlie, diana, eve)
//   - 2 messages (charlie→alice, alice→charlie)
//   - 1 OTA channel pin (release-2026 trust anchor key)
//   - 1 mock cloud broker pubkey
//
// Run with: go test -tags=e2e ./backend/firstboot/e2e/...
package e2e_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"vulos/backend/services/auth"
	"vulos/backend/services/bootmode"
	"vulos/backend/services/cluster"
	"vulos/backend/services/identity"
	"vulos/backend/services/joinsync"
	"vulos/backend/services/peering"
	"vulos/backend/services/signing"
)

// ─── Seed constants ───────────────────────────────────────────────────────────

const (
	seedClusterPassphrase = "e2e-cluster-pass-2026-vulos"
	seedBucket            = "vulos-e2e-cluster"
	seedRegion            = "us-east-1"
	seedAccess            = "AKID_E2E_EXAMPLE"
	seedSecret            = "secret_e2e_example"
	seedEndpoint          = "localhost:9000"
)

// seedPeerIDs are the three seeded peer contacts.
var seedPeerIDs = [3]string{
	"vula:ed25519:charlie0000000000000000000000000000000000000000000",
	"vula:ed25519:diana00000000000000000000000000000000000000000000",
	"vula:ed25519:eve000000000000000000000000000000000000000000000000",
}

// ─── Mock cluster backend ─────────────────────────────────────────────────────

// e2eMockBackend is a thread-safe in-memory cluster backend: no S3 dialing.
type e2eMockBackend struct {
	mu            sync.Mutex
	validateErr   error
	pullErr       error
	gotPassphrase string
	gotCfg        cluster.S3Config
	pullDone      chan struct{}
}

func newE2EMock() *e2eMockBackend {
	return &e2eMockBackend{pullDone: make(chan struct{})}
}

func (m *e2eMockBackend) Validate(_ context.Context, cfg cluster.S3Config, passphrase string) error {
	m.mu.Lock()
	m.gotPassphrase = passphrase
	m.gotCfg = cfg
	m.mu.Unlock()
	return m.validateErr
}

func (m *e2eMockBackend) Pull(_ context.Context, _ cluster.S3Config, _ string, progress func(string, int)) error {
	progress("connect", 5)
	progress("bootstrap", 20)
	progress("changeset:v1", 50)
	progress("changeset:v2", 75)
	progress("finalizing", 95)
	close(m.pullDone)
	return m.pullErr
}

// ─── Harness setup ────────────────────────────────────────────────────────────

// e2eHarness holds everything for a complete seeded test scenario.
type e2eHarness struct {
	home    string
	mock    *e2eMockBackend
	store   *auth.Store
	inbox   *peering.InboxStore
	contacts *peering.ContactStore

	// anchorPub is the mock OTA trust-anchor public key seeded into the harness.
	anchorPub ed25519.PublicKey
	// anchorPriv is the corresponding private key — used to sign mock manifests.
	anchorPriv ed25519.PrivateKey
	// brokerPub is the mock cloud broker public key seeded into the harness.
	brokerPub ed25519.PublicKey
}

// newE2EHarness creates a fully-seeded e2e harness:
//   - 2 OS accounts (alice admin, bob user)
//   - 1 cluster passphrase wired through mock backend
//   - 3 peer contacts (charlie approved, diana pending, eve blocked)
//   - 2 inbox messages (charlie→alice, alice→charlie)
//   - 1 OTA trust anchor + broker pubkey on disk
//
// The harness installs the mock backend for the lifetime of t.
func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()

	home := t.TempDir()
	mock := newE2EMock()
	joinsync.SetBackendForTest(t, mock)

	// Drain background pull goroutine before TempDir removal (LIFO cleanup).
	// Only blocks if the pull channel has not already been closed (i.e. if a
	// Join was attempted); for tests that skip join this returns immediately.
	t.Cleanup(func() {
		select {
		case <-mock.pullDone:
			// already done
		default:
			// Give a short window for an in-flight pull to finish; if the pull
			// was never started (no Join called) this select exits immediately.
			select {
			case <-mock.pullDone:
			case <-time.After(100 * time.Millisecond):
				// Not started — safe to ignore.
			}
		}
	})

	// --- Seed OS accounts ---
	dataDir := filepath.Join(home, ".vulos", "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	store, err := auth.NewStore(dataDir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	alice, err := store.Register("alice", "alicepass2026!", "Alice")
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	_ = alice

	_, err = store.Register("bob", "bobpass2026!", "Bob")
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}

	// --- Seed peer contacts ---
	contacts, err := peering.NewContactStore(home)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	// charlie: approved with all perms
	if err := contacts.Add(seedPeerIDs[0], "Charlie", "charlie.vulos.local:8080"); err != nil {
		t.Fatalf("contacts.Add charlie: %v", err)
	}
	if err := contacts.Approve(seedPeerIDs[0], peering.DefaultPerms()); err != nil {
		t.Fatalf("contacts.Approve charlie: %v", err)
	}

	// diana: pending
	if err := contacts.Add(seedPeerIDs[1], "Diana", "diana.vulos.local:8080"); err != nil {
		t.Fatalf("contacts.Add diana: %v", err)
	}

	// eve: blocked
	if err := contacts.Add(seedPeerIDs[2], "Eve", "eve.vulos.local:8080"); err != nil {
		t.Fatalf("contacts.Add eve: %v", err)
	}
	if err := contacts.Block(seedPeerIDs[2]); err != nil {
		t.Fatalf("contacts.Block eve: %v", err)
	}

	// --- Seed inbox messages ---
	inbox, err := peering.NewInboxStore(home)
	if err != nil {
		t.Fatalf("NewInboxStore: %v", err)
	}

	convID := peering.ConversationID(seedPeerIDs[0], alice.ID)
	msg1 := peering.StoredMessage{
		ID:        "msg-seed-001",
		ConvID:    convID,
		From:      seedPeerIDs[0],
		To:        alice.ID,
		Type:      "text",
		Body:      "Hey Alice, ready to cluster up?",
		Timestamp: time.Now().Add(-10 * time.Minute),
		Direction: peering.DirectionIn,
	}
	msg2 := peering.StoredMessage{
		ID:        "msg-seed-002",
		ConvID:    convID,
		From:      alice.ID,
		To:        seedPeerIDs[0],
		Type:      "text",
		Body:      "Sure Charlie, initiating join now.",
		Timestamp: time.Now().Add(-9 * time.Minute),
		Direction: peering.DirectionOut,
	}
	if err := inbox.StoreMessage(msg1); err != nil {
		t.Fatalf("StoreMessage msg1: %v", err)
	}
	if err := inbox.StoreMessage(msg2); err != nil {
		t.Fatalf("StoreMessage msg2: %v", err)
	}

	// --- Seed OTA trust anchor and broker pubkey ---
	anchorPub, anchorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate anchor key: %v", err)
	}
	brokerPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate broker key: %v", err)
	}

	// Write anchor key to the path osdist reads.
	anchorDir := filepath.Join(home, ".vulos", "keys")
	if err := os.MkdirAll(anchorDir, 0o700); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}

	anchorJSON, _ := json.Marshal(map[string]any{
		"channel":   "release-2026",
		"algorithm": "ed25519",
		"pub_key":   anchorPub,
		"pinned_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err := os.WriteFile(filepath.Join(anchorDir, "ota-anchor.json"), anchorJSON, 0o600); err != nil {
		t.Fatalf("write ota-anchor: %v", err)
	}

	brokerJSON, _ := json.Marshal(map[string]any{
		"broker":    "cloud.vulos.org",
		"algorithm": "ed25519",
		"pub_key":   brokerPub,
		"pinned_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err := os.WriteFile(filepath.Join(anchorDir, "broker-pubkey.json"), brokerJSON, 0o600); err != nil {
		t.Fatalf("write broker-pubkey: %v", err)
	}

	return &e2eHarness{
		home:       home,
		mock:       mock,
		store:      store,
		inbox:      inbox,
		contacts:   contacts,
		anchorPub:  anchorPub,
		anchorPriv: anchorPriv,
		brokerPub:  brokerPub,
	}
}

// ─── E2E tests ────────────────────────────────────────────────────────────────

// TestE2E_BmInit_FreshInstanceIsSetupMode verifies that a freshly imaged
// instance (no instance.json, no sync-state.json) boots into "setup" mode —
// the bare-metal init condition before the first-boot wizard is completed.
func TestE2E_BmInit_FreshInstanceIsSetupMode(t *testing.T) {
	h := newE2EHarness(t)

	r, err := bootmode.Detect(h.home)
	if err != nil {
		t.Fatalf("bootmode.Detect: %v", err)
	}
	if r.Mode != "setup" {
		t.Errorf("bm-init: expected 'setup', got %q", r.Mode)
	}
}

// TestE2E_FirstBootWizard_LocalAccount exercises the standalone path:
// first-boot wizard creates a local account without cluster join.
// Post-condition: bootmode "normal", instance.json written.
func TestE2E_FirstBootWizard_LocalAccount(t *testing.T) {
	h := newE2EHarness(t)

	// Simulate the wizard choosing "local only" — just call identity.Load.
	inst, err := identity.Load(h.home)
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	if inst.ULID == "" {
		t.Error("identity.Load returned empty ULID")
	}

	r, err := bootmode.Detect(h.home)
	if err != nil {
		t.Fatalf("bootmode.Detect: %v", err)
	}
	if r.Mode != "normal" {
		t.Errorf("after local-account setup: expected 'normal', got %q", r.Mode)
	}

	instPath := filepath.Join(h.home, ".vulos", "db", "instance.json")
	if _, err := os.Stat(instPath); err != nil {
		t.Fatalf("instance.json not created: %v", err)
	}
}

// TestE2E_CreateAccount_TwoUsersSeeded verifies that the seeded accounts
// (alice admin, bob user) are accessible and their roles are correct.
func TestE2E_CreateAccount_TwoUsersSeeded(t *testing.T) {
	h := newE2EHarness(t)

	// Alice should be able to log in.
	a, err := h.store.Login("alice", "alicepass2026!")
	if err != nil {
		t.Fatalf("alice login: %v", err)
	}
	if a.Username != "alice" {
		t.Errorf("alice username = %q", a.Username)
	}

	// Bob should also be able to log in.
	b, err := h.store.Login("bob", "bobpass2026!")
	if err != nil {
		t.Fatalf("bob login: %v", err)
	}
	if b.Username != "bob" {
		t.Errorf("bob username = %q", b.Username)
	}

	// Roles: alice = admin (first user), bob = user.
	pairs := h.store.ListUsersWithRoles()
	roles := make(map[string]string)
	for _, p := range pairs {
		roles[p.Username] = p.Role
	}
	if roles["alice"] != "admin" {
		t.Errorf("alice role = %q, want admin", roles["alice"])
	}
	if roles["bob"] != "user" {
		t.Errorf("bob role = %q, want user", roles["bob"])
	}
}

// TestE2E_JoinCluster_SeedPassphraseSetsNormal exercises the full join path:
// POST /api/setup/join equivalent, using the mock backend that accepts the
// seeded passphrase, pulls changesets, and completes.
// Post-condition: bootmode "normal", no passphrase on disk.
func TestE2E_JoinCluster_SeedPassphraseSetsNormal(t *testing.T) {
	h := newE2EHarness(t)

	req := joinsync.JoinRequest{
		Bucket:     seedBucket,
		Region:     seedRegion,
		Access:     seedAccess,
		Secret:     seedSecret,
		Passphrase: seedClusterPassphrase,
		Endpoint:   seedEndpoint,
	}

	result, err := joinsync.Join(req, h.home)
	if err != nil {
		t.Fatalf("joinsync.Join: %v", err)
	}
	if result.Status != "syncing" {
		t.Errorf("join: expected status 'syncing', got %q", result.Status)
	}
	if result.Bucket != seedBucket {
		t.Errorf("join: result bucket = %q, want %q", result.Bucket, seedBucket)
	}

	// Wait for the mock pull to complete.
	select {
	case <-h.mock.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("join: background pull did not complete")
	}

	// sync-state must reach "complete".
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := joinsync.Progress(h.home)
		if st.Status == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("join: sync-state never reached 'complete'; last=%+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// bootmode "normal".
	r, err := bootmode.Detect(h.home)
	if err != nil {
		t.Fatalf("bootmode.Detect after join: %v", err)
	}
	if r.Mode != "normal" {
		t.Errorf("after join complete: expected 'normal', got %q", r.Mode)
	}

	// Passphrase must not be on disk.
	if err := e2eAssertPassphraseNotOnDisk(t, h.home, seedClusterPassphrase); err != nil {
		t.Error(err)
	}

	// Verify backend received the correct passphrase in memory.
	h.mock.mu.Lock()
	got := h.mock.gotPassphrase
	h.mock.mu.Unlock()
	if got != seedClusterPassphrase {
		t.Errorf("backend received passphrase %q, want %q", got, seedClusterPassphrase)
	}
}

// TestE2E_JoinCluster_WrongPassphraseRejected verifies that a wrong passphrase
// is rejected before any state is persisted and bootmode stays "setup".
func TestE2E_JoinCluster_WrongPassphraseRejected(t *testing.T) {
	h := newE2EHarness(t)
	h.mock.validateErr = joinsync.ErrBadPassphrase

	req := joinsync.JoinRequest{
		Bucket:     seedBucket,
		Region:     seedRegion,
		Access:     seedAccess,
		Secret:     seedSecret,
		Passphrase: "wrong-passphrase",
		Endpoint:   seedEndpoint,
	}

	_, err := joinsync.Join(req, h.home)
	if !errors.Is(err, joinsync.ErrBadPassphrase) {
		t.Fatalf("expected ErrBadPassphrase, got %v", err)
	}

	r, _ := bootmode.Detect(h.home)
	if r.Mode != "setup" {
		t.Errorf("after bad passphrase: expected 'setup', got %q", r.Mode)
	}

	// No storage.json written.
	storagePath := filepath.Join(h.home, ".vulos", "db", "storage.json")
	if _, err := os.Stat(storagePath); err == nil {
		t.Error("storage.json written despite bad passphrase")
	}
}

// TestE2E_JoinCluster_AlreadyProvisionedBlocked verifies that a provisioned
// instance refuses join attempts (SECAUDIT2 L-2).
func TestE2E_JoinCluster_AlreadyProvisionedBlocked(t *testing.T) {
	h := newE2EHarness(t)

	// Provision the instance first.
	if _, err := identity.Load(h.home); err != nil {
		t.Fatalf("identity.Load: %v", err)
	}

	req := joinsync.JoinRequest{
		Bucket:     seedBucket,
		Access:     seedAccess,
		Secret:     seedSecret,
		Passphrase: seedClusterPassphrase,
	}

	_, err := joinsync.Join(req, h.home)
	if !errors.Is(err, joinsync.ErrAlreadyProvisioned) {
		t.Fatalf("expected ErrAlreadyProvisioned, got %v", err)
	}
}

// TestE2E_PeerContacts_ThreeSeeded verifies all three seeded contacts have
// the expected trust states: charlie approved, diana pending, eve blocked.
func TestE2E_PeerContacts_ThreeSeeded(t *testing.T) {
	h := newE2EHarness(t)

	list := h.contacts.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 contacts, got %d", len(list))
	}

	byID := make(map[string]*peering.Contact)
	for _, c := range list {
		byID[c.VulaID] = c
	}

	// charlie approved
	charlie := byID[seedPeerIDs[0]]
	if charlie == nil {
		t.Fatal("charlie not found in contacts")
	}
	if charlie.State != peering.StateApproved {
		t.Errorf("charlie state = %q, want approved", charlie.State)
	}

	// diana pending
	diana := byID[seedPeerIDs[1]]
	if diana == nil {
		t.Fatal("diana not found in contacts")
	}
	if diana.State != peering.StatePending {
		t.Errorf("diana state = %q, want pending", diana.State)
	}

	// eve blocked
	eve := byID[seedPeerIDs[2]]
	if eve == nil {
		t.Fatal("eve not found in contacts")
	}
	if eve.State != peering.StateBlocked {
		t.Errorf("eve state = %q, want blocked", eve.State)
	}
}

// TestE2E_PeerMessage_TwoSeedMessagesInInbox verifies the two seeded messages
// are retrievable from the inbox in the correct conversation.
func TestE2E_PeerMessage_TwoSeedMessagesInInbox(t *testing.T) {
	h := newE2EHarness(t)

	a, _ := h.store.Login("alice", "alicepass2026!")
	convID := peering.ConversationID(seedPeerIDs[0], a.ID)

	msgs, err := h.inbox.GetMessages(convID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Both msgs should be present in any order.
	found := make(map[string]bool)
	for _, m := range msgs {
		found[m.ID] = true
		switch m.ID {
		case "msg-seed-001":
			if m.Direction != peering.DirectionIn {
				t.Errorf("msg-001 direction = %q, want in", m.Direction)
			}
			if m.Body != "Hey Alice, ready to cluster up?" {
				t.Errorf("msg-001 body = %q", m.Body)
			}
		case "msg-seed-002":
			if m.Direction != peering.DirectionOut {
				t.Errorf("msg-002 direction = %q, want out", m.Direction)
			}
		}
	}
	if !found["msg-seed-001"] || !found["msg-seed-002"] {
		t.Error("one or more seed messages not found in inbox")
	}
}

// TestE2E_OTAStage_TrustAnchorSeeded verifies that the OTA anchor key was
// written to disk and can be used to verify a mock manifest signature — this
// represents the "OTA stage" step where the updater checks a manifest.
func TestE2E_OTAStage_TrustAnchorSeeded(t *testing.T) {
	h := newE2EHarness(t)

	// Verify the anchor key file exists.
	anchorPath := filepath.Join(h.home, ".vulos", "keys", "ota-anchor.json")
	if _, err := os.Stat(anchorPath); err != nil {
		t.Fatalf("ota-anchor.json not found: %v", err)
	}

	// Simulate what the OTA updater does: produce canonical bytes for a mock
	// manifest and verify a signature produced by the anchor private key.
	manifest := map[string]any{
		"version":   "v08",
		"channel":   "release-2026",
		"latest":    "v08",
		"root_hash": "abcdef1234567890",
		"issued_at": time.Now().UTC().Format(time.RFC3339),
	}

	canonical, err := signing.Canonical(manifest)
	if err != nil {
		t.Fatalf("signing.Canonical: %v", err)
	}

	// Sign with the anchor private key.
	sigBytes := signing.Sign(h.anchorPriv, canonical)

	// Verify against the anchor public key — should succeed.
	if !signing.Verify(h.anchorPub, canonical, sigBytes) {
		t.Error("OTA stage: signature verification failed for valid anchor key")
	}

	// Tamper the canonical bytes — verify should fail.
	tampered := append([]byte(nil), canonical...)
	tampered[0] ^= 0xFF
	if signing.Verify(h.anchorPub, tampered, sigBytes) {
		t.Error("OTA stage: signature verified for tampered manifest — should have failed")
	}

	// Wrong key — verify should fail.
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if signing.Verify(wrongPub, canonical, sigBytes) {
		t.Error("OTA stage: signature verified for wrong key — should have failed")
	}
}

// TestE2E_OTAStage_BrokerPubkeySeeded verifies that the cloud broker pubkey
// is on disk and parseable.
func TestE2E_OTAStage_BrokerPubkeySeeded(t *testing.T) {
	h := newE2EHarness(t)

	brokerPath := filepath.Join(h.home, ".vulos", "keys", "broker-pubkey.json")
	data, err := os.ReadFile(brokerPath)
	if err != nil {
		t.Fatalf("broker-pubkey.json: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("broker-pubkey.json parse: %v", err)
	}

	if parsed["broker"] != "cloud.vulos.org" {
		t.Errorf("broker = %v, want cloud.vulos.org", parsed["broker"])
	}
	if parsed["algorithm"] != "ed25519" {
		t.Errorf("algorithm = %v, want ed25519", parsed["algorithm"])
	}
}

// TestE2E_Reboot_BootmodeTransitionsAfterJoin exercises the full lifecycle:
//
//	setup → sync → normal
//
// This mirrors the sequence a real device experiences: bm-init → wizard
// join → sync completes → normal boot.
func TestE2E_Reboot_BootmodeTransitionsAfterJoin(t *testing.T) {
	h := newE2EHarness(t)

	// State 1: fresh (no instance.json) → setup.
	r, _ := bootmode.Detect(h.home)
	if r.Mode != "setup" {
		t.Fatalf("initial bootmode = %q, want setup", r.Mode)
	}

	// State 2: join → sync.
	req := joinsync.JoinRequest{
		Bucket:     seedBucket,
		Region:     seedRegion,
		Access:     seedAccess,
		Secret:     seedSecret,
		Passphrase: seedClusterPassphrase,
		Endpoint:   seedEndpoint,
	}
	if _, err := joinsync.Join(req, h.home); err != nil {
		t.Fatalf("join: %v", err)
	}

	r, _ = bootmode.Detect(h.home)
	if r.Mode != "sync" {
		t.Fatalf("after join bootmode = %q, want sync", r.Mode)
	}

	// State 3: wait for pull → normal.
	select {
	case <-h.mock.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not complete")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := joinsync.Progress(h.home)
		if st.Status == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync never completed; last=%+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	r, _ = bootmode.Detect(h.home)
	if r.Mode != "normal" {
		t.Errorf("after sync complete bootmode = %q, want normal", r.Mode)
	}
}

// TestE2E_ClusterHealth_DisabledWhenNotConfigured verifies that the cluster
// package returns a disabled (non-error) state when no S3 env vars are set,
// representing a freshly installed node that has not yet been configured.
func TestE2E_ClusterHealth_DisabledWhenNotConfigured(t *testing.T) {
	// Clear S3 env vars to simulate unconfigured state.
	for _, key := range []string{"VULOS_S3_ACCESS_KEY", "VULOS_S3_SECRET_KEY", "VULOS_S3_BUCKET"} {
		old, had := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if had {
				os.Setenv(key, old)
			} else {
				os.Unsetenv(key)
			}
		})
	}

	cfg := cluster.LoadS3Config()
	c, err := cluster.New(cfg, seedClusterPassphrase)
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	if c.Enabled() {
		t.Error("cluster should be disabled when S3 env vars are absent")
	}

	health := c.Health()
	if health["enabled"] != false {
		t.Errorf("health[enabled] = %v, want false", health["enabled"])
	}
}

// TestE2E_SeedPassphrase_NeverOnDisk is a security invariant test: after a
// successful join, the cluster passphrase must not appear in any file under
// the home directory.
func TestE2E_SeedPassphrase_NeverOnDisk(t *testing.T) {
	h := newE2EHarness(t)

	req := joinsync.JoinRequest{
		Bucket:     seedBucket,
		Region:     seedRegion,
		Access:     seedAccess,
		Secret:     seedSecret,
		Passphrase: seedClusterPassphrase,
		Endpoint:   seedEndpoint,
	}
	if _, err := joinsync.Join(req, h.home); err != nil {
		t.Fatalf("join: %v", err)
	}

	select {
	case <-h.mock.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pull goroutine did not finish")
	}

	if err := e2eAssertPassphraseNotOnDisk(t, h.home, seedClusterPassphrase); err != nil {
		t.Error(err)
	}
}

// TestE2E_IdentityPersistsAcrossReload verifies that identity.Load returns
// the same ULID on a second call (idempotent, no re-generation).
func TestE2E_IdentityPersistsAcrossReload(t *testing.T) {
	h := newE2EHarness(t)

	inst1, err := identity.Load(h.home)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	inst2, err := identity.Load(h.home)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if inst1.ULID != inst2.ULID {
		t.Errorf("ULID changed between loads: %q vs %q", inst1.ULID, inst2.ULID)
	}
	if inst1.FirstBoot != inst2.FirstBoot {
		t.Errorf("FirstBoot changed between loads: %q vs %q", inst1.FirstBoot, inst2.FirstBoot)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// e2eAssertPassphraseNotOnDisk walks every file under home and fails if the
// passphrase appears as a byte substring in any file.
func e2eAssertPassphraseNotOnDisk(t *testing.T, home, passphrase string) error {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i := 0; i+len(passphrase) <= len(data); i++ {
			if string(data[i:i+len(passphrase)]) == passphrase {
				found = append(found, path)
				return nil
			}
		}
		return nil
	})
	if len(found) > 0 {
		return errors.New("SECURITY: passphrase found on disk in: " + found[0])
	}
	return nil
}
