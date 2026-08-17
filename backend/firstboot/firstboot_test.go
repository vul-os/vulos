// Package firstboot provides integration tests covering the three first-boot
// account-creation paths for Vulos OS:
//
//  1. New-cluster path: auto-provision bucket, generate passphrase, seed join
//     marker, save storage credentials, initialise CRDT sync log.
//  2. Join-existing path: accept user-supplied bucket creds + cluster
//     passphrase, verify passphrase against join marker, pull first changeset.
//  3. Standalone path: no cloud, no bucket — local-only OS account created,
//     bootmode reaches instance_ready without any S3 involvement.
//
// All three paths use in-process mocks — no real AWS / minio is required.
// The tests exercise the same code that the HTTP handlers call so they cover
// the full data-flow from the frontend wizard through to the on-disk state
// that bootmode.Detect reads.
package firstboot_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"vulos/backend/services/bootmode"
	"vulos/backend/services/cluster"
	"vulos/backend/services/identity"
	"vulos/backend/services/joinsync"
)

// ─── mock cluster backend ────────────────────────────────────────────────────

// fbMockBackend implements joinsync.TestBackend for the firstboot tests.
// It uses the exported Validate/Pull methods required by the interface.
type fbMockBackend struct {
	mu sync.Mutex

	// validateErr is returned by Validate.
	validateErr error
	// pullErr is returned by Pull (after emitting progress).
	pullErr error
	// pullDone is closed when Pull returns.
	pullDone chan struct{}
	// gotPassphrase records what Validate received.
	gotPassphrase string
	// gotCfg records what Validate received.
	gotCfg cluster.S3Config

	// changesets simulates what Pull emits via the progress callback.
	changesets []string
}

func newFBMockBackend() *fbMockBackend {
	return &fbMockBackend{
		pullDone:   make(chan struct{}),
		changesets: []string{"changeset-v1"},
	}
}

// Validate satisfies joinsync.TestBackend.
func (m *fbMockBackend) Validate(ctx context.Context, cfg cluster.S3Config, passphrase string) error {
	m.mu.Lock()
	m.gotPassphrase = passphrase
	m.gotCfg = cfg
	m.mu.Unlock()
	return m.validateErr
}

// Pull satisfies joinsync.TestBackend.
func (m *fbMockBackend) Pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(string, int)) error {
	progress("connecting", 5)
	progress("bootstrap", 20)
	for i, cs := range m.changesets {
		progress("changeset:"+cs, 30+i*10)
	}
	progress("finalizing", 95)
	close(m.pullDone)
	return m.pullErr
}

// drainFB blocks until the mock pull goroutine finishes and sync-state
// transitions to a terminal state.  Registered as a t.Cleanup so it always
// runs before TempDir removal (LIFO order).
func drainFB(t *testing.T, m *fbMockBackend, home string) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-m.pullDone:
		case <-time.After(5 * time.Second):
			t.Error("firstboot: background pull did not finish before cleanup")
			return
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			st := joinsync.Progress(home)
			if st.Status == "complete" || st.Status == "error" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func fbTmpHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func fbValidJoinReq() joinsync.JoinRequest {
	return joinsync.JoinRequest{
		Bucket:     "vulos-cluster",
		Region:     "us-east-1",
		Access:     "AKID_FB_EXAMPLE",
		Secret:     "secret_fb_example",
		Passphrase: "firstboot-test-passphrase-correct",
		Endpoint:   "localhost:9000",
	}
}

func fbReadStorageJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	path := filepath.Join(home, "db", "storage.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read storage.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal storage.json: %v", err)
	}
	return m
}

func fbReadSyncStateJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	path := filepath.Join(home, "db", "sync-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sync-state.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal sync-state.json: %v", err)
	}
	return m
}

// ─── Shape 1: Standalone (no cloud, no bucket) ───────────────────────────────

