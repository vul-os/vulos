package joinsync

// joinsync_l2_test.go — SECAUDIT2 L-2 regression tests.
//
// Verifies that Join is refused when the CALLER says this box already belongs
// to somebody, and proceeds when it does not.
//
// The gate used to be computed inside Join from bootmode ("normal": instance.json
// exists, nothing syncing). That made these tests pass while the shipped feature
// was dead: the server writes instance.json at startup, so "provisioned" was true
// on every running box and no device could ever join. provisionedHome() below is
// exactly the state a PRISTINE box is in one second after boot — which is why it
// must NOT, on its own, refuse a join.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// provisionedHome returns a tmpHome with an instance identity and no active
// sync — bootmode instance_ready. NOTE the name is historical: this is the state
// of EVERY running box, pristine ones included.
func provisionedHome(t *testing.T) string {
	t.Helper()
	home := tmpHome(t)
	dbDir := filepath.Join(home, "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	// Minimal instance.json — bootmode.Detect only checks for file existence.
	instancePath := filepath.Join(dbDir, "instance.json")
	if err := os.WriteFile(instancePath, []byte(`{"id":"test-instance"}`), 0o600); err != nil {
		t.Fatalf("write instance.json: %v", err)
	}
	// No sync-state.json (or "complete") → bootmode returns instance_ready.
	return home
}

// freshHome returns a tmpHome with no db dir at all → bootmode instance_absent.
func freshHome(t *testing.T) string {
	t.Helper()
	return tmpHome(t)
}

// syncingHome returns a tmpHome that is in "sync" mode: instance.json present
// AND sync-state.json with status "syncing" → bootmode syncing.
func syncingHome(t *testing.T) string {
	t.Helper()
	home := tmpHome(t)
	dbDir := filepath.Join(home, "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "instance.json"), []byte(`{"id":"test-instance"}`), 0o600); err != nil {
		t.Fatalf("write instance.json: %v", err)
	}
	syncJSON := []byte(`{"status":"syncing","phase":"downloading","progress_pct":40}`)
	if err := os.WriteFile(filepath.Join(dbDir, "sync-state.json"), syncJSON, 0o600); err != nil {
		t.Fatalf("write sync-state.json: %v", err)
	}
	return home
}

// --- L-2: provisioned state must refuse ---

// TestJoin_RefusedWhenOwnerClaimed asserts that calling Join on a box whose
// owner has claimed it returns ErrAlreadyProvisioned immediately, before any S3
// or crypto work is performed. This is the primary SECAUDIT2 L-2 regression.
func TestJoin_RefusedWhenOwnerClaimed(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := provisionedHome(t)

	_, err := Join(validReq(), home, true)
	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Fatalf("expected ErrAlreadyProvisioned for a provisioned instance, got: %v", err)
	}

	// The backend validate must NOT have been called — no S3/crypto work.
	m.mu.Lock()
	gotPassphrase := m.gotPassphrase
	m.mu.Unlock()
	if gotPassphrase != "" {
		t.Fatal("SECAUDIT2 L-2: backend.validate was called despite instance being provisioned — " +
			"the gate must fire before any S3 work")
	}
}

// TestJoin_RefusedWhenOwnerClaimed_NothingPersisted asserts that a refused join
// writes nothing new to disk.
func TestJoin_RefusedWhenOwnerClaimed_NothingPersisted(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := provisionedHome(t)

	_, _ = Join(validReq(), home, true)

	dbDir := filepath.Join(home, "db")
	// storage.json must not have been created (only instance.json exists).
	if _, err := os.Stat(filepath.Join(dbDir, storageFileName)); err == nil {
		t.Fatal("storage.json was written despite provisioned gate — should not touch disk")
	}
	// sync-state.json must not have been created.
	if _, err := os.Stat(filepath.Join(dbDir, syncStateFileName)); err == nil {
		t.Fatal("sync-state.json was written despite provisioned gate — should not touch disk")
	}
}

// --- L-2: first-boot (fresh) state must proceed ---

// TestJoin_ProceedsOnFreshInstance asserts that a fresh instance (no db dir,
// bootmode instance_absent) is not blocked.
func TestJoin_ProceedsOnFreshInstance(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := freshHome(t)
	drainPull(t, m, home)

	res, err := Join(validReq(), home, false)
	if err != nil {
		t.Fatalf("Join on fresh instance should succeed, got: %v", err)
	}
	if res == nil || res.Status != "syncing" {
		t.Fatalf("expected syncing result, got: %+v", res)
	}
}

// TestJoin_ProceedsOnSyncingInstance asserts that a mid-sync instance
// (bootmode syncing) is not blocked — re-join during sync must still work.
func TestJoin_ProceedsOnSyncingInstance(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := syncingHome(t)
	drainPull(t, m, home)

	res, err := Join(validReq(), home, false)
	if err != nil {
		t.Fatalf("Join on syncing instance should succeed, got: %v", err)
	}
	if res == nil || res.Status != "syncing" {
		t.Fatalf("expected syncing result, got: %+v", res)
	}
}

// --- the box-state helper, and what it is NOT ---

// TestHasInstanceIdentity_States pins the helper's contract, and pins the
// separation that its predecessor lacked: an instance identity on disk is not
// ownership, so a box carrying one must still be joinable.
func TestHasInstanceIdentity_States(t *testing.T) {
	t.Run("fresh instance has no identity", func(t *testing.T) {
		if HasInstanceIdentity(freshHome(t)) {
			t.Fatal("fresh home (no db dir) must not report an instance identity")
		}
	})

	t.Run("syncing instance has an identity", func(t *testing.T) {
		if !HasInstanceIdentity(syncingHome(t)) {
			t.Fatal("a syncing box has already written instance.json")
		}
	})

	t.Run("ready instance has an identity", func(t *testing.T) {
		if !HasInstanceIdentity(provisionedHome(t)) {
			t.Fatal("instance.json present, no active sync → identity present")
		}
	})

	// THE REGRESSION. This is the state a pristine box is in the moment its
	// server starts; if having an identity is allowed to mean "owned", the
	// entire join flow becomes unreachable on real hardware, which is what
	// shipped. Join must be driven by the caller's ownership answer alone.
	t.Run("an identity alone does not refuse a join", func(t *testing.T) {
		m := newMockBackend()
		withMock(t, m)
		home := provisionedHome(t)
		drainPull(t, m, home)

		res, err := Join(validReq(), home, false)
		if err != nil {
			t.Fatalf("a box with an instance identity but no owner must accept a join, got: %v", err)
		}
		if res == nil || res.Status != "syncing" {
			t.Fatalf("expected syncing result, got: %+v", res)
		}
	})
}
