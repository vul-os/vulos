package appnet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRejectEmptyChecksum verifies that installing a recipe that downloads a binary
// directly without a checksum is refused with a hard error (SEC-H3).
func TestRejectEmptyChecksum(t *testing.T) {
	// Signature check is bypassed (INSECURE=1) so we can reach the checksum gate.
	t.Setenv(envRegistryInsecure, "1")
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"testapp": {
				Name:   "Test App",
				Vetted: true,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"1.0": {
						// wget direct binary download — checksum is empty → must fail.
						Install:  "wget -qO /usr/local/bin/testapp https://example.com/releases/testapp-1.0-linux-amd64 && chmod +x /usr/local/bin/testapp",
						Command:  "testapp --port ${PORT}",
						Port:     8080,
						Checksum: "", // deliberately empty
					},
				},
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "testapp", "1.0", dir)
	if err == nil {
		t.Fatal("expected error for empty checksum on binary download, got nil")
	}
	if !strings.Contains(err.Error(), "no checksum") && !strings.Contains(err.Error(), "checksum") {
		t.Errorf("expected checksum-related error, got: %v", err)
	}
	t.Logf("correctly rejected: %v", err)
}

// TestRejectCurlBashRecipe verifies that a curl|bash pipe-to-shell install recipe
// is unconditionally refused (SEC-H3).
func TestRejectCurlBashRecipe(t *testing.T) {
	t.Setenv(envRegistryInsecure, "1")
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"badapp": {
				Name:   "Bad App",
				Vetted: true,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"latest": {
						Install: "curl -fsSL https://example.com/install.sh | bash",
						Command: "badapp --port ${PORT}",
						Port:    9090,
					},
				},
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "badapp", "latest", dir)
	if err == nil {
		t.Fatal("expected error for curl|bash recipe, got nil")
	}
	if !strings.Contains(err.Error(), "pipe-to-shell") && !strings.Contains(err.Error(), "security") {
		t.Errorf("expected pipe-to-shell/security error, got: %v", err)
	}
	t.Logf("correctly rejected: %v", err)
}

// TestRejectWgetBashRecipe verifies that a wget|bash pipe-to-shell pattern is also refused.
func TestRejectWgetBashRecipe(t *testing.T) {
	t.Setenv(envRegistryInsecure, "1")
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"badapp2": {
				Name:   "Bad App 2",
				Vetted: true,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"1": {
						Install: "wget -qO- https://example.com/setup.sh | sh",
						Command: "badapp2 serve",
						Port:    8080,
					},
				},
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "badapp2", "1", dir)
	if err == nil {
		t.Fatal("expected error for wget|sh recipe, got nil")
	}
	if !strings.Contains(err.Error(), "pipe-to-shell") && !strings.Contains(err.Error(), "security") {
		t.Errorf("expected pipe-to-shell/security error, got: %v", err)
	}
	t.Logf("correctly rejected: %v", err)
}

// TestRejectDisabledEntry verifies that a recipe with _disabled:true is refused.
func TestRejectDisabledEntry(t *testing.T) {
	t.Setenv(envRegistryInsecure, "1")
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"disabledapp": {
				Name:   "Disabled App",
				Vetted: false,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"latest": {
						Disabled: true,
						Command:  "disabledapp serve",
						Port:     9090,
					},
				},
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "disabledapp", "latest", dir)
	if err == nil {
		t.Fatal("expected error for disabled entry, got nil")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected 'disabled' in error, got: %v", err)
	}
	t.Logf("correctly rejected: %v", err)
}

// TestValidChecksumNonShellPipeRecipe verifies that a recipe with a proper checksum
// embedded in the install command and no pipe-to-shell pattern passes the security gate
// and proceeds to actual execution (mocked by using a non-existent curl command that
// fails for reasons other than the security check).
func TestValidChecksumNonShellPipeRecipe(t *testing.T) {
	t.Setenv(envRegistryInsecure, "1")
	// This recipe mimics the pinned-artifact shape: it downloads a binary, verifies
	// its sha256, and chmod's it. It has a non-empty Checksum field. The security gate
	// should pass; the actual shell command will fail because we're in a test environment
	// without a real binary — but the failure must NOT be a security error.
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"goodapp": {
				Name:   "Good App",
				Vetted: true,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"1.0": {
						Install:  "curl -fSL -o bin/goodapp https://example.com/goodapp-1.0 && echo 'abc123def456abc123def456abc123def456abc123def456abc123def456abc1  bin/goodapp' | sha256sum -c - && chmod +x bin/goodapp",
						Checksum: "abc123def456abc123def456abc123def456abc123def456abc123def456abc1",
						Command:  "bin/goodapp --port ${PORT}",
						Port:     8080,
					},
				},
			},
		},
	}

	dir := t.TempDir()
	// Create the bin/ subdir so the install can at least start.
	os.MkdirAll(filepath.Join(dir, "goodapp", "bin"), 0755)

	err := InstallFromRegistry(context.Background(), reg, "goodapp", "1.0", dir)
	// The install will fail (no real binary at that URL in test env) but the error
	// must not be a security gate error — it should be a command execution error.
	if err != nil {
		securityErr := strings.Contains(err.Error(), "pipe-to-shell") ||
			strings.Contains(err.Error(), "no checksum") ||
			strings.Contains(err.Error(), "SEC-H3") ||
			strings.Contains(err.Error(), "disabled")
		if securityErr {
			t.Errorf("security gate should have passed for valid recipe, but got security error: %v", err)
		} else {
			t.Logf("security gate passed; install failed for non-security reason (expected in test): %v", err)
		}
	} else {
		t.Log("install succeeded (unexpected in unit test but security gate passed)")
	}
}

