package appnet

import (
	"context"
	"strings"
	"testing"
)

// TestAppStore_Install_RejectsSSRFDownloadURLs is a regression test for the
// SSRF guard added to AppStore.Install. entry.DownloadURL is decoded straight
// from an admin's POST /api/store/install request body (cmd/server/main.go)
// with no registry signature and no mandatory checksum — unlike
// InstallFromRegistry, which only ever downloads a URL from an Ed25519-signed,
// vetted registry entry. Before this fix, AppStore.client had no SSRF guard at
// all: an admin session (or a CSRF'd one) could point Install at a cloud
// metadata endpoint, a loopback admin API, or any LAN host, and the box would
// fetch, tar-extract, and install whatever came back.
func TestAppStore_Install_RejectsSSRFDownloadURLs(t *testing.T) {
	store := NewAppStore(t.TempDir())
	ctx := context.Background()

	cases := []struct {
		name string
		url  string
	}{
		{"cloud metadata endpoint", "http://169.254.169.254/latest/meta-data/iam/security-credentials/"},
		{"loopback", "http://127.0.0.1:8080/admin/dump"},
		{"loopback hostname", "http://localhost:9999/internal"},
		{"RFC1918 LAN", "http://192.168.1.1/firmware.tar.gz"},
		{"IPv6 loopback", "http://[::1]:8080/"},
		{"non-http scheme", "file:///etc/passwd"},
		{"obfuscated decimal loopback", "http://2130706433/payload.tar.gz"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			entry := StoreEntry{
				AppManifest: AppManifest{ID: "ssrf-test-app"},
				DownloadURL: tc.url,
			}
			err := store.Install(ctx, entry)
			if err == nil {
				t.Fatalf("Install accepted SSRF-shaped download URL %q — no SSRF guard", tc.url)
			}
			if !strings.Contains(err.Error(), "refusing download URL") {
				t.Fatalf("Install(%q) failed for the wrong reason (want SSRF guard rejection): %v", tc.url, err)
			}
			t.Logf("correctly refused %q: %v", tc.url, err)
		})
	}
}

// TestValidateStoreDownloadURL_AllowsPublic is the positive control for the
// pre-dial half of the guard: a definitively public IP literal (no DNS, no
// live network call — IsDeniedIP is a pure function) must pass, proving the
// guard does not over-block. Exercised directly against
// validateStoreDownloadURL (rather than the full Install, which would need a
// live network round trip) so the test has no network dependency at all.
func TestValidateStoreDownloadURL_AllowsPublic(t *testing.T) {
	for _, u := range []string{
		"https://1.1.1.1/app.tar.gz",
		"http://8.8.8.8:8080/app.tar.gz",
	} {
		if err := validateStoreDownloadURL(u); err != nil {
			t.Errorf("validateStoreDownloadURL(%q) incorrectly rejected a public IP literal: %v", u, err)
		}
	}
}
