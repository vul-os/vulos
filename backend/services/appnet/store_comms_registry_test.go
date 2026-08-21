package appnet

// store_comms_registry_test.go — verifies the app-store backend loads the
// third-party comms entries (Element, Jitsi Meet, Element Call) that replaced
// Vulos Talk/Meet as the App Hub's chat/video answer (see docs/APPS.md
// "Comms" chapter). Uses AppStore.NewAppStore against the real shipped
// registry.json, exactly as backend/cmd/server/main.go does — not a
// synthetic registry — so a shape regression in these entries (or a JSON
// field renamed out from under them) fails a normal `go test ./...` run
// instead of surfacing only at click-to-install time on a box.
//
// registry_acceptance_test.go already asserts every shipped entry's Ed25519
// signature verifies against the release cert; this file checks the fields
// the App Hub UI and installer actually read for these three entries.

import (
	"path/filepath"
	"strings"
	"testing"
)

// commsAppStore loads the app store the same way main.go does, pointed at
// the repo's real registry.json (via VULOS_REGISTRY) so we exercise the same
// JSON this repo ships rather than a hand-built fixture.
func commsAppStore(t *testing.T) *AppStore {
	t.Helper()
	regPath, err := filepath.Abs(shippedRegistryPath(t))
	if err != nil {
		t.Fatalf("resolve %s: %v", shippedRegistryPath(t), err)
	}
	t.Setenv("VULOS_REGISTRY", regPath)
	appsDir := t.TempDir()
	store := NewAppStore(appsDir)
	if store.Registry() == nil || len(store.Registry().Apps) == 0 {
		t.Fatal("NewAppStore did not load any registry entries — check VULOS_REGISTRY resolution")
	}
	return store
}

// TestAppStore_CommsEntries_Element verifies the Element registry entry
// (Matrix chat/voice/video client, Flatpak-packaged) loads with the shape
// the Flatpak install path (backend/services/appnet/flatpak.go) expects:
// a flatpak_id, no download_url/checksum (Flatpak verifies its own
// artifacts), and the av permissions a calling client needs.
func TestAppStore_CommsEntries_Element(t *testing.T) {
	store := commsAppStore(t)
	entry, ok := store.Registry().Apps["element"]
	if !ok {
		t.Fatal(`registry entry "element" not found via AppStore.Registry()`)
	}
	if !entry.Vetted {
		t.Error("element: expected Vetted=true")
	}
	if entry.Signature == "" {
		t.Error("element: missing signature")
	}
	recipe := entry.GetRecipe(entry.LatestVersion())
	if recipe == nil {
		t.Fatal("element: GetRecipe(LatestVersion()) returned nil")
	}
	if recipe.FlatpakID != "im.riot.Riot" {
		t.Errorf("element: FlatpakID = %q, want im.riot.Riot", recipe.FlatpakID)
	}
	if recipe.DownloadURL != "" {
		t.Errorf("element: expected empty DownloadURL for a flatpak recipe, got %q", recipe.DownloadURL)
	}
	for _, perm := range []string{"network", "camera", "microphone"} {
		if !containsStr(recipe.Permissions, perm) {
			t.Errorf("element: missing expected permission %q (got %v)", perm, recipe.Permissions)
		}
	}
}

// TestAppStore_CommsEntries_JitsiMeet mirrors the Element check for the
// Jitsi Meet Flatpak entry.
func TestAppStore_CommsEntries_JitsiMeet(t *testing.T) {
	store := commsAppStore(t)
	entry, ok := store.Registry().Apps["jitsi-meet"]
	if !ok {
		t.Fatal(`registry entry "jitsi-meet" not found via AppStore.Registry()`)
	}
	if !entry.Vetted {
		t.Error("jitsi-meet: expected Vetted=true")
	}
	if entry.Signature == "" {
		t.Error("jitsi-meet: missing signature")
	}
	recipe := entry.GetRecipe(entry.LatestVersion())
	if recipe == nil {
		t.Fatal("jitsi-meet: GetRecipe(LatestVersion()) returned nil")
	}
	if recipe.FlatpakID != "org.jitsi.jitsi-meet" {
		t.Errorf("jitsi-meet: FlatpakID = %q, want org.jitsi.jitsi-meet", recipe.FlatpakID)
	}
	for _, perm := range []string{"network", "camera", "microphone"} {
		if !containsStr(recipe.Permissions, perm) {
			t.Errorf("jitsi-meet: missing expected permission %q (got %v)", perm, recipe.Permissions)
		}
	}
}

