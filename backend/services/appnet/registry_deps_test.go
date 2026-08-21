package appnet

// registry_deps_test.go — DEPS-01 and DEPS-02.
//
// The line these tests exist for was:
//
//	if len(recipe.Deps) > 0 {
//	    packages.InstallDeps(ctx, recipe.Deps)   // error DISCARDED
//	}
//
// placed AFTER the payload had been downloaded and unpacked. It produced a
// successful install of an app that cannot exec, which is POSTINSTALL-01's
// defect one field down, and it did so on a box where the call could not have
// worked in the first place — build.sh clears the apt lists and nothing in the
// install path runs `apt-get update`.
//
// Measured 2026-08-17, debian:trixie-slim, lists cleared:
//
//	apt-get install -y --no-install-recommends liburing2  → exit 100,
//	                                        "E: Unable to locate package liburing2"
//	./conduwuit-linux-arm64 --version                     → "error while loading
//	                                        shared libraries: liburing.so.2"
//	(after installing liburing2)                          → "continuwuity 0.5.9"
//
// so conduit — a shipped, enabled entry declaring deps ["liburing2",
// "ca-certificates"] — installed "successfully" and could never start.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// depsTestRegistry builds a one-entry registry whose recipe declares deps and
// downloads a single binary from srvURL.
func depsTestRegistry(srvURL string, body []byte, deps []string) *Registry {
	return &Registry{Apps: map[string]*RegistryEntry{
		"depsapp": {
			Name: "Deps App", Vetted: true, Type: "web",
			Description: "deps test app", Category: "developer",
			Icon: "D", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					Artifacts: map[string]*Artifact{
						"amd64": {DownloadURL: srvURL + "/app", Checksum: sha256Hex(body)},
						"arm64": {DownloadURL: srvURL + "/app", Checksum: sha256Hex(body)},
					},
					BinaryName: "depsapp",
					Command:    "bin/depsapp",
					Port:       8080,
					Deps:       deps,
				},
			},
		},
	}}
}

// TestInstall_FailsWhenADeclaredDependencyIsMissing is DEPS-01 itself.
//
// The dependency name is one no distribution ships, so it is missing on every
// platform this suite runs on: with dpkg-query present it is "not installed",
// without it the answer is unknowable, and both are refusals.
func TestInstall_FailsWhenADeclaredDependencyIsMissing(t *testing.T) {
	withInsecureRegistry(t)

	body := []byte("#!/bin/sh\necho hi\n")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := depsTestRegistry(srv.URL, body, []string{"vulos-no-such-package-deps01"})

	err := InstallFromRegistry(context.Background(), reg, "depsapp", "1.0", appsDir)
	if err == nil {
		t.Fatal("DEPS-01 REGRESSION: an app whose declared dependency could not be satisfied " +
			"installed SUCCESSFULLY. Every launch of it dies in the dynamic loader, and the " +
			"App Hub shows it as installed.")
	}
	if !strings.Contains(err.Error(), "dependency check failed") {
		t.Errorf("the install failed for some other reason — the deps check may not be the one "+
			"answering: %v", err)
	}

	// The check runs BEFORE the download, for EXTRACT-01's reason (M10): a box
	// that fetches the payload and only then notices it can never run the
	// result has already paid for the mistake. An error message describes WHAT
	// happened and cannot describe WHEN, so this counts requests instead.
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("the payload was fetched %d time(s) before the dependency check refused — "+
			"the check has moved back behind the download", n)
	}

	// And nothing was left on disk. A half-built app directory is what
	// POSTINSTALL-01 removes; here it should never have been created.
	if _, err := os.Stat(filepath.Join(appsDir, "depsapp")); err == nil {
		t.Error("a refused install left an app directory behind")
	}
}

