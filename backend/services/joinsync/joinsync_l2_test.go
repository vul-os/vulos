package joinsync

// joinsync_l2_test.go — SECAUDIT2 L-2 regression tests.
//
// Verifies that Join is refused when the instance is already provisioned
// ("normal" bootmode: instance.json exists and sync-state.json is absent or
// not "syncing"), and that fresh / sync-mode instances continue to proceed.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// provisionedHome returns a tmpHome that looks like a fully provisioned
// instance: ~/.vulos/db/instance.json present, no sync-state.json (or
// sync-state with status "complete") → bootmode "normal".
func provisionedHome(t *testing.T) string {
	t.Helper()
	home := tmpHome(t)
	dbDir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	// Minimal instance.json — bootmode.Detect only checks for file existence.
	instancePath := filepath.Join(dbDir, "instance.json")
	if err := os.WriteFile(instancePath, []byte(`{"id":"test-instance"}`), 0o600); err != nil {
		t.Fatalf("write instance.json: %v", err)
	}
	// No sync-state.json (or "complete") → bootmode returns "normal".
	return home
}

// freshHome returns a tmpHome with no db dir at all → bootmode "setup".
func freshHome(t *testing.T) string {
	t.Helper()
	return tmpHome(t)
}

// syncingHome returns a tmpHome that is in "sync" mode: instance.json present
// AND sync-state.json with status "syncing" → bootmode "sync".
func syncingHome(t *testing.T) string {
	t.Helper()
	home := tmpHome(t)
	dbDir := filepath.Join(home, ".vulos", "db")
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

// TestJoin_RefusedWhenProvisioned asserts that calling Join on a provisioned
// instance returns ErrAlreadyProvisioned immediately, before any S3 or crypto
// work is performed. This is the primary SECAUDIT2 L-2 regression.
func TestJoin_RefusedWhenProvisioned(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := provisionedHome(t)

	_, err := Join(validReq(), home)
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

// TestJoin_RefusedWhenProvisioned_NothingPersisted asserts that a refused join
// on a provisioned instance writes nothing new to disk.
func TestJoin_RefusedWhenProvisioned_NothingPersisted(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := provisionedHome(t)

	_, _ = Join(validReq(), home)

	dbDir := filepath.Join(home, ".vulos", "db")
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
// bootmode "setup") is not blocked by the provisioned gate.
func TestJoin_ProceedsOnFreshInstance(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := freshHome(t)
	drainPull(t, m, home)

	res, err := Join(validReq(), home)
	if err != nil {
		t.Fatalf("Join on fresh instance should succeed, got: %v", err)
	}
	if res == nil || res.Status != "syncing" {
		t.Fatalf("expected syncing result, got: %+v", res)
	}
}

// TestJoin_ProceedsOnSyncingInstance asserts that a mid-sync instance
// (bootmode "sync") is not blocked — re-join during sync must still work.
func TestJoin_ProceedsOnSyncingInstance(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := syncingHome(t)
	drainPull(t, m, home)

	res, err := Join(validReq(), home)
	if err != nil {
		t.Fatalf("Join on syncing instance should succeed, got: %v", err)
	}
	if res == nil || res.Status != "syncing" {
		t.Fatalf("expected syncing result, got: %+v", res)
	}
}

// --- IsProvisioned helper ---

// TestIsProvisioned_States covers all three bootmode states to make the helper
// contract explicit and catch regressions in the bootmode.Detect integration.
func TestIsProvisioned_States(t *testing.T) {
	t.Run("fresh instance is not provisioned", func(t *testing.T) {
		if IsProvisioned(freshHome(t)) {
			t.Fatal("fresh home (no db dir) must not be considered provisioned")
		}
	})

	t.Run("syncing instance is not provisioned", func(t *testing.T) {
		if IsProvisioned(syncingHome(t)) {
			t.Fatal("syncing home must not be considered provisioned")
		}
	})

	t.Run("normal instance is provisioned", func(t *testing.T) {
		if !IsProvisioned(provisionedHome(t)) {
			t.Fatal("provisioned home (instance.json, no active sync) must be considered provisioned")
		}
	})
}
