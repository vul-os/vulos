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
// pinned static recipe must pass the gate (so the fix above does not
// over-block legitimate pinned downloads).
//
// Rewritten for INSTALL-METHODOLOGY: the positive case is now the `artifacts`
// map, because `download_url` is refused outright (DOWNLOAD-01). The assertion
// is unchanged in strength — a correctly pinned download must be ACCEPTED — and
// the negative case above still exercises SECAUDIT2-H1 through download_url,
// which is why that rule is still ordered ahead of DOWNLOAD-01.
func TestStaticDownloadWithChecksumAccepted(t *testing.T) {
	recipe := &VersionRecipe{
		Artifacts: map[string]*Artifact{
			"amd64": {
				DownloadURL: "https://example.com/releases/app-1.0-linux-amd64.tar.gz",
				Checksum:    "0000000000000000000000000000000000000000000000000000000000000000",
			},
			"arm64": {
				DownloadURL: "https://example.com/releases/app-1.0-linux-arm64.tar.gz",
				Checksum:    "1111111111111111111111111111111111111111111111111111111111111111",
			},
		},
		Command: "bin/app",
		Port:    8080,
	}
	if err := validateRecipeSecurity(recipe); err != nil {
		t.Fatalf("a static recipe WITH a checksum must pass the security "+
			"gate, got: %v", err)
	}
}

// TestShellInstallRefused is INSTALL-01's own assertion, kept next to the
// SECAUDIT2 rules because it closes the hole those rules could only narrow.
//
// `TestStaticDownloadRequiresChecksum` proves an unchecksummed download is
// refused; `TestPipeToShellStillRejected` proves curl|bash is refused. Neither
// could refuse `code-server`'s real recipe, which verified a checksum it had
// INVENTED and piped the verification into `|| true` so `dpkg -i` ran anyway.
// The only defence against that is not accepting a shell string at all.
func TestShellInstallRefused(t *testing.T) {
	for _, cmd := range []string{
		"apt-get install -y --no-install-recommends blender",
		"git clone --depth=1 https://github.com/example/app.git static/",
		"wget -qO /tmp/x.tar.gz https://example.com/x.tar.gz && tar xzf /tmp/x.tar.gz -C static/",
		// The real code-server line, shortened: a forged digest, checked, and
		// then the check discarded.
		"curl -fsSL https://example.com/x.deb -o /tmp/x.deb && echo '0e8c...  /tmp/x.deb' | sha256sum --status -c - 2>/dev/null || true && dpkg -i /tmp/x.deb",
	} {
		r := &VersionRecipe{Install: cmd, Command: "app"}
		err := validateRecipeSecurity(r)
		if err == nil {
			t.Fatalf("INSTALL-01 REGRESSION: a shell install command was ACCEPTED: %q", cmd)
		}
		t.Logf("refused %.60q: %v", cmd, err)
	}
}

// TestUnclassifiedRecipeGetsNoInstallPath is INSTALL-02. A recipe that names
// neither vehicle used to fall through to `sh -c ""` and be reported as a
// successful install of nothing; `vaultwarden` ships in exactly that shape.
func TestUnclassifiedRecipeGetsNoInstallPath(t *testing.T) {
	r := &VersionRecipe{Command: "bin/vaultwarden", Port: 8080}
	err := validateRecipeSecurity(r)
	if err == nil {
		t.Fatal("INSTALL-02 REGRESSION: a recipe with no flatpak_id and no artifacts was ACCEPTED")
	}
	if !strings.Contains(err.Error(), "INSTALL-02") {
		t.Errorf("expected an INSTALL-02 refusal, got: %v", err)
	}
}

// TestPostInstallMayNotFetch is POSTINSTALL-02. post_install survives the
// removal of the install shell only because it may not be an install shell:
// the moment it can fetch, the signed per-artefact digest stops meaning
// anything, because the bytes that end up on the box are not the pinned ones.
func TestPostInstallMayNotFetch(t *testing.T) {
	valid := map[string]*Artifact{
		"amd64": {DownloadURL: "https://example.test/a", Checksum: strings.Repeat("a", 64)},
		"arm64": {DownloadURL: "https://example.test/b", Checksum: strings.Repeat("b", 64)},
	}
	for _, cmd := range []string{
		"npm install --production",
		"mkdir -p data && wget -qO data/model.bin https://example.com/model.bin",
		"curl -sf https://example.com/seed.sql | sqlite3 data/db",
		"apt-get install -y ffmpeg",
		"pip3 install --break-system-packages libretranslate",
		"git clone https://github.com/example/plugins data/plugins",
	} {
		r := &VersionRecipe{Artifacts: valid, PostInstall: cmd, Command: "app"}
		if err := validateRecipeSecurity(r); err == nil {
			t.Errorf("POSTINSTALL-02 REGRESSION: a fetching post_install was ACCEPTED: %q", cmd)
		}
	}
	// Control: the shape the three verified first-party entries actually use —
	// write a config, mint a secret, make a state directory — must still pass.
	// Without this the rule could be satisfied by refusing every post_install.
	ok := &VersionRecipe{
		Artifacts: valid,
		Command:   "bin/app",
		PostInstall: `mkdir -p data cache sessions && ` +
			`printf 'port = %s\n' "8080" > data/app.toml && ` +
			`head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > data/secret`,
	}
	if err := validateRecipeSecurity(ok); err != nil {
		t.Fatalf("a configure-only post_install must still be accepted, got: %v", err)
	}
}

// TestPostInstallMayNotSwallowFailure is POSTINSTALL-03. POSTINSTALL-01 made a
// failed post_install fatal; `|| true` re-opens that from inside the string.
func TestPostInstallMayNotSwallowFailure(t *testing.T) {
	valid := map[string]*Artifact{
		"amd64": {DownloadURL: "https://example.test/a", Checksum: strings.Repeat("a", 64)},
	}
	for _, cmd := range []string{
		"mkdir -p data || true",
		"bin/app --init 2>/dev/null || true",
		"bin/app --init || :",
		"bin/app --init || true && echo done",
	} {
		r := &VersionRecipe{Artifacts: valid, PostInstall: cmd, Command: "app"}
		if err := validateRecipeSecurity(r); err == nil {
			t.Errorf("POSTINSTALL-03 REGRESSION: a failure-swallowing post_install was ACCEPTED: %q", cmd)
		}
	}
	// Control: `||` is not itself the problem — a real fallback that can still
	// fail is fine. Refusing every `||` would pass the loop above while making
	// the rule about the wrong thing.
	r := &VersionRecipe{Artifacts: valid, Command: "app", PostInstall: "test -d data || mkdir data"}
	if err := validateRecipeSecurity(r); err != nil {
		t.Fatalf("a post_install with a real fallback must be accepted, got: %v", err)
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