// TestAptGetRecipeNoChecksumAllowed verifies that apt-get install recipes (which use
// package-manager integrity, not a sha256 field) are not required to have a Checksum.
func TestAptGetRecipeNoChecksumAllowed(t *testing.T) {
	t.Setenv(envRegistryInsecure, "1")
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"nginx": {
				Name:   "Nginx",
				Vetted: true,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"1": {
						Install:  "apt-get install -y --no-install-recommends nginx",
						Command:  "nginx -c data/nginx.conf -g 'daemon off;'",
						Port:     8080,
						Checksum: "", // apt-get: no sha256 field needed
					},
				},
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "nginx", "1", dir)
	// Should fail because apt-get is not available / fails in test — but must NOT be a security error.
	if err != nil {
		securityErr := strings.Contains(err.Error(), "pipe-to-shell") ||
			strings.Contains(err.Error(), "no checksum") ||
			strings.Contains(err.Error(), "SEC-H3") ||
			strings.Contains(err.Error(), "disabled")
		if securityErr {
			t.Errorf("apt-get recipe should not trigger security error, got: %v", err)
		} else {
			t.Logf("security gate correctly passed apt-get recipe; non-security failure: %v", err)
		}
	} else {
		t.Log("install succeeded")
	}
}

// TestRegistryJSONValid verifies that registry.json is valid JSON and all non-disabled
// versioned entries that download binaries directly have non-empty checksums.
func TestRegistryJSONValid(t *testing.T) {
	// Walk up from the package directory to the repo root to find registry.json.
	candidates := []string{
		"../../../../registry.json",
		"../../../registry.json",
		"../../registry.json",
		"../registry.json",
	}

	var reg *Registry
	var found string
	for _, path := range candidates {
		r, err := LoadRegistry(path)
		if err == nil {
			reg = r
			found = path
			break
		}
	}
	if reg == nil {
		t.Skip("registry.json not found relative to test directory; skipping registry validation")
	}
	t.Logf("loaded registry from %s", found)

	for appID, entry := range reg.Apps {
		for ver, recipe := range entry.Versions {
			label := appID + "@" + ver

			if recipe.Disabled {
				t.Logf("%s: disabled, skipping checksum check", label)
				continue
			}

			// Pipe-to-shell must not appear in any non-disabled recipe.
			if err := rejectPipeToShell(recipe.Install); err != nil {
				t.Errorf("%s: install recipe fails pipe-to-shell check: %v", label, err)
			}
			if err := rejectPipeToShell(recipe.PostInstall); err != nil {
				t.Errorf("%s: post_install recipe fails pipe-to-shell check: %v", label, err)
			}

			// Binary downloads must have a checksum.
			if requiresChecksum(recipe.Install) && strings.TrimSpace(recipe.Checksum) == "" {
				t.Errorf("%s: install downloads a binary directly but has no checksum (SEC-H3)", label)
			}
		}
	}
}

// TestRejectShCPipePattern verifies the sh -c "... | bash" pattern is also rejected.
func TestRejectShCPipePattern(t *testing.T) {
	tests := []struct {
		name    string
		install string
	}{
		{"sh -c with pipe", `sh -c "curl https://example.com/install.sh | bash"`},
		{"bash -c with pipe", `bash -c 'wget -O- https://example.com/setup | sh'`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envRegistryInsecure, "1")
			reg := &Registry{
				Apps: map[string]*RegistryEntry{
					"shcapp": {
						Name:   "ShC App",
						Vetted: true,
						Type:   "web",
						Versions: map[string]*VersionRecipe{
							"1": {
								Install: tc.install,
								Command: "app serve",
								Port:    8080,
							},
						},
					},
				},
			}
			dir := t.TempDir()
			err := InstallFromRegistry(context.Background(), reg, "shcapp", "1", dir)
			if err == nil {
				t.Fatalf("expected security error for %q, got nil", tc.install)
			}
			if !strings.Contains(err.Error(), "pipe") && !strings.Contains(err.Error(), "security") {
				t.Errorf("expected pipe/security error, got: %v", err)
			}
			t.Logf("correctly rejected %q: %v", tc.install, err)
		})
	}
}
