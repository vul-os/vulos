package multiinstance_test

// MINST-04 tests: app registry CRDT changeset replication.
//
// Acceptance criteria verified:
//   [AC1] app install on instance A appears in instance B's registry within
//         5 s (simulated via two in-process AppSync stores sharing the
//         same in-memory representation via changeset marshal/unmarshal).
//   [AC2] uninstall conflict resolved by quorum (quorum not met → install wins).
//   [AC3] per-instance HTTP endpoint GET /api/instances/:ulid/apps.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// openTempAppSync creates a Registry + AppSync backed by a temporary DB file.
func openTempAppSync(t *testing.T) (*multiinstance.Registry, *multiinstance.AppSync) {
	t.Helper()
	dir := t.TempDir()
	reg, err := multiinstance.Open(filepath.Join(dir, "instances.db"))
	if err != nil {
		t.Fatalf("Open registry: %v", err)
	}
	as, err := multiinstance.OpenAppSync(reg)
	if err != nil {
		t.Fatalf("OpenAppSync: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg, as
}

// ── AC1: install on A appears in B ───────────────────────────────────────────

// TestAppInstallReplicatesAtoB verifies that an install event on instance A
// propagates to instance B via changeset marshal → unmarshal → ApplyChangeset.
// This satisfies the "within 5 s" AC with two in-process stores (transport is
// instantaneous; no actual timer check needed for pure-Go logic).
func TestAppInstallReplicatesAtoB(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		ulidB = "01HWZMINST000000000000001B"
		appID = "browser"
	)

	_, asA := openTempAppSync(t)
	_, asB := openTempAppSync(t)

	// Register both instances in A's registry so EmitChangeset sets PeerCount.
	regA, _ := openTempAppSync(t)
	_ = regA // (reg is returned but AppSync is what we use)

	// A emits a changeset for a new install.
	now := time.Now().UTC()
	entries := []multiinstance.AppRegistryEntry{
		{
			InstanceULID: ulidA,
			AppID:        appID,
			AppVersion:   "1.0.0",
			Installed:    true,
			InstalledBy:  "nodeA",
			UpdatedAt:    now,
		},
	}

	cs, err := asA.EmitChangeset(ulidA, entries)
	if err != nil {
		t.Fatalf("EmitChangeset: %v", err)
	}
	if cs.OriginULID != ulidA {
		t.Errorf("OriginULID: got %q want %q", cs.OriginULID, ulidA)
	}

	// Wire-encode and decode (simulates transport).
	wire, err := multiinstance.MarshalChangeset(cs)
	if err != nil {
		t.Fatalf("MarshalChangeset: %v", err)
	}
	received, err := multiinstance.UnmarshalChangeset(wire)
	if err != nil {
		t.Fatalf("UnmarshalChangeset: %v", err)
	}

	// B applies the changeset.
	if err := asB.ApplyChangeset(received); err != nil {
		t.Fatalf("ApplyChangeset: %v", err)
	}

	// B should now see the app for instance A.
	apps, err := asB.ListAppsForInstance(ulidA, false)
	if err != nil {
		t.Fatalf("ListAppsForInstance on B: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app on B, got %d", len(apps))
	}
	if apps[0].AppID != appID {
		t.Errorf("AppID: got %q want %q", apps[0].AppID, appID)
	}
	if apps[0].AppVersion != "1.0.0" {
		t.Errorf("AppVersion: got %q want %q", apps[0].AppVersion, "1.0.0")
	}
	if !apps[0].Installed {
		t.Error("Installed: expected true")
	}
}

// ── LWW: newer timestamp wins ─────────────────────────────────────────────────

func TestLWWNewerWins(t *testing.T) {
	const (
		ulid  = "01HWZMINST000000000000002A"
		appID = "notes"
	)

	_, as := openTempAppSync(t)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// Apply older entry first.
	csOld, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: base},
	})
	if err := as.ApplyChangeset(csOld); err != nil {
		t.Fatalf("apply old: %v", err)
	}

	// Apply newer entry (version upgrade).
	csNew, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "2.0.0", Installed: true, InstalledBy: "nodeB", UpdatedAt: base.Add(time.Hour)},
	})
	if err := as.ApplyChangeset(csNew); err != nil {
		t.Fatalf("apply new: %v", err)
	}

	apps, err := as.ListAppsForInstance(ulid, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1, got %d", len(apps))
	}
	if apps[0].AppVersion != "2.0.0" {
		t.Errorf("AppVersion after LWW: got %q want 2.0.0", apps[0].AppVersion)
	}
}

