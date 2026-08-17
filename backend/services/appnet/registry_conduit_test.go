package appnet

// registry_conduit_test.go — verifies the "conduit" registry entry, re-enabled
// to track Continuwuity (continuwuity.org, the actively-maintained community
// continuation of Famedly's Conduit/conduwuit) after the previous _disabled
// placeholder was blocked on a verified upstream checksum (famedly/conduit's
// GitLab releases remain source-archives-only). See docs/COMMS.md
// "Matrix homeserver" and docs/decisions.md APPSTORE-07.
//
// Mirrors store_comms_registry_test.go's approach: load the real shipped
// registry.json via AppStore, exactly as backend/cmd/server/main.go does, so
// a shape regression here fails a normal `go test ./...` run instead of only
// surfacing at click-to-install time on a box.

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAppStore_ConduitEntry_Enabled verifies the conduit entry is no longer
// administratively disabled and carries the fields the static-download
// install path (registry.go staticInstall) and launcher expect: a
// download_url, a non-empty sha256 checksum, deps for the binary's runtime
// libraries (liburing, TLS roots), a command that points at the downloaded
// binary, and a listen port.
func TestAppStore_ConduitEntry_Enabled(t *testing.T) {
	store := commsAppStore(t)
	entry, ok := store.Registry().Apps["conduit"]
	if !ok {
		t.Fatal(`registry entry "conduit" not found via AppStore.Registry()`)
	}
	if entry.Disabled {
		t.Error("conduit: entry is still _disabled — expected it re-enabled with a verified checksum")
	}
	if !entry.Vetted {
		t.Error("conduit: expected Vetted=true")
	}
	if entry.Signature == "" {
		t.Error("conduit: missing signature")
	}

	version := entry.LatestVersion()
	if version == "" {
		t.Fatal("conduit: LatestVersion() returned empty")
	}
	recipe := entry.GetRecipe(version)
	if recipe == nil {
		t.Fatalf("conduit: GetRecipe(%q) returned nil", version)
	}
	if recipe.Disabled {
		t.Errorf("conduit: version %q is still _disabled", version)
	}
	// MIGRATED to the per-architecture vehicle (roadmap/INSTALL-METHODOLOGY.md).
	// This block used to assert a single top-level download_url + checksum,
	// which DOWNLOAD-01 now refuses — so those assertions were not merely
	// obsolete, they asserted the defect. The replacement is strictly stronger:
	// every architecture must carry its OWN https URL and its OWN bare sha256,
	// so the pair that used to be checked once is now checked per arch, and an
	// entry offering no architecture at all fails instead of passing vacuously.
	if recipe.DownloadURL != "" {
		t.Errorf("conduit: still carries a top-level download_url %q — the retired vehicle", recipe.DownloadURL)
	}
	if recipe.Checksum != "" {
		t.Errorf("conduit: still carries a top-level checksum %q — the retired vehicle", recipe.Checksum)
	}
	if !recipe.HasArtifacts() {
		t.Fatal("conduit: no artifacts — nothing declares what to install on any architecture")
	}
	for arch, a := range recipe.Artifacts {
		if !strings.HasPrefix(a.DownloadURL, "https://") {
			t.Errorf("conduit[%s]: download_url must be https, got %q", arch, a.DownloadURL)
		}
		if a.Checksum == "" {
			t.Errorf("conduit[%s]: missing sha256 — must be computed, never invented (SEC-H3/SECAUDIT2-H1)", arch)
		}
		if len(a.Checksum) != 64 {
			t.Errorf("conduit[%s]: checksum %q is not a bare 64-char sha256 hex digest", arch, a.Checksum)
		}
	}
	if recipe.Command == "" {
		t.Error("conduit: missing run command")
	}
	if recipe.Port == 0 {
		t.Error("conduit: expected a non-zero listen port")
	}
	found := false
	for _, d := range recipe.Deps {
		if d == "liburing2" {
			found = true
		}
	}
	if !found {
		t.Errorf("conduit: expected liburing2 in deps (continuwuity requires io_uring at runtime), got %v", recipe.Deps)
	}

	// Belt-and-braces: this recipe must actually pass the security gate an
	// install request runs through — a regression here would silently block
	// install at click-time despite the registry entry looking fine.
	if err := validateRecipeSecurity(recipe); err != nil {
		t.Errorf("conduit: recipe fails validateRecipeSecurity: %v", err)
	}
}

// TestAppStore_ConduitEntry_VerifiesAgainstShippedAnchor confirms the
// re-enabled conduit entry's signature still verifies against the shipped
// dev trust anchor (registry_acceptance_test.go covers this generically for
// all entries; this names conduit explicitly so a future edit that breaks
// its signature fails by name).
func TestAppStore_ConduitEntry_VerifiesAgainstShippedAnchor(t *testing.T) {
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
	entry, ok := store.Registry().Apps["conduit"]
	if !ok {
		t.Fatal(`registry entry "conduit" not found`)
	}
	if err := VerifyEntrySignature(entry, "conduit", key); err != nil {
		t.Errorf("conduit: signature verification failed: %v", err)
	}
}

// TestRegistryJSON_ConduitEntry validates conduit parses correctly and has
// the required fields, the same style as TestRegistryJSON_NavidromeEntry in
// registry_static_test.go — a direct LoadRegistry check independent of
// AppStore wiring.
func TestRegistryJSON_ConduitEntry(t *testing.T) {
	regPath := filepath.Join("..", "..", "..", "registry.json")
	reg, err := LoadRegistry(regPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	entry, ok := reg.Apps["conduit"]
	if !ok {
		t.Fatal("conduit not found in registry.json")
	}
	if entry.Disabled {
		t.Error("conduit: expected _disabled to be absent/false")
	}
	if entry.Type != "service" {
		t.Errorf("conduit: type = %q, want service", entry.Type)
	}
	if len(entry.Keywords) == 0 {
		t.Error("conduit: expected non-empty keywords")
	}
}
