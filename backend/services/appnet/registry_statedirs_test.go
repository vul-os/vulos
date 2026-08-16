package appnet

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// WHOSE JOB IS cache/ ?
//
// lilmail was observed serving DEGRADED — `scheduled send unavailable (store
// open failed)` — when no cache/ directory existed before launch. It still
// answered GET /login with 200, so every assertion short of reading the app's
// own log called that a pass.
//
// That left one question that prose cannot settle: does the INSTALLER create
// the directory, does the RECIPE, or does the APP? The two tests below answer
// it in code, in the two places the answer can change without anyone noticing:
// InstallFromRegistry's fixed directory set, and lilmail's shipped post_install.
// ─────────────────────────────────────────────────────────────────────────────

// TestInstallFromRegistry_CreatesOnlyTheBundleDirs pins the installer's side of
// the answer: it creates bin/, static/ and data/ — the strict bundle structure
// AppManifest documents — and NOTHING app-specific.
//
// It is not a restatement of the implementation. It is the assertion that makes
// the recipe's `mkdir -p cache sessions` load-bearing rather than defensive: if
// someone later "helpfully" adds cache/ to the installer's list, this goes red
// and whoever does it has to decide deliberately whether an app-specific
// runtime directory belongs in a loop that runs for all 56 entries.
func TestInstallFromRegistry_CreatesOnlyTheBundleDirs(t *testing.T) {
	withInsecureRegistry(t)

	body := []byte("#!/bin/sh\necho hi\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"statedirs": {
			Name: "State Dirs", Vetted: true, Type: "web",
			Description: "state dir test app", Category: "developer",
			Icon: "S", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					DownloadURL: srv.URL + "/app",
					Checksum:    sha256Hex(body),
					BinaryName:  "statedirs",
					Command:     "bin/statedirs",
					Port:        8080,
					// No post_install: this is the installer on its own.
				},
			},
		},
	}}

	if err := InstallFromRegistry(context.Background(), reg, "statedirs", "1.0", appsDir); err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}

	appDir := filepath.Join(appsDir, "statedirs")
	for _, want := range []string{"bin", "static", "data"} {
		st, err := os.Stat(filepath.Join(appDir, want))
		if err != nil || !st.IsDir() {
			t.Errorf("installer did not create the bundle directory %q: %v", want, err)
		}
	}
	// The two lilmail needs. If either of these ever exists here, the recipe's
	// mkdir has become dead code and the comment in registry.json is wrong.
	for _, notWanted := range []string{"cache", "sessions"} {
		if st, err := os.Stat(filepath.Join(appDir, notWanted)); err == nil && st.IsDir() {
			t.Errorf("the installer now creates %q for every app — decide that deliberately, "+
				"and delete the per-recipe mkdir it makes redundant", notWanted)
		}
	}
}

