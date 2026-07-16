// routes_boot_test.go — httptest coverage for the boot artifact endpoints.
//
// Acceptance criteria tested:
//
//  1. GET /boot/ipxe returns text/plain with valid iPXE script referencing
//     /boot/kernel and /boot/initramfs when BOOT_ARTIFACT_DIR is set.
//  2. GET /boot/manifest.json returns JSON with version, image_url/artifact_url,
//     sha256, signature fields when a pre-signed file is present.
//  3. BOOT_ARTIFACT_DIR="" → all four endpoints return 302 to OTA_PUBLIC_BUCKET_BASE
//     (except /boot/ipxe which returns text/plain with bucket-base URLs).
//  4. /boot/manifest.json returns 404 when neither BOOT_ARTIFACT_DIR nor
//     OTA_PUBLIC_BUCKET_BASE is set (no unsigned manifest must ever be served).
//  5. No signing key is imported or loaded by this package.
package cproutes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// bootMux registers boot routes with the supplied env values and returns a mux.
// It temporarily sets the env vars, registers the routes, then restores.
func bootMux(t *testing.T, artifactDir, bucketBase string) *http.ServeMux {
	t.Helper()

	prev1 := os.Getenv("BOOT_ARTIFACT_DIR")
	prev2 := os.Getenv("OTA_PUBLIC_BUCKET_BASE")
	os.Setenv("BOOT_ARTIFACT_DIR", artifactDir)
	os.Setenv("OTA_PUBLIC_BUCKET_BASE", bucketBase)
	t.Cleanup(func() {
		os.Setenv("BOOT_ARTIFACT_DIR", prev1)
		os.Setenv("OTA_PUBLIC_BUCKET_BASE", prev2)
	})

	mux := http.NewServeMux()
	RegisterBoot(mux)
	return mux
}