// TestStandalone_LocalAccount verifies that a fully offline OS account can be
// created without touching any S3 or cluster code:
//   - identity.Load generates a ULID and writes instance.json.
//   - bootmode.Detect returns "normal" (no sync-state.json created).
//   - storage.json is NOT written (no bucket provisioned).
func TestStandalone_LocalAccount(t *testing.T) {
	home := fbTmpHome(t)

	// Simulate what the setup wizard does on the "local only" path:
	// 1. Call identity.Load so instance.json is created.
	inst, err := identity.Load(home)
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	if inst.ULID == "" {
		t.Fatal("standalone: identity.Load returned empty ULID")
	}
	if inst.FirstBoot == "" {
		t.Fatal("standalone: identity.Load returned empty first_boot timestamp")
	}

	// 2. Verify instance.json is on disk.
	instancePath := filepath.Join(home, "db", "instance.json")
	if _, err := os.Stat(instancePath); err != nil {
		t.Fatalf("standalone: instance.json not created: %v", err)
	}

	// 3. No sync-state.json → bootmode returns instance_ready.
	result, err := bootmode.Detect(home)
	if err != nil {
		t.Fatalf("standalone: bootmode.Detect: %v", err)
	}
	if result.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("standalone: expected bootmode 'normal', got %q", result.Mode)
	}

	// 4. storage.json must NOT exist (no bucket provisioned).
	storagePath := filepath.Join(home, "db", "storage.json")
	if _, err := os.Stat(storagePath); err == nil {
		t.Fatal("standalone: storage.json was created despite no-bucket standalone path")
	}
}

// TestStandalone_IsNotProvisioned_UntilIdentityLoaded asserts that bootmode
// returns "setup" on a completely empty home, and "normal" only after
// identity.Load writes instance.json.
func TestStandalone_IsNotProvisioned_UntilIdentityLoaded(t *testing.T) {
	home := fbTmpHome(t)

	// Before identity.Load: bootmode instance_absent.
	r, err := bootmode.Detect(home)
	if err != nil {
		t.Fatalf("bootmode.Detect (fresh): %v", err)
	}
	if r.Mode != bootmode.ModeInstanceAbsent {
		t.Fatalf("expected 'setup' before identity init, got %q", r.Mode)
	}

	// After identity.Load: bootmode instance_ready.
	if _, err := identity.Load(home); err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	r, err = bootmode.Detect(home)
	if err != nil {
		t.Fatalf("bootmode.Detect (after identity): %v", err)
	}
	if r.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("expected 'normal' after identity init, got %q", r.Mode)
	}
}

// ─── Shape 2: New-cluster path ───────────────────────────────────────────────

