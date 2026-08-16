package appnet

// registry_artifacts_test.go — ARTIFACTS-01.
//
// Covers the per-architecture download form (`artifacts`), the `binary_name`
// rename that makes it usable, and the zip extraction path that lilmail needs.
//
// The point these tests defend is narrow and easy to lose: a per-arch recipe
// must resolve to the RIGHT artifact and must REFUSE when it cannot, because
// every wrong answer here is a successful install of a binary the box cannot
// execute — a green install and a dead app, which is precisely the failure the
// artifacts field was added to remove.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ── Resolution ───────────────────────────────────────────────────────────────

// TestResolveArtifact_PicksTheBoxArch is the core claim: two artifacts, and the
// one matching the box is chosen. A test that only checked "no error" would
// pass even if the resolver always returned the first map entry, which on Go's
// randomised map iteration would be a coin flip per install.
func TestResolveArtifact_PicksTheBoxArch(t *testing.T) {
	recipe := &VersionRecipe{Artifacts: map[string]*Artifact{
		"amd64": {DownloadURL: "https://example.test/app-linux-amd64", Checksum: "aa"},
		"arm64": {DownloadURL: "https://example.test/app-linux-arm64", Checksum: "bb"},
	}}

	for _, tc := range []struct{ boxArch, wantURL, wantSum string }{
		{"amd64", "https://example.test/app-linux-amd64", "aa"},
		{"arm64", "https://example.test/app-linux-arm64", "bb"},
		// Foreign spellings must land on the same artifact — arch.go's
		// NormalizeArch is the single normalisation point and this path has to
		// go through it, or a box reporting x86_64 gets "no artifact".
		{"x86_64", "https://example.test/app-linux-amd64", "aa"},
		{"aarch64", "https://example.test/app-linux-arm64", "bb"},
	} {
		url, sum, err := recipe.ResolveArtifact(tc.boxArch)
		if err != nil {
			t.Fatalf("boxArch=%s: unexpected error: %v", tc.boxArch, err)
		}
		if url != tc.wantURL {
			t.Errorf("boxArch=%s: url = %q, want %q", tc.boxArch, url, tc.wantURL)
		}
		if sum != tc.wantSum {
			t.Errorf("boxArch=%s: checksum = %q, want %q", tc.boxArch, sum, tc.wantSum)
		}
	}
}

// TestResolveArtifact_RegistryMaySpellArchForeignly covers the other side of the
// same table: the KEY in the registry, not just the box's report.
func TestResolveArtifact_RegistryMaySpellArchForeignly(t *testing.T) {
	recipe := &VersionRecipe{Artifacts: map[string]*Artifact{
		"x86_64":  {DownloadURL: "https://example.test/x", Checksum: "aa"},
		"aarch64": {DownloadURL: "https://example.test/a", Checksum: "bb"},
	}}
	url, _, err := recipe.ResolveArtifact("arm64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://example.test/a" {
		t.Errorf("url = %q, want the aarch64 artifact", url)
	}
}

// TestResolveArtifact_MissingArchIsRefusedNotFallenBackTo is the test that
// matters most. A fallback to download_url would install an amd64 binary on an
// arm64 box: checksum-valid, manifest written, install reported successful, and
// the app can never exec. The refusal must also NAME what is on offer, so the
// failure is diagnosable from the message alone.
func TestResolveArtifact_MissingArchIsRefusedNotFallenBackTo(t *testing.T) {
	recipe := &VersionRecipe{
		// A download_url here would be refused by validateRecipeSecurity; it is
		// set only to prove ResolveArtifact does not reach for it.
		DownloadURL: "https://example.test/fallback-amd64",
		Checksum:    "cc",
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: "https://example.test/app-linux-amd64", Checksum: "aa"},
		},
	}
	url, sum, err := recipe.ResolveArtifact("arm64")
	if err == nil {
		t.Fatalf("expected a refusal for arm64; got url=%q checksum=%q", url, sum)
	}
	if url != "" || sum != "" {
		t.Errorf("a refusal must return nothing usable; got url=%q checksum=%q", url, sum)
	}
	if !strings.Contains(err.Error(), "arm64") {
		t.Errorf("error must name the box's architecture: %v", err)
	}
	if !strings.Contains(err.Error(), "amd64") {
		t.Errorf("error must name the architectures the recipe does offer: %v", err)
	}
	if strings.Contains(err.Error(), "fallback") {
		t.Errorf("error mentions the download_url — it must never be consulted: %v", err)
	}
}