// TestAppStore_CommsEntries_ElementCall verifies the Element Call entry — a
// static web bundle (not a Flatpak) with a pinned download and checksum, so
// it goes through the static-install / checksum-required path
// (requiresChecksum in registry.go) rather than Flatpak.
func TestAppStore_CommsEntries_ElementCall(t *testing.T) {
	store := commsAppStore(t)
	entry, ok := store.Registry().Apps["element-call"]
	if !ok {
		t.Fatal(`registry entry "element-call" not found via AppStore.Registry()`)
	}
	if !entry.Vetted {
		t.Error("element-call: expected Vetted=true")
	}
	if entry.Signature == "" {
		t.Error("element-call: missing signature")
	}
	if entry.Type != "web" {
		t.Errorf("element-call: Type = %q, want web", entry.Type)
	}
	version := entry.LatestVersion()
	recipe := entry.GetRecipe(version)
	if recipe == nil {
		t.Fatalf("element-call: GetRecipe(%q) returned nil", version)
	}
	if recipe.FlatpakID != "" {
		t.Errorf("element-call: expected no FlatpakID (static web bundle), got %q", recipe.FlatpakID)
	}
	// MIGRATED to the per-architecture vehicle (roadmap/INSTALL-METHODOLOGY.md).
	// The comment this replaces described "its install script downloads a
	// binary directly" — that script is exactly what INSTALL-01 now refuses,
	// so the sentence documented a vehicle the installer will not run. SEC-H3's
	// requirement is unchanged and now lands per architecture instead of once.
	if recipe.DownloadURL != "" {
		t.Errorf("element-call: still carries a top-level download_url %q — the retired vehicle", recipe.DownloadURL)
	}
	if recipe.Checksum != "" {
		t.Errorf("element-call: still carries a top-level checksum %q — the retired vehicle", recipe.Checksum)
	}
	if !recipe.HasArtifacts() {
		t.Fatal("element-call: no artifacts — nothing declares what to install on any architecture")
	}
	for arch, a := range recipe.Artifacts {
		if !strings.HasPrefix(a.DownloadURL, "https://") {
			t.Errorf("element-call[%s]: download_url must be https, got %q", arch, a.DownloadURL)
		}
		if len(a.Checksum) != 64 {
			t.Errorf("element-call[%s]: checksum %q is not a bare 64-char sha256 (SEC-H3)", arch, a.Checksum)
		}
	}
	if recipe.Port == 0 {
		t.Error("element-call: expected a non-zero listen port for a static web bundle")
	}
	if recipe.Command == "" {
		t.Error("element-call: missing run command")
	}
	// SECAUDIT2-H1 / SEC-H3 gate: an install script that downloads a binary
	// directly via curl/wget must carry a checksum. Belt-and-braces check
	// that this specific entry would actually pass validateRecipeSecurity —
	// a regression here would silently block install at click-time.
	if err := validateRecipeSecurity(recipe); err != nil {
		t.Errorf("element-call: recipe fails validateRecipeSecurity: %v", err)
	}
}

// TestAppStore_CommsEntries_AllVerifyAgainstShippedAnchor is a focused
// cross-check (registry_acceptance_test.go covers all 55 entries generically)
// naming the three comms entries explicitly, so a future registry refactor
// that silently drops or renames one of them fails here by name instead of
// only via a package-wide count.
func TestAppStore_CommsEntries_AllVerifyAgainstShippedAnchor(t *testing.T) {
	store := commsAppStore(t)
	anchor, cert := shippedTrust(t)
	stageBox(t, "dev", anchor, &cert)
	key, err := TrustedKey()
	if err != nil {
		t.Fatalf("could not resolve trust chain: %v", err)
	}
	if key == nil {
		t.Fatal("TrustedKey returned nil with no error")
	}
	for _, appID := range []string{"element", "jitsi-meet", "element-call"} {
		entry, ok := store.Registry().Apps[appID]
		if !ok {
			t.Errorf("registry entry %q not found", appID)
			continue
		}
		if err := VerifyEntrySignature(entry, appID, key); err != nil {
			t.Errorf("%s: signature verification failed: %v", appID, err)
		}
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