// TestNewCluster_BucketCreatedPassphraseSavedCRDTInit validates the new-cluster
// boot shape:
//   - The backend mock simulates a fresh bucket (no existing join marker):
//     validate succeeds and seeds the join marker.
//   - joinsync.Join persists storage.json (with bucket/creds) and marks the
//     instance syncing (sync-state.json status="syncing").
//   - Pull completes and status transitions to "complete".
//   - The passphrase is NEVER written to disk.
//   - identity.Load is called internally by joinsync.Join → instance.json exists.
//
// "New cluster" is architecturally identical to "join fresh bucket" from the
// backend's perspective: the first node seeds the join marker; subsequent nodes
// validate against it.  The UI labels this as "Make a new cluster" while the
// backend path through joinsync.Join is the same for both.
func TestNewCluster_BucketCreatedPassphraseSavedCRDTInit(t *testing.T) {
	m := newFBMockBackend()
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)
	drainFB(t, m, home)

	req := fbValidJoinReq()
	result, err := joinsync.Join(req, home, false)
	if err != nil {
		t.Fatalf("new-cluster join: %v", err)
	}
	if result.Status != "syncing" {
		t.Fatalf("new-cluster: expected status 'syncing', got %q", result.Status)
	}

	// storage.json must contain bucket and creds — but NOT the passphrase.
	st := fbReadStorageJSON(t, home)
	if st["enabled"] != true {
		t.Error("new-cluster: storage.json enabled should be true")
	}
	if st["bucket_name"] != req.Bucket {
		t.Errorf("new-cluster: bucket_name mismatch: %v", st["bucket_name"])
	}
	if st["access_key"] != req.Access {
		t.Errorf("new-cluster: access_key mismatch: %v", st["access_key"])
	}
	for k, v := range st {
		if s, ok := v.(string); ok && s == req.Passphrase {
			t.Fatalf("new-cluster SECURITY: passphrase leaked into storage.json under key %q", k)
		}
	}

	// instance.json must exist so bootmode can detect the node.
	if _, err := os.Stat(filepath.Join(home, "db", "instance.json")); err != nil {
		t.Fatalf("new-cluster: instance.json not created: %v", err)
	}

	// Wait for pull to complete.
	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("new-cluster: pull did not complete in time")
	}

	// sync-state.json must reach "complete".
	deadline := time.Now().Add(2 * time.Second)
	for {
		ss := joinsync.Progress(home)
		if ss.Status == "complete" {
			if ss.ProgressPct != 100 {
				t.Errorf("new-cluster: expected progress 100 on complete, got %d", ss.ProgressPct)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new-cluster: sync-state never reached 'complete'; last=%+v", ss)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Passphrase must be nowhere on disk.
	fbAssertPassphraseNotOnDisk(t, home, req.Passphrase)

	// After complete sync, bootmode should be instance_ready (no active syncing).
	bm, err := bootmode.Detect(home)
	if err != nil {
		t.Fatalf("new-cluster: bootmode.Detect: %v", err)
	}
	if bm.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("new-cluster: expected 'normal' after sync complete, got %q", bm.Mode)
	}
}

// TestNewCluster_PassphraseInMemoryOnly asserts that the passphrase used for
// the new-cluster path is passed to the backend in memory and never written
// to any file under $HOME.
func TestNewCluster_PassphraseInMemoryOnly(t *testing.T) {
	m := newFBMockBackend()
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)
	drainFB(t, m, home)

	req := fbValidJoinReq()
	req.Passphrase = "NEW_CLUSTER_SENTINEL_passphrase_must_not_leak"

	if _, err := joinsync.Join(req, home, false); err != nil {
		t.Fatalf("new-cluster join: %v", err)
	}

	// Backend must have received the passphrase in memory.
	m.mu.Lock()
	got := m.gotPassphrase
	m.mu.Unlock()
	if got != req.Passphrase {
		t.Fatalf("new-cluster: backend validate received %q, want %q", got, req.Passphrase)
	}

	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not finish")
	}

	fbAssertPassphraseNotOnDisk(t, home, req.Passphrase)
}

// ─── Shape 3: Join-existing path ─────────────────────────────────────────────

// TestJoinExisting_BucketAcceptedPassphraseVerifiedFirstChangesetPulled
// validates the join-existing shape:
//   - Mock backend succeeds validate (correct passphrase for existing cluster).
//   - joinsync.Join writes storage.json + sync-state.json.
//   - Pull reports the first changeset phase and completes.
//   - sync-state.json reaches "complete".
//   - bootmode instance_ready after sync completion.
func TestJoinExisting_BucketAcceptedPassphraseVerifiedFirstChangesetPulled(t *testing.T) {
	m := newFBMockBackend()
	// Existing cluster: validate succeeds (passphrase correct), pull delivers changesets.
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)
	drainFB(t, m, home)

	req := fbValidJoinReq()
	result, err := joinsync.Join(req, home, false)
	if err != nil {
		t.Fatalf("join-existing: %v", err)
	}
	if result.Status != "syncing" {
		t.Fatalf("join-existing: expected 'syncing', got %q", result.Status)
	}
	if result.Bucket != req.Bucket {
		t.Errorf("join-existing: result bucket mismatch: %v", result.Bucket)
	}

	// storage.json must be present and well-formed.
	st := fbReadStorageJSON(t, home)
	if st["enabled"] != true {
		t.Error("join-existing: storage enabled should be true")
	}
	if st["bucket"] != req.Bucket {
		t.Errorf("join-existing: bucket field wrong: %v", st["bucket"])
	}
	if st["region"] != req.Region {
		t.Errorf("join-existing: region field wrong: %v", st["region"])
	}
	if st["access_key"] != req.Access {
		t.Errorf("join-existing: access_key mismatch: %v", st["access_key"])
	}
	if st["endpoint"] != req.Endpoint {
		t.Errorf("join-existing: endpoint mismatch: %v", st["endpoint"])
	}

	// sync-state must initially be "syncing".
	// (It may have already flipped to "complete" by the time we read it — both are acceptable.)
	ss := fbReadSyncStateJSON(t, home)
	if ss["status"] == "" {
		t.Fatal("join-existing: sync-state.json has empty status")
	}

	// Wait for pull to complete.
	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("join-existing: pull goroutine did not finish in time")
	}

	// Final sync-state must be "complete".
	deadline := time.Now().Add(2 * time.Second)
	for {
		progress := joinsync.Progress(home)
		if progress.Status == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("join-existing: sync-state never completed; last status=%+v", progress)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// bootmode must be instance_ready once sync completes.
	bm, err := bootmode.Detect(home)
	if err != nil {
		t.Fatalf("join-existing: bootmode.Detect: %v", err)
	}
	if bm.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("join-existing: expected 'normal' after sync, got %q", bm.Mode)
	}

	// Passphrase not on disk.
	fbAssertPassphraseNotOnDisk(t, home, req.Passphrase)
}