// TestInstall_SucceedsWhenTheDependenciesAreSatisfied is the CONTROL, and it is
// not optional: a dependency check that refused EVERYTHING would pass the test
// above while breaking conduit, diwan, lilmail and wede — the four shipped
// entries that declare deps, three of them the verified first-party ones.
//
// It goes through the seam because "this package is installed" is not portably
// expressible: ca-certificates is present on the shipped image and absent from
// a developer Mac, so asserting on a real package name would make the control
// a statement about the machine rather than about the installer.
func TestInstall_SucceedsWhenTheDependenciesAreSatisfied(t *testing.T) {
	withInsecureRegistry(t)

	var asked []string
	orig := verifyDeps
	verifyDeps = func(ctx context.Context, deps []string) error {
		asked = append(asked, deps...)
		return nil
	}
	defer func() { verifyDeps = orig }()

	body := []byte("#!/bin/sh\necho hi\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := depsTestRegistry(srv.URL, body, []string{"liburing2", "ca-certificates"})

	if err := InstallFromRegistry(context.Background(), reg, "depsapp", "1.0", appsDir); err != nil {
		t.Fatalf("an app whose dependencies ARE satisfied was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appsDir, "depsapp", "bin", "depsapp")); err != nil {
		t.Errorf("the install reported success but the binary is not there: %v", err)
	}
	// The check must actually have been handed the recipe's list. Without this
	// the control also passes when the call site is deleted outright, which is
	// precisely the mutation the test above is meant to kill.
	if len(asked) != 2 || asked[0] != "liburing2" || asked[1] != "ca-certificates" {
		t.Errorf("the dependency check was handed %v, want the recipe's own [liburing2 ca-certificates]", asked)
	}
}

// TestInstall_NoDepsStillInstalls is the second control. Most of the catalogue
// declares no deps at all; a check that refused an empty list would take the
// other 70 entries down with it.
func TestInstall_NoDepsStillInstalls(t *testing.T) {
	withInsecureRegistry(t)

	body := []byte("#!/bin/sh\necho hi\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := depsTestRegistry(srv.URL, body, nil)

	if err := InstallFromRegistry(context.Background(), reg, "depsapp", "1.0", appsDir); err != nil {
		t.Fatalf("an app with no declared dependencies was refused: %v", err)
	}
}

// TestShippedDepsAreImagePackages is the DEPS-02 half, and it is a data
// assertion rather than a behavioural one.
//
// Every package a shipped recipe names in `deps` must be one the image
// actually carries, because that is now the ONLY way the install can succeed —
// there is no apt call left to acquire it. The three package sets are pinned in
// scripts/image-packages.txt (the Dockerfile image) and
// scripts/build-sh-packages.txt (build.sh's `deploy` and `rootfs` sets), and a
// dep that is in one but not another is an app that installs on one target and
// refuses on the next.
func TestShippedDepsAreImagePackages(t *testing.T) {
	reg, err := LoadRegistry(shippedRegistryPath(t))
	if err != nil {
		t.Fatalf("cannot read the registry this gate exists to bound: %v", err)
	}

	sets := map[string]map[string]bool{
		"Dockerfile":      readPackageManifest(t, filepath.Join("..", "..", "..", "scripts", "image-packages.txt"), ""),
		"build.sh deploy": readPackageManifest(t, filepath.Join("..", "..", "..", "scripts", "build-sh-packages.txt"), "deploy"),
		"build.sh rootfs": readPackageManifest(t, filepath.Join("..", "..", "..", "scripts", "build-sh-packages.txt"), "rootfs"),
	}
	for label, set := range sets {
		if len(set) < 20 {
			t.Fatalf("the %s package set read back as %d entries — a gate reading an empty "+
				"list would report every dependency satisfied", label, len(set))
		}
	}

	declared := 0
	for appID, entry := range reg.Apps {
		if entry.Disabled {
			continue
		}
		for ver, recipe := range entry.Versions {
			if recipe == nil || recipe.Disabled || len(recipe.Deps) == 0 {
				continue
			}
			for _, dep := range recipe.Deps {
				declared++
				name, _, _ := strings.Cut(dep, "=")
				for label, set := range sets {
					if !set[name] {
						t.Errorf("%s@%s declares dep %q, which the %s package set does not carry.\n"+
							"`deps` is VERIFIED at install, never installed (DEPS-02): on a box where the "+
							"package is absent this app now REFUSES to install instead of installing and "+
							"failing to start. Add it to the image, or drop the dep.",
							appID, ver, name, label)
					}
				}
			}
		}
	}
	// The floor. Four shipped entries declare deps today — conduit, diwan,
	// lilmail, wede — for six declarations in total. If that reaches zero this
	// test starts passing by examining nothing, which is how a gate goes hollow.
	if declared < 6 {
		t.Fatalf("only %d dependency declarations were examined; four shipped entries carry six "+
			"between them. Either the registry lost them or this gate is reading the wrong field.", declared)
	}
}

// TestStoreInstall_DoesNotDiscardTheDependencyError covers the SECOND call
// site, the external-catalog path at AppStore.Install.
//
// It is a source assertion and it has to be. A behavioural test would need
// AppStore.Install to complete a real download, and its SSRF guard refuses
// loopback — so an httptest server, which is the only server a unit test can
// stand up, is unreachable by construction. That guard is correct and this
// test is not going to be the reason it gets a seam punched through it.
//
// The claim being made is about the SHAPE of the call — that its answer is
// used — so the check reads the shape. This is the same instrument
// TestInstallFromRegistry_ExecsNoInstallShell uses, for the same reason.
func TestStoreInstall_DoesNotDiscardTheDependencyError(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "func (s *AppStore) Install(")
	if start < 0 {
		t.Fatal("AppStore.Install not found in store.go — this guard is reading the wrong file")
	}
	rest := text[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of AppStore.Install")
	}
	var code []string
	for _, line := range strings.Split(rest[:end], "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code = append(code, line)
	}
	body := strings.Join(code, "\n")

	// Sanity: the guard must be looking at the real function.
	if !strings.Contains(body, "verifySHA256") || !strings.Contains(body, "safeExtractTarGz") {
		t.Fatalf("the extracted body does not contain the download and extract steps — "+
			"the guard is not reading code:\n%s", body)
	}

	if strings.Contains(body, "packages.InstallDeps") {
		t.Error("AppStore.Install still apt-installs the deps named by a DOWNLOADED, UNSIGNED " +
			"app.json. That is a package manager taking its argument list from the network.")
	}
	if !strings.Contains(body, "packages.VerifyDeps") {
		t.Fatal("AppStore.Install no longer checks the manifest's declared dependencies at all")
	}
	if !strings.Contains(body, "if err := packages.VerifyDeps(") {
		t.Error("DEPS-01 REGRESSION at the second call site: packages.VerifyDeps is called and its " +
			"answer is not bound to an error check, so a missing dependency reports a successful " +
			"install of an app that cannot start")
	}
}

// readPackageManifest reads one of the pinned apt package sets. `prefix` is the
// list name for build-sh-packages.txt ("deploy"/"rootfs") and empty for
// image-packages.txt, which is one bare package per line.
func readPackageManifest(t *testing.T, path, prefix string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if prefix == "" {
			out[line] = true
			continue
		}
		name, pkg, ok := strings.Cut(line, " ")
		if ok && name == prefix {
			out[strings.TrimSpace(pkg)] = true
		}
	}
	return out
}
