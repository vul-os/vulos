package multiinstance_test

// SYNC-APPS-01: the seam between the replicator and the box's real installer.
//
// This test lives here, next to the interface, rather than beside AppStore,
// because what it checks is that the two halves FIT. multiinstance declares
// Realiser in primitive types and services/appnet satisfies it structurally —
// neither package imports the other — which keeps the installer and the
// replicator independent but leaves nothing to fail at compile time in either
// package on its own. The assertion has to be somewhere that sees both.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/appnet"
)

// TestAppStoreSatisfiesRealiser is the compile-time half. Left as a var inside a
// test so the failure is attributed to this file rather than to whichever
// package happens to be built first.
func TestAppStoreSatisfiesRealiser(t *testing.T) {
	var r multiinstance.Realiser = appnet.NewAppStore(t.TempDir())
	if r == nil {
		t.Fatal("nil Realiser")
	}
}

// TestRealisedVersionsReadsTheDiskNotATable is the behavioural half, and it is
// the assumption most worth pinning: "installed" in this OS is a filesystem
// fact, and the reconciler compares desire against THAT, not against the
// app_registry rows describing it.
//
// If RealisedVersions ever came to read the table, a box whose row said
// "realised" while the directory was gone would never repair itself — it would
// be reconciling against a report of the disk instead of the disk. The audit
// this work comes from is entirely about a system believing its own bookkeeping.
func TestRealisedVersionsReadsTheDiskNotATable(t *testing.T) {
	appsDir := t.TempDir()
	t.Setenv("VULOS_BUNDLED_APPS", filepath.Join(appsDir, "no-such-bundled-dir"))
	writeRealiserManifest(t, appsDir, "notes", "3.1.4")

	store := appnet.NewAppStore(appsDir)
	got, err := store.RealisedVersions()
	if err != nil {
		t.Fatalf("RealisedVersions: %v", err)
	}
	if got["notes"] != "3.1.4" {
		t.Fatalf("RealisedVersions()[notes] = %q, want %q (from %s/notes/app.json)", got["notes"], "3.1.4", appsDir)
	}

	// Remove the directory behind its back — the case a table cannot see.
	if err := os.RemoveAll(filepath.Join(appsDir, "notes")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err = store.RealisedVersions()
	if err != nil {
		t.Fatalf("RealisedVersions after removal: %v", err)
	}
	if _, still := got["notes"]; still {
		t.Error("RealisedVersions still reports an app whose directory is gone — it is reading a record rather than the disk, " +
			"so a box that lost an app would never notice it had to reinstall it")
	}
}

// TestReconcileDrivesTheRealAppStore runs the loop against the real installer
// rather than a fake, for the one case that needs no network and no registry:
// the fleet no longer wants an app this box has, so the directory must go.
//
// The install direction cannot run here — it downloads from a signed registry
// over the network — and is proven against a scripted Realiser in
// appsync_desired_test.go instead. What this adds is that Reconcile's plan
// actually reaches AppStore rather than only the test double.
func TestReconcileDrivesTheRealAppStore(t *testing.T) {
	const ulidA = "01HWZMINST000000000000001A"
	appsDir := t.TempDir()
	t.Setenv("VULOS_BUNDLED_APPS", filepath.Join(appsDir, "no-such-bundled-dir"))
	writeRealiserManifest(t, appsDir, "notes", "3.1.4")
	store := appnet.NewAppStore(appsDir)

	_, as := openTempAppSync(t)
	if err := as.DesireInstall(ulidA, "notes", "3.1.4"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireRemove(ulidA, "notes"); err != nil {
		t.Fatalf("DesireRemove: %v", err)
	}

	res, err := as.Reconcile(context.Background(), ulidA, store)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "notes" {
		t.Fatalf("Reconcile removed %v (failed: %v), want [notes]", res.Removed, res.Failed)
	}
	if _, err := os.Stat(filepath.Join(appsDir, "notes")); !os.IsNotExist(err) {
		t.Errorf("%s/notes still exists after reconciling against its tombstone (stat err: %v)", appsDir, err)
	}

	rows, err := as.ListAppsForInstance(ulidA, true)
	if err != nil {
		t.Fatalf("ListAppsForInstance: %v", err)
	}
	if len(rows) != 1 || rows[0].RealiseState != multiinstance.RealiseRemoved {
		t.Errorf("realisation rows after removal = %+v, want one row in state %q", rows, multiinstance.RealiseRemoved)
	}
}

// TestRealiseOnlyInstallsWhatTheVettedRegistryLists pins the routing choice in
// AppStore.Realise, which is the most security-relevant line of the seam and had
// no guard until a mutation pointed that out.
//
// Realise is driven by REPLICATED intent: any box that can write the fleet
// desired set names an app that every other box then installs. AppStore has two
// install paths and only one of them is safe to put on the end of that wire:
//
//	InstallFromRegistry  downloads only a URL from an Ed25519-signed, vetted
//	                     registry entry, verifies the publisher signature before
//	                     touching the filesystem, and refuses disabled entries
//	                     and unsupported architectures.
//	Install              takes a DownloadURL straight from a request body, with
//	                     no registry signature and no mandatory checksum.
//
// Routing Realise to the second would turn "install this app everywhere" into
// "fetch and extract this URL on every box in the fleet". So an app the registry
// does not list must be refused BY THE REGISTRY, before anything is fetched.
func TestRealiseOnlyInstallsWhatTheVettedRegistryLists(t *testing.T) {
	appsDir := t.TempDir()
	t.Setenv("VULOS_BUNDLED_APPS", filepath.Join(appsDir, "no-such-bundled-dir"))
	t.Setenv("VULOS_REGISTRY", filepath.Join(appsDir, "no-such-registry.json"))
	store := appnet.NewAppStore(appsDir)

	err := store.Realise(context.Background(), "steam", "1.0.0")
	if err == nil {
		t.Fatal("Realise installed an app the registry does not list — replicated intent reached an unvetted install path")
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("Realise refused %q with %q, which does not come from the registry check. Realise must route through "+
			"InstallFromRegistry (signed, vetted, arch-checked), never through Install (arbitrary DownloadURL): otherwise a peer "+
			"that can write the fleet desired set can name a URL for every box to fetch and extract.", "steam", err)
	}
	// And nothing was written to disk on the refused path.
	if entries, rerr := os.ReadDir(appsDir); rerr == nil {
		for _, e := range entries {
			if e.Name() == "steam" {
				t.Error("the refused install still created an app directory — the refusal happens after the filesystem is touched")
			}
		}
	}
}

// writeRealiserManifest writes a minimal valid app.json under appsDir/id.
func writeRealiserManifest(t *testing.T, appsDir, id, version string) {
	t.Helper()
	dir := filepath.Join(appsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"id": id, "name": id, "description": id + " description",
		"version": version, "command": "bin/server", "port": 8080,
		"category": "productivity", "icon": "🧩",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