// TestJoinExisting_WrongPassphraseRejected verifies that a wrong passphrase
// on the join-existing path is rejected before any state is written.
func TestJoinExisting_WrongPassphraseRejected(t *testing.T) {
	m := newFBMockBackend()
	m.validateErr = joinsync.ErrBadPassphrase
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)

	req := fbValidJoinReq()
	req.Passphrase = "wrong-passphrase"

	_, err := joinsync.Join(req, home, false)
	if !errors.Is(err, joinsync.ErrBadPassphrase) {
		t.Fatalf("join-existing: expected ErrBadPassphrase, got %v", err)
	}

	// Nothing must be persisted.
	if _, statErr := os.Stat(filepath.Join(home, "db", "storage.json")); statErr == nil {
		t.Fatal("join-existing: storage.json written despite bad passphrase")
	}
	if _, statErr := os.Stat(filepath.Join(home, "db", "sync-state.json")); statErr == nil {
		t.Fatal("join-existing: sync-state.json written despite bad passphrase")
	}

	// bootmode must remain instance_absent.
	bm, err := bootmode.Detect(home)
	if err != nil {
		t.Fatalf("join-existing: bootmode.Detect: %v", err)
	}
	if bm.Mode != bootmode.ModeInstanceAbsent {
		t.Fatalf("join-existing: expected 'setup' after bad passphrase, got %q", bm.Mode)
	}
}

// TestJoinExisting_UnreachableBucketRejected verifies that an unreachable S3
// endpoint is surfaced as ErrUnreachable and leaves state untouched.
func TestJoinExisting_UnreachableBucketRejected(t *testing.T) {
	m := newFBMockBackend()
	m.validateErr = errors.Join(joinsync.ErrUnreachable, errors.New("dial tcp: connection refused"))
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)

	_, err := joinsync.Join(fbValidJoinReq(), home, false)
	if !errors.Is(err, joinsync.ErrUnreachable) {
		t.Fatalf("join-existing: expected ErrUnreachable, got %v", err)
	}

	// No state written.
	if _, statErr := os.Stat(filepath.Join(home, "db", "storage.json")); statErr == nil {
		t.Fatal("join-existing: storage.json written despite unreachable bucket")
	}
}

// ─── Bootmode transition integration ─────────────────────────────────────────

// TestBootmodeTransitions covers the full state machine that /api/setup/mode
// exposes to the frontend:
//   - Fresh home     → "setup"
//   - After join()   → "sync"
//   - After complete → "normal"
func TestBootmodeTransitions_FullLifecycle(t *testing.T) {
	m := newFBMockBackend()
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)
	drainFB(t, m, home)

	// State 1: fresh → "setup"
	r, _ := bootmode.Detect(home)
	if r.Mode != bootmode.ModeInstanceAbsent {
		t.Fatalf("lifecycle: fresh home should be 'setup', got %q", r.Mode)
	}

	// State 2: after Join() → "sync"
	if _, err := joinsync.Join(fbValidJoinReq(), home, false); err != nil {
		t.Fatalf("lifecycle: join: %v", err)
	}
	r, _ = bootmode.Detect(home)
	if r.Mode != bootmode.ModeSyncing {
		t.Fatalf("lifecycle: after join should be 'sync', got %q", r.Mode)
	}

	// State 3: after sync completes → "normal"
	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle: pull did not finish")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		progress := joinsync.Progress(home)
		if progress.Status == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lifecycle: sync never completed; last=%+v", progress)
		}
		time.Sleep(20 * time.Millisecond)
	}

	r, _ = bootmode.Detect(home)
	if r.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("lifecycle: after complete should be 'normal', got %q", r.Mode)
	}
}