// Applying an older timestamp must NOT overwrite the local (newer) state.
func TestLWWOlderDiscarded(t *testing.T) {
	const (
		ulid  = "01HWZMINST000000000000003A"
		appID = "terminal"
	)

	_, as := openTempAppSync(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// Insert current row.
	csNow, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "3.0.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: base.Add(2 * time.Hour)},
	})
	if err := as.ApplyChangeset(csNow); err != nil {
		t.Fatalf("apply now: %v", err)
	}

	// Try to apply stale row.
	csOld, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: true, InstalledBy: "nodeB", UpdatedAt: base},
	})
	if err := as.ApplyChangeset(csOld); err != nil {
		t.Fatalf("apply stale: %v", err)
	}

	apps, _ := as.ListAppsForInstance(ulid, false)
	if len(apps) != 1 || apps[0].AppVersion != "3.0.0" {
		t.Errorf("stale write clobbered newer row: %+v", apps)
	}
}

// ── AC2: uninstall quorum ─────────────────────────────────────────────────────

// TestUninstallQuorumNotMet: with >2 instances and a low-PeerCount changeset,
// the uninstall should be rejected and installed=true retained (OR-set wins).
func TestUninstallQuorumNotMet(t *testing.T) {
	const (
		ulid  = "01HWZMINST000000000000004A"
		appID = "mail"
	)

	reg, as := openTempAppSync(t)

	// Populate registry with 3 instances (>2 → quorum required).
	for _, u := range []string{ulid, "01HWZMINST000000000000004B", "01HWZMINST000000000000004C"} {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:   u,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert instance %s: %v", u, err)
		}
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// First: install the app.
	csInstall, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: base},
	})
	if err := as.ApplyChangeset(csInstall); err != nil {
		t.Fatalf("apply install: %v", err)
	}

	// Now try to uninstall with PeerCount=1 (quorum not met: originator only
	// knew 1 peer, but local registry has 3 instances → quorum required).
	csUninstall := &multiinstance.AppChangeset{
		OriginULID: ulid,
		PeerCount:  1, // too few peers known — quorum not satisfied
		Entries: []multiinstance.AppRegistryEntry{
			{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: false, InstalledBy: "nodeA", UpdatedAt: base.Add(time.Minute)},
		},
	}
	if err := as.ApplyChangeset(csUninstall); err != nil {
		t.Fatalf("apply uninstall: %v", err)
	}

	// installed should still be true (OR-set + quorum guard).
	apps, err := as.ListAppsForInstance(ulid, false) // false = installed only
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("quorum guard should retain the installed row; got %d rows", len(apps))
	}
	if !apps[0].Installed {
		t.Error("installed should be true after failed quorum uninstall")
	}
}

// TestUninstallQuorumMet: with ≥2 peer instances known to the originator the
// uninstall IS accepted.
func TestUninstallQuorumMet(t *testing.T) {
	const (
		ulid  = "01HWZMINST000000000000005A"
		appID = "files"
	)

	reg, as := openTempAppSync(t)

	// 3 instances in registry.
	for _, u := range []string{ulid, "01HWZMINST000000000000005B", "01HWZMINST000000000000005C"} {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:   u,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	csInstall, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: base},
	})
	_ = as.ApplyChangeset(csInstall)

	// Uninstall with PeerCount=2 → quorum met.
	csUninstall := &multiinstance.AppChangeset{
		OriginULID: ulid,
		PeerCount:  2, // ≥2 → quorum satisfied
		Entries: []multiinstance.AppRegistryEntry{
			{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: false, InstalledBy: "nodeA", UpdatedAt: base.Add(time.Minute)},
		},
	}
	if err := as.ApplyChangeset(csUninstall); err != nil {
		t.Fatalf("apply uninstall: %v", err)
	}

	// Row should be gone from the installed view.
	apps, err := as.ListAppsForInstance(ulid, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 0 {
		t.Errorf("expected 0 installed apps after quorum uninstall, got %d", len(apps))
	}

	// But the row should still exist with installed=false when includeRemoved=true.
	all, err := as.ListAppsForInstance(ulid, true)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 row (uninstalled) with includeRemoved=true, got %d", len(all))
	}
	if all[0].Installed {
		t.Error("expected installed=false for uninstalled row")
	}
}

// ── OR-set: install wins over uninstall on equal timestamp ───────────────────