// TestResolveArtifact_SingleArchRecipeUnchanged pins the compatibility promise
// the other 55 entries rest on: with no artifacts, the recipe resolves to
// itself for any box arch, exactly as before this field existed.
func TestResolveArtifact_SingleArchRecipeUnchanged(t *testing.T) {
	recipe := &VersionRecipe{DownloadURL: "https://example.test/only", Checksum: "dd"}
	for _, arch := range []string{"amd64", "arm64", "riscv64", ""} {
		url, sum, err := recipe.ResolveArtifact(arch)
		if err != nil {
			t.Fatalf("arch=%q: %v", arch, err)
		}
		if url != "https://example.test/only" || sum != "dd" {
			t.Errorf("arch=%q: got %q/%q, want the recipe's own url/checksum", arch, url, sum)
		}
	}
}

// ── Validation ───────────────────────────────────────────────────────────────

// TestValidateRecipeSecurity_ArtifactsRules walks every way the new fields can
// be set wrongly. Each case must be REFUSED; a schema that silently accepts one
// of these ships a recipe whose behaviour nobody can predict from reading it.
func TestValidateRecipeSecurity_ArtifactsRules(t *testing.T) {
	good := &Artifact{DownloadURL: "https://example.test/app-linux-amd64", Checksum: strings.Repeat("a", 64)}

	cases := []struct {
		name   string
		recipe *VersionRecipe
		want   string
	}{
		{
			name: "both forms set is ambiguous",
			recipe: &VersionRecipe{
				DownloadURL: "https://example.test/app",
				Checksum:    strings.Repeat("b", 64),
				Artifacts:   map[string]*Artifact{"amd64": good},
			},
			want: "BOTH download_url and artifacts",
		},
		{
			name: "top-level checksum beside artifacts",
			recipe: &VersionRecipe{
				Checksum:  strings.Repeat("b", 64),
				Artifacts: map[string]*Artifact{"amd64": good},
			},
			want: "top-level checksum alongside artifacts",
		},
		{
			name:   "artifact with no checksum",
			recipe: &VersionRecipe{Artifacts: map[string]*Artifact{"arm64": {DownloadURL: "https://example.test/a"}}},
			want:   "SECAUDIT2-H1",
		},
		{
			name:   "artifact with no url",
			recipe: &VersionRecipe{Artifacts: map[string]*Artifact{"arm64": {Checksum: strings.Repeat("a", 64)}}},
			want:   "no download_url",
		},
		{
			name:   "null artifact",
			recipe: &VersionRecipe{Artifacts: map[string]*Artifact{"arm64": nil}},
			want:   "is null",
		},
		{
			name: "one architecture spelled two ways",
			recipe: &VersionRecipe{Artifacts: map[string]*Artifact{
				"amd64":  good,
				"x86_64": {DownloadURL: "https://example.test/other", Checksum: strings.Repeat("c", 64)},
			}},
			want: "twice",
		},
		{
			name: "binary_name on an archive download_url",
			recipe: &VersionRecipe{
				DownloadURL: "https://example.test/app.tar.gz",
				Checksum:    strings.Repeat("b", 64),
				BinaryName:  "app",
			},
			want: "does nothing for an archive",
		},
		{
			name: "binary_name on an archive artifact",
			recipe: &VersionRecipe{
				BinaryName: "app",
				Artifacts: map[string]*Artifact{
					"amd64": {DownloadURL: "https://example.test/app.zip", Checksum: strings.Repeat("b", 64)},
				},
			},
			want: "does nothing for an archive",
		},
		{
			name: "binary_name containing a path",
			recipe: &VersionRecipe{
				BinaryName: "../../etc/cron.d/x",
				Artifacts:  map[string]*Artifact{"amd64": good},
			},
			want: "plain filename",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRecipeSecurity(tc.recipe)
			if err == nil {
				t.Fatalf("recipe was ACCEPTED; expected a refusal mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestValidateRecipeSecurity_ValidArtifactsRecipeAccepted is the control. Every
// case above is a red; without a green here the suite would pass just as well
// if validateRecipeSecurity rejected everything.
func TestValidateRecipeSecurity_ValidArtifactsRecipeAccepted(t *testing.T) {
	recipe := &VersionRecipe{
		BinaryName: "diwan",
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: "https://example.test/diwan-linux-amd64", Checksum: strings.Repeat("a", 64)},
			"arm64": {DownloadURL: "https://example.test/diwan-linux-arm64", Checksum: strings.Repeat("b", 64)},
		},
	}
	if err := validateRecipeSecurity(recipe); err != nil {
		t.Fatalf("a well-formed per-arch recipe was refused: %v", err)
	}
}

// ── Install ──────────────────────────────────────────────────────────────────

// TestStaticInstall_PerArchInstallsTheMatchingBinary drives the real installer
// through the per-arch path and asserts the BYTES on disk are the ones for this
// box, not merely that some file appeared.
func TestStaticInstall_PerArchInstallsTheMatchingBinary(t *testing.T) {
	amd := []byte("I am the amd64 build\n")
	arm := []byte("I am the arm64 build\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "amd64"):
			w.Write(amd)
		case strings.HasSuffix(r.URL.Path, "arm64"):
			w.Write(arm)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	recipe := &VersionRecipe{
		BinaryName: "app",
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: srv.URL + "/app-linux-amd64", Checksum: sha256Hex(amd)},
			"arm64": {DownloadURL: srv.URL + "/app-linux-arm64", Checksum: sha256Hex(arm)},
		},
	}

	for _, tc := range []struct {
		boxArch string
		want    []byte
	}{{"amd64", amd}, {"arm64", arm}} {
		t.Run(tc.boxArch, func(t *testing.T) {
			// VULOS_BOX_ARCH is the server-side override arch.go documents for
			// exactly this: testing the box's arch handling on any machine.
			t.Setenv("VULOS_BOX_ARCH", tc.boxArch)

			appDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := staticInstall(context.Background(), recipe, appDir); err != nil {
				t.Fatalf("staticInstall: %v", err)
			}
			// binary_name means the path is the SAME on both arches — that is
			// what lets one `command` string serve both.
			dest := filepath.Join(appDir, "bin", "app")
			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatalf("binary not at %s: %v", dest, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("installed the WRONG architecture's binary: got %q, want %q", got, tc.want)
			}
			info, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&0111 == 0 {
				t.Errorf("installed binary is not executable (mode=%s)", info.Mode())
			}
		})
	}
}

// TestStaticInstall_PerArchChecksumIsTheArtifactsOwn guards a specific way this
// could be got wrong: verifying against recipe.Checksum (empty on a per-arch
// recipe) would skip verification entirely and every corrupt download would
// install silently.
func TestStaticInstall_PerArchChecksumIsTheArtifactsOwn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not what was pinned"))
	}))
	defer srv.Close()

	t.Setenv("VULOS_BOX_ARCH", "amd64")
	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	recipe := &VersionRecipe{Artifacts: map[string]*Artifact{
		"amd64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex([]byte("the pinned bytes"))},
	}}
	err := staticInstall(context.Background(), recipe, appDir)
	if err == nil {
		t.Fatal("a mismatched artifact checksum was ACCEPTED — per-arch downloads are unverified")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected a checksum mismatch, got: %v", err)
	}
}