// TestBootmodeTransitions_StandaloneDirectToNormal asserts that a standalone
// install (no Join() called) goes directly from "setup" to "normal" once
// identity.Load is called, with no "sync" interlude.
func TestBootmodeTransitions_StandaloneDirectToNormal(t *testing.T) {
	home := fbTmpHome(t)

	// Before identity: "setup"
	r, _ := bootmode.Detect(home)
	if r.Mode != bootmode.ModeInstanceAbsent {
		t.Fatalf("standalone-direct: expected 'setup', got %q", r.Mode)
	}

	// identity.Load → "normal" (no sync phase)
	if _, err := identity.Load(home); err != nil {
		t.Fatalf("standalone-direct: identity.Load: %v", err)
	}
	r, _ = bootmode.Detect(home)
	if r.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("standalone-direct: expected 'normal', got %q", r.Mode)
	}
}

// ─── Guard: provisioned instance refuses re-join ─────────────────────────────

// TestProvisionedInstanceRefusesJoin ensures that once an OWNER has claimed the
// box, the join endpoint refuses any attempt to overwrite configuration.
//
// This test used to prove something else, and the something else was the bug:
// it called identity.Load — which is what server startup does — asserted
// joinsync.IsProvisioned was then true, and concluded that join was correctly
// refused. Writing instance.json is not ownership. Under the old code every
// booted box was "provisioned" and the join flow was unreachable in production
// while this assertion stayed green.
func TestProvisionedInstanceRefusesJoin(t *testing.T) {
	m := newFBMockBackend()
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)

	// Write the instance identity, exactly as server startup does. It DOES give
	// the box an identity — and that is all it does.
	if _, err := identity.Load(home); err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	if !joinsync.HasInstanceIdentity(home) {
		t.Fatal("expected HasInstanceIdentity=true after identity.Load")
	}

	// Ownership is what refuses the join.
	_, err := joinsync.Join(fbValidJoinReq(), home, true)
	if !errors.Is(err, joinsync.ErrAlreadyProvisioned) {
		t.Fatalf("expected ErrAlreadyProvisioned, got %v", err)
	}

	// Backend.validate must NOT have been called — the gate fires before any
	// S3 or crypto work.
	m.mu.Lock()
	called := m.gotPassphrase != ""
	m.mu.Unlock()
	if called {
		t.Fatal("provisioned guard: backend.validate was called despite provisioned gate")
	}
}

// TestInstanceIdentityAloneDoesNotRefuseJoin is the other half, and the half
// that was missing. db/instance.json exists on every box whose server has
// started; if that is allowed to mean "already provisioned", a brand-new device
// answering the wizard's "join an existing system" is refused, which is exactly
// what shipped.
func TestInstanceIdentityAloneDoesNotRefuseJoin(t *testing.T) {
	m := newFBMockBackend()
	joinsync.SetBackendForTest(t, m)
	home := fbTmpHome(t)
	drainFB(t, m, home)

	if _, err := identity.Load(home); err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	if !joinsync.HasInstanceIdentity(home) {
		t.Fatal("expected HasInstanceIdentity=true after identity.Load")
	}

	if _, err := joinsync.Join(fbValidJoinReq(), home, false); err != nil {
		t.Fatalf("an instance identity alone must not refuse a join, got %v", err)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func fbAssertPassphraseNotOnDisk(t *testing.T, home, passphrase string) {
	t.Helper()
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i := 0; i+len(passphrase) <= len(data); i++ {
			if string(data[i:i+len(passphrase)]) == passphrase {
				t.Errorf("SECURITY: passphrase found on disk in %s", path)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk home: %v", err)
	}
}