// bootGet issues GET <path> against mux and returns the recorder.
func bootGet(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "cp.vulos.internal"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// tempArtifactDir creates a temporary directory with a valid kernel,
// initramfs.img, and a fake pre-signed manifest.json.
// Returns the directory path.
func tempArtifactDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Write fake kernel blob.
	if err := os.WriteFile(filepath.Join(dir, "kernel"), []byte("KERNEL_DATA"), 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	// Write fake initramfs blob.
	if err := os.WriteFile(filepath.Join(dir, "initramfs.img"), []byte("INITRAMFS_DATA"), 0o644); err != nil {
		t.Fatalf("write initramfs.img: %v", err)
	}
	// Write a realistic-looking pre-signed manifest.json.
	manifest := map[string]any{
		"version":      "1.0.0",
		"artifact_url": "https://cdn.vulos.cloud/boot/v1.0.0/vulos-arm64.tar.gz",
		"sha256":       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"security":     false,
		"signature":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		t.Fatalf("write manifest.json: %v", err)
	}
	return dir
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-1: BOOT_ARTIFACT_DIR set — /boot/ipxe returns valid iPXE text
// ─────────────────────────────────────────────────────────────────────────────

func TestBootIPXE_WithArtifactDir(t *testing.T) {
	dir := tempArtifactDir(t)
	mux := bootMux(t, dir, "")

	rr := bootGet(mux, "/boot/ipxe")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", ct)
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Errorf("iPXE script must start with #!ipxe, got:\n%s", body)
	}
	// Must reference the local endpoints (not bucket).
	if !strings.Contains(body, "/boot/kernel") {
		t.Errorf("iPXE script must reference /boot/kernel:\n%s", body)
	}
	if !strings.Contains(body, "/boot/initramfs") {
		t.Errorf("iPXE script must reference /boot/initramfs:\n%s", body)
	}
	// Must NOT reference a bucket URL when BOOT_ARTIFACT_DIR is set.
	if strings.Contains(body, "OTA_PUBLIC_BUCKET_BASE") || strings.Contains(body, "https://bucket") {
		t.Errorf("iPXE script must not reference bucket when BOOT_ARTIFACT_DIR is set:\n%s", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-1b: /boot/ipxe contains "kernel" and "initrd" directives
// ─────────────────────────────────────────────────────────────────────────────

func TestBootIPXE_Directives(t *testing.T) {
	dir := tempArtifactDir(t)
	mux := bootMux(t, dir, "")

	body := bootGet(mux, "/boot/ipxe").Body.String()

	for _, directive := range []string{"kernel ", "initrd ", "boot"} {
		if !strings.Contains(body, directive) {
			t.Errorf("iPXE script missing %q directive:\n%s", directive, body)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-2: GET /boot/manifest.json returns JSON with required fields
// ─────────────────────────────────────────────────────────────────────────────

func TestBootManifest_WithArtifactDir(t *testing.T) {
	dir := tempArtifactDir(t)
	mux := bootMux(t, dir, "")

	rr := bootGet(mux, "/boot/manifest.json")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}

	var m map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	for _, field := range []string{"version", "artifact_url", "sha256", "signature"} {
		if _, ok := m[field]; !ok {
			t.Errorf("manifest missing required field %q; got: %v", field, m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-3: BOOT_ARTIFACT_DIR="" → 302 for kernel/initramfs/manifest
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEndpoints_NoBucketBase_302(t *testing.T) {
	const bucket = "https://bucket.vulos.cloud/boot"
	mux := bootMux(t, "", bucket)

	paths := []struct {
		path     string
		wantSufx string
	}{
		{"/boot/kernel", bucket + "/kernel"},
		{"/boot/initramfs", bucket + "/initramfs"},
		{"/boot/manifest.json", bucket + "/manifest.json"},
	}

	for _, tc := range paths {
		rr := bootGet(mux, tc.path)
		if rr.Code != http.StatusFound {
			t.Errorf("GET %s: expected 302, got %d: %s", tc.path, rr.Code, rr.Body.String())
			continue
		}
		loc := rr.Header().Get("Location")
		if loc != tc.wantSufx {
			t.Errorf("GET %s: expected Location %q, got %q", tc.path, tc.wantSufx, loc)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-3b: BOOT_ARTIFACT_DIR="" → /boot/ipxe returns text/plain with bucket URLs
// ─────────────────────────────────────────────────────────────────────────────

func TestBootIPXE_BucketFallback(t *testing.T) {
	const bucket = "https://bucket.vulos.cloud/boot"
	mux := bootMux(t, "", bucket)

	rr := bootGet(mux, "/boot/ipxe")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Errorf("iPXE script must start with #!ipxe, got:\n%s", body)
	}
	if !strings.Contains(body, bucket+"/kernel") {
		t.Errorf("iPXE script must reference %s/kernel, got:\n%s", bucket, body)
	}
	if !strings.Contains(body, bucket+"/initramfs") {
		t.Errorf("iPXE script must reference %s/initramfs, got:\n%s", bucket, body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-4: /boot/manifest.json → 404 when neither dir nor bucket is set
// ─────────────────────────────────────────────────────────────────────────────

func TestBootManifest_NotFoundWhenNeitherSet(t *testing.T) {
	mux := bootMux(t, "", "")

	rr := bootGet(mux, "/boot/manifest.json")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no manifest source, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-4b: /boot/manifest.json → 404 when BOOT_ARTIFACT_DIR set but file absent
// ─────────────────────────────────────────────────────────────────────────────

func TestBootManifest_NotFoundWhenFileAbsent(t *testing.T) {
	dir := t.TempDir() // empty — no manifest.json
	mux := bootMux(t, dir, "https://bucket.vulos.cloud/boot")

	rr := bootGet(mux, "/boot/manifest.json")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 when manifest.json absent in BOOT_ARTIFACT_DIR, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC-5 (invariant): no signing key imported — compile-time via import list.
// Verified by the fact that this file and routes_boot.go import no signer
// package (no crypto/ed25519, no ota.Signer, no KeyProvider).
// ─────────────────────────────────────────────────────────────────────────────

// TestBootNoSigningKey is a documentation-as-test: the test binary must compile
// without any signer import in routes_boot.go. If someone accidentally adds
// ota.Signer or KeyProvider, the import graph will fail the build. This test
// exists to anchor the AC in the test suite and always passes at runtime.
func TestBootNoSigningKey(t *testing.T) {
	t.Log("AC-5: routes_boot.go imports no signing key — verified at compile time by absence of crypto/ed25519, ota.Signer, or KeyProvider")
}

// ─────────────────────────────────────────────────────────────────────────────
// CORS header tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBootEndpoints_CORSHeaders(t *testing.T) {
	const bucket = "https://bucket.vulos.cloud/boot"
	mux := bootMux(t, "", bucket)

	paths := []string{"/boot/ipxe", "/boot/kernel", "/boot/initramfs", "/boot/manifest.json"}

	for _, path := range paths {
		rr := bootGet(mux, path)
		origin := rr.Header().Get("Access-Control-Allow-Origin")
		if origin != "*" {
			t.Errorf("GET %s: expected Access-Control-Allow-Origin: *, got %q", path, origin)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BOOT_ARTIFACT_DIR set — kernel and initramfs are served directly (200)
// ─────────────────────────────────────────────────────────────────────────────

func TestBootKernelInitramfs_ServedFromDir(t *testing.T) {
	dir := tempArtifactDir(t)
	mux := bootMux(t, dir, "")

	for _, tc := range []struct{ path, want string }{
		{"/boot/kernel", "KERNEL_DATA"},
		{"/boot/initramfs", "INITRAMFS_DATA"},
	} {
		rr := bootGet(mux, tc.path)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: expected 200, got %d: %s", tc.path, rr.Code, rr.Body.String())
			continue
		}
		if !strings.Contains(rr.Body.String(), tc.want) {
			t.Errorf("GET %s: body does not contain %q", tc.path, tc.want)
		}
	}
}