// TestStaticInstall_PerArchRefusesUncoveredBox proves the refusal reaches the
// installer, not just the resolver, and that nothing is written when it fires.
func TestStaticInstall_PerArchRefusesUncoveredBox(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "riscv64")
	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	recipe := &VersionRecipe{Artifacts: map[string]*Artifact{
		"amd64": {DownloadURL: "https://example.invalid/app", Checksum: strings.Repeat("a", 64)},
	}}
	if err := staticInstall(context.Background(), recipe, appDir); err == nil {
		t.Fatal("staticInstall accepted a box no artifact covers")
	}
	entries, err := os.ReadDir(filepath.Join(appDir, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused install left %d file(s) in bin/", len(entries))
	}
}

// TestInstallFromRegistry_ArtifactsReachTheStaticPath drives the WHOLE product
// installer — the same InstallFromRegistry the box reaches at
// POST /api/store/install — over a per-arch entry.
//
// This test exists because a mutation proved it was missing: making the install
// dispatch read only `recipe.DownloadURL != ""` (so a per-arch recipe silently
// took the apt branch and installed nothing) SURVIVED the entire suite. Every
// other test here calls staticInstall directly, so none of them noticed that
// nothing routed to it. A per-arch entry that resolves perfectly and is never
// invoked is the same green-install-dead-app failure by another road.
func TestInstallFromRegistry_ArtifactsReachTheStaticPath(t *testing.T) {
	withInsecureRegistry(t)
	t.Setenv("VULOS_BOX_ARCH", "arm64")

	amd := []byte("#!/bin/sh\necho amd64\n")
	arm := []byte("#!/bin/sh\necho arm64\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "arm64") {
			w.Write(arm)
			return
		}
		w.Write(amd)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"perarch": {
			Name: "Per Arch", Vetted: true, Type: "web",
			Description: "per-arch test app", Category: "developer",
			Icon: "P", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Arch: []string{"amd64", "arm64"},
			Versions: map[string]*VersionRecipe{
				"1.0": {
					BinaryName: "perarch",
					Artifacts: map[string]*Artifact{
						"amd64": {DownloadURL: srv.URL + "/app-linux-amd64", Checksum: sha256Hex(amd)},
						"arm64": {DownloadURL: srv.URL + "/app-linux-arm64", Checksum: sha256Hex(arm)},
					},
					Command: "bin/perarch",
					Port:    8080,
				},
			},
		},
	}}

	if err := InstallFromRegistry(context.Background(), reg, "perarch", "1.0", appsDir); err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}

	binPath := filepath.Join(appsDir, "perarch", "bin", "perarch")
	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("per-arch entry installed no binary at %s: %v", binPath, err)
	}
	if !bytes.Equal(got, arm) {
		t.Errorf("installed the wrong architecture: got %q, want the arm64 build %q", got, arm)
	}
	// The manifest is what registers the app with the launcher; without it the
	// install is invisible to the product no matter what landed in bin/.
	if _, err := os.Stat(filepath.Join(appsDir, "perarch", "app.json")); err != nil {
		t.Errorf("no manifest written: %v", err)
	}
}