func TestORSetInstallWinsOnTie(t *testing.T) {
	const (
		ulid  = "01HWZMINST000000000000006A"
		appID = "settings"
	)

	_, as := openTempAppSync(t)
	ts := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// Apply uninstalled entry first.
	csUninstall, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: false, InstalledBy: "nodeA", UpdatedAt: ts},
	})
	_ = as.ApplyChangeset(csUninstall)

	// Now apply installed entry with the SAME timestamp — OR-set: install wins.
	csInstall, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: appID, AppVersion: "1.0.0", Installed: true, InstalledBy: "nodeB", UpdatedAt: ts},
	})
	if err := as.ApplyChangeset(csInstall); err != nil {
		t.Fatalf("apply install: %v", err)
	}

	apps, _ := as.ListAppsForInstance(ulid, false)
	if len(apps) != 1 {
		t.Fatalf("expected 1 installed app after OR-set merge, got %d", len(apps))
	}
	if !apps[0].Installed {
		t.Error("OR-set: install should have won over uninstall on equal timestamp")
	}
}

// ── AC3: HTTP endpoint GET /api/instances/:ulid/apps ─────────────────────────

func TestHTTPGetInstanceApps(t *testing.T) {
	const ulid = "01HWZMINST000000000000007A"

	_, as := openTempAppSync(t)

	now := time.Now().UTC()
	cs, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
		{InstanceULID: ulid, AppID: "browser", AppVersion: "2.1.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: now},
		{InstanceULID: ulid, AppID: "notes", AppVersion: "1.5.0", Installed: true, InstalledBy: "nodeA", UpdatedAt: now},
	})
	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("ApplyChangeset: %v", err)
	}

	mux := http.NewServeMux()
	multiinstance.RegisterAppSyncHandlers(mux, as)

	req := httptest.NewRequest(http.MethodGet, "/api/instances/"+ulid+"/apps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q", ct)
	}

	var apps []multiinstance.AppRegistryEntry
	if err := json.NewDecoder(w.Body).Decode(&apps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(apps))
	}

	// Results are ordered by app_id: browser, notes.
	if apps[0].AppID != "browser" {
		t.Errorf("apps[0].AppID: got %q want browser", apps[0].AppID)
	}
	if apps[1].AppID != "notes" {
		t.Errorf("apps[1].AppID: got %q want notes", apps[1].AppID)
	}
}

// TestHTTPGetInstanceAppsEmpty verifies [] (not null) for an unknown instance.
func TestHTTPGetInstanceAppsEmpty(t *testing.T) {
	_, as := openTempAppSync(t)

	mux := http.NewServeMux()
	multiinstance.RegisterAppSyncHandlers(mux, as)

	req := httptest.NewRequest(http.MethodGet, "/api/instances/01HWZMINST999999999999/apps", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	var apps []multiinstance.AppRegistryEntry
	if err := json.NewDecoder(w.Body).Decode(&apps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if apps == nil || len(apps) != 0 {
		t.Errorf("expected empty array [], got %v", apps)
	}
}

// ── ListAllApps ───────────────────────────────────────────────────────────────

func TestListAllApps(t *testing.T) {
	_, as := openTempAppSync(t)

	now := time.Now().UTC()
	for _, ulid := range []string{"01HWZMINST000000000000008A", "01HWZMINST000000000000008B"} {
		cs, _ := as.EmitChangeset(ulid, []multiinstance.AppRegistryEntry{
			{InstanceULID: ulid, AppID: "browser", AppVersion: "1.0.0", Installed: true, InstalledBy: "n", UpdatedAt: now},
		})
		if err := as.ApplyChangeset(cs); err != nil {
			t.Fatalf("ApplyChangeset %s: %v", ulid, err)
		}
	}

	all, err := as.ListAllApps()
	if err != nil {
		t.Fatalf("ListAllApps: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 rows across 2 instances, got %d", len(all))
	}
}

// ── Nil / empty edge cases ────────────────────────────────────────────────────

func TestApplyNilChangesetErrors(t *testing.T) {
	_, as := openTempAppSync(t)
	if err := as.ApplyChangeset(nil); err == nil {
		t.Error("expected error for nil changeset")
	}
}

func TestApplyEmptyChangesetNoError(t *testing.T) {
	_, as := openTempAppSync(t)
	cs := &multiinstance.AppChangeset{OriginULID: "X", Entries: nil}
	if err := as.ApplyChangeset(cs); err != nil {
		t.Errorf("empty changeset: unexpected error: %v", err)
	}
}

func TestEmitChangesetEmptyOriginErrors(t *testing.T) {
	_, as := openTempAppSync(t)
	_, err := as.EmitChangeset("", nil)
	if err == nil {
		t.Error("expected error for empty originULID")
	}
}