// TestShippedLilmailPostInstall_CreatesTheDirsItsConfigNames RUNS lilmail's
// shipped post_install and checks the outcome, rather than pattern-matching the
// string.
//
// Running it is the point. The last defect in this exact field was a quoting
// one — `\'` inside a single-quoted sh string ends the quote instead of
// escaping — which no substring check would have caught and which shipped an
// app whose every launch died with "Failed to load config". So the test asks
// sh, not a regexp.
//
// `chown` is stubbed to a no-op on PATH: the recipe chowns to uid 65534 (the
// uid appnet.Launcher drops to via setpriv), which no unprivileged test process
// on any platform can do. The stub keeps every OTHER part of the command real —
// the quoting, the secret generation, the mkdirs, the printf — and the chown
// itself is asserted textually below, where a textual check is the honest one.
func TestShippedLilmailPostInstall_CreatesTheDirsItsConfigNames(t *testing.T) {
	reg := shippedRegistry(t)
	entry, ok := reg.Apps["lilmail"]
	if !ok {
		t.Skip("lilmail is not in the shipped registry (entry removed?)")
	}
	recipe := entry.GetRecipe(entry.LatestVersion())
	if recipe == nil {
		t.Fatalf("lilmail has no recipe for its latest version %q", entry.LatestVersion())
	}
	if strings.TrimSpace(recipe.PostInstall) == "" {
		t.Fatal("lilmail's recipe has no post_install — its config, its secrets and its " +
			"runtime directories all come from there, so an empty one is a broken install")
	}

	appDir := t.TempDir()

	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "chown"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Same invocation InstallFromRegistry uses: sh -c, cwd = app dir, APP_DIR
	// and DATA_DIR in the environment.
	cmd := exec.Command("sh", "-c", recipe.PostInstall)
	cmd.Dir = appDir
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"APP_DIR="+appDir,
		"DATA_DIR="+filepath.Join(appDir, "data"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("lilmail's shipped post_install failed: %v\n%s", err, stderr.String())
	}

	// 1. the runtime directories exist — nothing else creates them
	for _, d := range []string{"cache", "sessions"} {
		st, err := os.Stat(filepath.Join(appDir, d))
		if err != nil || !st.IsDir() {
			t.Errorf("post_install did not create %q; without it lilmail runs DEGRADED "+
				"(\"scheduled send unavailable (store open failed)\") while still answering 200: %v", d, err)
		}
	}

	// 2. the config exists and names the cache dir that was actually created.
	//    A config pointing somewhere else is the same degradation with a
	//    directory sitting next to it looking innocent.
	raw, err := os.ReadFile(filepath.Join(appDir, "config.toml"))
	if err != nil {
		t.Fatalf("post_install wrote no config.toml: %v", err)
	}
	conf := string(raw)
	wantFolder := "folder = \"" + filepath.Join(appDir, "cache") + "\""
	if !strings.Contains(conf, wantFolder) {
		t.Errorf("config.toml's [cache].folder does not name the directory post_install created.\nwant a line: %s\ngot:\n%s", wantFolder, conf)
	}

	// 3. the two required secrets are real, distinct, and the length LoadConfig
	//    demands. lilmail's config.LoadConfig rejects an encryption key that is
	//    not 16/24/32 bytes, so a short one is a launch failure, and reusing one
	//    value for both would make the JWT secret recoverable from the
	//    ciphertext key.
	secret := firstSubmatch(t, conf, `(?m)^secret = "([^"]*)"$`, "[jwt].secret")
	key := firstSubmatch(t, conf, `(?m)^key = "([^"]*)"$`, "[encryption].key")
	if len(secret) != 32 {
		t.Errorf("[jwt].secret is %d characters, want 32", len(secret))
	}
	if len(key) != 32 {
		t.Errorf("[encryption].key is %d characters, want 32 (LoadConfig rejects 16/24/32-byte violations)", len(key))
	}
	if secret == key {
		t.Error("[jwt].secret and [encryption].key are the same value")
	}

	// 4. a second run must not reproduce the same secrets. If /dev/urandom were
	//    ever replaced by something deterministic, every box would ship the same
	//    JWT signing key.
	appDir2 := t.TempDir()
	cmd2 := exec.Command("sh", "-c", recipe.PostInstall)
	cmd2.Dir = appDir2
	cmd2.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"APP_DIR="+appDir2,
		"DATA_DIR="+filepath.Join(appDir2, "data"),
	)
	if err := cmd2.Run(); err != nil {
		t.Fatalf("second post_install run failed: %v", err)
	}
	raw2, err := os.ReadFile(filepath.Join(appDir2, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if secret == firstSubmatch(t, string(raw2), `(?m)^secret = "([^"]*)"$`, "[jwt].secret") {
		t.Error("two installs produced the SAME [jwt].secret — the secret is not being generated per box")
	}

	// 5. the chown the stub swallowed. The launcher runs the app as uid 65534
	//    with a scrubbed environment; config.toml is written 0640, so without
	//    this the app cannot read its own config on a real box.
	if !strings.Contains(recipe.PostInstall, "chown -R 65534:65534") {
		t.Error("post_install no longer chowns its state to 65534 — appnet.Launcher drops to that uid, " +
			"and config.toml is mode 640")
	}
	if !strings.Contains(recipe.PostInstall, "chmod 640 config.toml") {
		t.Error("post_install no longer restricts config.toml, which holds [jwt].secret and [encryption].key")
	}
}

func firstSubmatch(t *testing.T, s, pattern, what string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if len(m) < 2 {
		t.Fatalf("could not find %s in the generated config:\n%s", what, s)
	}
	return m[1]
}