// ── post_install (POSTINSTALL-01) ────────────────────────────────────────────

// TestInstallFromRegistry_FailedPostInstallIsFatal pins the rule that a failed
// post_install fails the install.
//
// This is a regression test for a defect that shipped a broken app during the
// change that added it. lilmail's post_install contained `\'` inside a
// single-quoted sh string, where a backslash does not escape but ENDS the
// quote; sh exited 2 with "Syntax error: Unterminated quoted string", the
// config file was never written, and InstallFromRegistry logged a warning and
// RETURNED SUCCESS. The manifest was written, the binary was present and the
// correct architecture, and every launch died with "Failed to load config".
//
// For every entry that writes its config in post_install — conduit, gitea,
// navidrome, diwan, wede, lilmail — post_install IS the install.
func TestInstallFromRegistry_FailedPostInstallIsFatal(t *testing.T) {
	withInsecureRegistry(t)

	body := []byte("#!/bin/sh\necho hi\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"brokenpost": {
			Name: "Broken Post", Vetted: true, Type: "web",
			Description: "post_install fails", Category: "developer",
			Icon: "B", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					DownloadURL: srv.URL + "/app",
					Checksum:    sha256Hex(body),
					Command:     "bin/app",
					Port:        8080,
					// The real shape of the bug: a quote that never closes.
					PostInstall: `printf 'frame_ancestors = "\'self\'"\n' > config.toml`,
				},
			},
		},
	}}

	err := InstallFromRegistry(context.Background(), reg, "brokenpost", "1.0", appsDir)
	if err == nil {
		t.Fatal("install with a FAILING post_install returned success — " +
			"this is the successful-install-of-an-unconfigured-app defect")
	}
	if !strings.Contains(err.Error(), "post-install") {
		t.Errorf("error should name post-install as the cause: %v", err)
	}
	// The half-built directory must be gone, so a retry starts clean rather
	// than on top of whatever the failed command managed to create.
	if _, statErr := os.Stat(filepath.Join(appsDir, "brokenpost")); !os.IsNotExist(statErr) {
		t.Errorf("app dir survived a failed post_install: %v", statErr)
	}
}

// TestInstallFromRegistry_SucceedingPostInstallStillInstalls is the control.
// Without it, the test above would pass just as well if post_install were made
// to fail unconditionally.
func TestInstallFromRegistry_SucceedingPostInstallStillInstalls(t *testing.T) {
	withInsecureRegistry(t)

	body := []byte("#!/bin/sh\necho hi\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"goodpost": {
			Name: "Good Post", Vetted: true, Type: "web",
			Description: "post_install succeeds", Category: "developer",
			Icon: "G", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					DownloadURL: srv.URL + "/app",
					Checksum:    sha256Hex(body),
					Command:     "bin/app",
					Port:        8080,
					PostInstall: `printf 'ok\n' > config.txt`,
				},
			},
		},
	}}

	if err := InstallFromRegistry(context.Background(), reg, "goodpost", "1.0", appsDir); err != nil {
		t.Fatalf("install with a SUCCEEDING post_install failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appsDir, "goodpost", "config.txt")); err != nil {
		t.Errorf("post_install did not run: %v", err)
	}
}

// ── Zip ──────────────────────────────────────────────────────────────────────

// buildZip makes a zip whose members and modes the test controls.
func buildZip(t *testing.T, members map[string][]byte, modes map[string]os.FileMode) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range members {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if m, ok := modes[name]; ok {
			hdr.SetMode(m)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestStaticInstall_ZipIsExtractedNotDroppedAsAFile is the regression test for
// the gap this change closed. staticInstall recognised .tar.gz/.tgz/.tar.bz2/
// .tar.xz and NOTHING else, so a .zip fell through to the plain-binary branch:
// it was copied to bin/<name>.zip, chmod 0755, and the install reported
// SUCCESS. The app could never start. lilmail ships zips.
func TestStaticInstall_ZipIsExtractedNotDroppedAsAFile(t *testing.T) {
	body := []byte("#!/bin/sh\necho lilmail\n")
	payload := buildZip(t,
		map[string][]byte{
			"lilmail/lilmail":             body,
			"lilmail/config.toml.example": []byte("[server]\nport = 3000\n"),
		},
		map[string]os.FileMode{"lilmail/lilmail": 0755},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	recipe := &VersionRecipe{
		DownloadURL:  srv.URL + "/lilmail_1.14.0_linux_amd64.zip",
		Checksum:     sha256Hex(payload),
		ArchiveStrip: 1,
	}
	if err := staticInstall(context.Background(), recipe, appDir); err != nil {
		t.Fatalf("staticInstall: %v", err)
	}

	// The zip itself must NOT be sitting in bin/ pretending to be a program.
	if _, err := os.Stat(filepath.Join(appDir, "bin", "lilmail_1.14.0_linux_amd64.zip")); err == nil {
		t.Fatal("the .zip was installed as a binary — it was not recognised as an archive")
	}

	dest := filepath.Join(appDir, "lilmail")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("extracted binary not at %s: %v", dest, err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("extracted content mismatch")
	}
	// An extracted binary that lost its executable bit is an install that
	// cannot launch — the same silent failure in a different disguise.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("extracted binary is not executable (mode=%s)", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(appDir, "config.toml.example")); err != nil {
		t.Errorf("archive_strip=1 did not place the sibling file at the app root: %v", err)
	}
}

// TestExtractZip_RefusesTraversal applies to zip the same screen the tar path
// already had. `tar` does not block ../ members and neither does archive/zip;
// a checksum proves the archive is the one we pinned, not that its contents are
// safe to unpack as root.
// Both screens are asserted, and asserted SEPARATELY by the message they
// produce, because they defend different things: absolute names are refused
// outright (containment would let "/etc/cron.d/evil" trim into the app dir and
// look safe), while traversal is caught on the resolved destination.
func TestExtractZip_RefusesTraversal(t *testing.T) {
	for _, tc := range []struct{ member, wantMsg string }{
		{"../escaped", "escapes the app directory"},
		{"a/../../escaped", "escapes the app directory"},
		{"../../../../../../tmp/escaped", "escapes the app directory"},
		{"/etc/cron.d/evil", "absolute path"},
		{`\windows\evil`, "absolute path"},
	} {
		t.Run(tc.member, func(t *testing.T) {
			payload := buildZip(t, map[string][]byte{tc.member: []byte("x")}, nil)
			zipPath := filepath.Join(t.TempDir(), "a.zip")
			if err := os.WriteFile(zipPath, payload, 0644); err != nil {
				t.Fatal(err)
			}
			appDir := t.TempDir()
			err := extractZip(zipPath, appDir, 0)
			if err == nil {
				t.Fatalf("member %q was extracted; the screen did not fire", tc.member)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("member %q: error %q does not mention %q", tc.member, err.Error(), tc.wantMsg)
			}
			// Nothing may be written before a refusal — a half-extracted
			// malicious archive is still a compromised app dir.
			entries, rerr := os.ReadDir(appDir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(entries) != 0 {
				t.Errorf("refused archive still wrote %d entr(ies) into the app dir", len(entries))
			}
		})
	}
}

// TestStaticInstall_ZipDoesNotDependOnTar is the platform-honesty test.
//
// It exists because a mutation that routed .zip to the `tar` branch SURVIVED on
// this machine: macOS ships BSD tar (libarchive), which reads zips quite
// happily. The shipped Debian image ships GNU tar, which cannot read a zip at
// all — so that mutation was a defect invisible to a green local suite and
// fatal on every box a user owns. Exactly the host-is-not-the-target trap.
//
// Running the install with a `tar` on PATH that always fails models the target
// truthfully and makes the test's verdict independent of which tar the test
// machine happens to have.
func TestStaticInstall_ZipDoesNotDependOnTar(t *testing.T) {
	body := []byte("#!/bin/sh\necho lilmail\n")
	payload := buildZip(t,
		map[string][]byte{"lilmail/lilmail": body},
		map[string]os.FileMode{"lilmail/lilmail": 0755},
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	// A stub `tar` that refuses everything, the way GNU tar refuses a zip.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "tar")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho 'tar: This does not look like a tar archive' >&2\nexit 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	appDir := t.TempDir()
	recipe := &VersionRecipe{
		DownloadURL:  srv.URL + "/lilmail_1.14.0_linux_amd64.zip",
		Checksum:     sha256Hex(payload),
		ArchiveStrip: 1,
	}
	if err := staticInstall(context.Background(), recipe, appDir); err != nil {
		t.Fatalf("zip install failed when tar is unusable — the zip path is routing through tar: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(appDir, "lilmail"))
	if err != nil {
		t.Fatalf("binary not extracted: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("extracted content mismatch")
	}
}

// TestExtractZip_StripDropsShallowMembers pins the tar --strip-components
// semantic: a member with fewer components than the strip count is DROPPED, not
// flattened into the app root. Flattening would scatter an archive's top-level
// files over the app dir.
func TestExtractZip_StripDropsShallowMembers(t *testing.T) {
	payload := buildZip(t, map[string][]byte{
		"toplevel.txt":   []byte("dropped"),
		"dir/nested.txt": []byte("kept"),
	}, nil)
	zipPath := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(zipPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	appDir := t.TempDir()
	if err := extractZip(zipPath, appDir, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "nested.txt")); err != nil {
		t.Errorf("stripped member not placed at app root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "toplevel.txt")); err == nil {
		t.Error("a member shallower than strip= was flattened into the app root instead of dropped")
	}
}

// ── Round-tripping and signatures ────────────────────────────────────────────

// TestArtifacts_OmitEmptyKeepsExistingEntriesByteIdentical is the compatibility
// proof the 55 shipped entries depend on. The publisher signature covers the
// marshalled entry, so if adding these fields changed the serialisation of a
// recipe that does not use them, every existing signature would break at once.
func TestArtifacts_OmitEmptyKeepsExistingEntriesByteIdentical(t *testing.T) {
	const before = `{"install":"","flatpak_id":"","download_url":"https://example.test/a.tar.gz","archive_strip":1,"command":"bin/a","port":8080,"post_install":"","deps":null,"env":null,"permissions":null,"checksum":"abc","singleton":false,"auto_start":false}`

	var recipe VersionRecipe
	if err := json.Unmarshal([]byte(before), &recipe); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != before {
		t.Errorf("a recipe that uses neither new field no longer round-trips byte-identically.\n got: %s\nwant: %s", out, before)
	}
}

// TestArtifacts_RoundTripPreservesEveryField proves the new fields survive
// load→save. A field dropped on unmarshal would be a field outside the
// publisher signature, which is the exact hole RegistryEntry.Extra exists to
// close — and a per-arch URL silently dropped would send every box to whichever
// artifact remained.
func TestArtifacts_RoundTripPreservesEveryField(t *testing.T) {
	const src = `{"install":"","flatpak_id":"","download_url":"","artifacts":{"amd64":{"download_url":"https://example.test/x-amd64","checksum":"aa"},"arm64":{"download_url":"https://example.test/x-arm64","checksum":"bb"}},"archive_strip":0,"binary_name":"x","command":"bin/x","port":1,"post_install":"","deps":null,"env":null,"permissions":null,"checksum":"","singleton":false,"auto_start":false}`

	var recipe VersionRecipe
	if err := json.Unmarshal([]byte(src), &recipe); err != nil {
		t.Fatal(err)
	}
	if len(recipe.Artifacts) != 2 {
		t.Fatalf("artifacts did not unmarshal: got %d entries", len(recipe.Artifacts))
	}
	if recipe.BinaryName != "x" {
		t.Errorf("binary_name = %q, want %q", recipe.BinaryName, "x")
	}
	out, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != src {
		t.Errorf("per-arch recipe did not round-trip.\n got: %s\nwant: %s", out, src)
	}

	// The new keys must be MODELLED, not swept into Extra — an Extra key is
	// round-tripped but read by nothing, which is how per-recipe `arch` became
	// dead data (APP-RECIPE-STANDARD §1.1).
	for _, k := range []string{"artifacts", "binary_name"} {
		if _, ok := knownRecipeKeys[k]; !ok {
			t.Errorf("%q is not in knownRecipeKeys — it would land in Extra and be read by nothing", k)
		}
		if _, ok := recipe.Extra[k]; ok {
			t.Errorf("%q was captured into Extra instead of being modelled", k)
		}
	}
}
