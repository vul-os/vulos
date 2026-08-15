package docsref

// imagebins_test.go — IMAGEBIN-01.
//
// Every external binary a built-in feature execs must be present in the image
// that feature ships in.
//
// # The defect class this exists for
//
// This is the THIRD instance in one session of the same pattern: verified in
// Docker, absent on bare metal.
//
//  1. /opt/vulos/apps shipped as an empty directory — build.sh copied from a
//     path that had moved, so v0.1.0 and v0.2.0 shipped zero apps (dbebd593).
//  2. A `User=vulos` systemd unit that does not apply on the init path, where
//     cmd/init/main.go is PID 1.
//  3. THIS one: services/webbrowser/chrome.go execs chromium, the Dockerfile
//     installed it, and the debootstrap rootfs never did. The released
//     v0.2.0-arm64 rootfs contains ./usr/bin/cog and ./usr/bin/cogctl and no
//     browser at all, so the built-in browser answered
//     500 {"error":"chromium not found"} on every bare-metal box while working
//     perfectly in every container anyone tested in.
//
// A fourth is likely, and no check that runs against the Docker image can see
// any of them — Docker has the binary, so the test passes and the product is
// still broken. This check reads the SOURCE against the ROOTFS PACKAGE LIST, so
// the container it happens to run in is irrelevant.
//
// # What it proves, and what it does not
//
// It proves that every binary named in the scanned services is accounted for:
// either its providing package is in build.sh's rootfs list, or it is recorded
// below with an explicit reason for not being there. It does NOT prove the
// package actually contains that binary — that would need a real apt run, which
// scripts/prove-portbind.sh-style container proofs cover separately.
//
// It is deliberately hostile to being satisfied by nothing: an unknown binary
// is a hard failure that names the binary and demands a classification, and the
// scan asserts a minimum count of both binaries and scanned files.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// scannedServiceDirs are the packages whose exec'd binaries must exist in the
// bare-metal image. These back built-in, always-present features — the streamed
// browser and the stream pool that every desktop app rides on — as opposed to
// registry apps, which declare their own deps and are installed on demand.
var scannedServiceDirs = []string{
	"services/webbrowser",
	"services/stream",
	"services/desktop",
}

// binPackage maps an exec'd binary to the Debian package that provides it in
// the bare-metal rootfs.
var binPackage = map[string]string{
	"chromium":                "chromium",
	"Xvfb":                    "xvfb",
	"xdotool":                 "xdotool",
	"xrandr":                  "x11-xserver-utils",
	"matchbox-window-manager": "matchbox-window-manager",
	"cage":                    "cage",
	"pulseaudio":              "pulseaudio",

	"gst-launch-1.0": "gstreamer1.0-tools",
}

// binExempt records binaries that are deliberately NOT guaranteed in the image,
// with the reason. An entry here is a claim that the feature degrades honestly
// without the binary — not a way to silence the check.
var binExempt = map[string]string{
	// findBin() tries several names and takes the first that resolves. Only one
	// has to ship; these are the alternates that a Debian rootfs will not have.
	"chromium-browser":          "alternate name findBin tries; Debian's package installs /usr/bin/chromium",
	"/usr/bin/chromium-browser": "absolute-path alternate of the same findBin call",
	"google-chrome":             "proprietary; deliberately NOT redistributed inside the image (chromium is)",
	"google-chrome-stable":      "proprietary; same as google-chrome",

	// Provided by packages already pulled in transitively or by the base system.
	"pkill": "procps, part of the debootstrap base system",
}

var (
	reExecCommand = regexp.MustCompile(`exec\.Command(?:Context)?\((?:ctx,\s*)?"([a-zA-Z0-9_.\-/]+)"`)
	reLookPath    = regexp.MustCompile(`lookPath\("([a-zA-Z0-9_.\-/]+)"\)`)
	reFindBin     = regexp.MustCompile(`findBin\(([^)]*)\)`)
	reQuoted      = regexp.MustCompile(`"([^"]+)"`)
)

// rootfsPackages reads the rootfs half of the pinned package manifest that
// scripts/check-image-packages.sh keeps in sync with build.sh.
//
// Reading the MANIFEST rather than build.sh is deliberate: the manifest is
// generated from build.sh and verified against it by that script, so it is the
// same fact in a form that cannot be misparsed — and if the two ever diverge,
// that script fails, not this one silently passing on a stale copy.
func rootfsPackages(t *testing.T) map[string]bool {
	t.Helper()
	path := repoPath(t, "scripts/build-sh-packages.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pkgs := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "rootfs" {
			pkgs[fields[1]] = true
		}
	}
	if len(pkgs) < 30 {
		t.Fatalf("only %d rootfs packages parsed from %s — the manifest format changed "+
			"and this check would pass by comparing against almost nothing", len(pkgs), path)
	}
	return pkgs
}

// repoPath resolves a repo-relative path from this test's directory
// (backend/internal/docsref).
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "..", rel)
}

type binRef struct {
	bin  string
	file string
}

// scanExecdBinaries walks the scanned service dirs and collects every literal
// binary name handed to exec.Command / lookPath / findBin.
func scanExecdBinaries(t *testing.T) ([]binRef, int) {
	t.Helper()
	seen := map[string]string{}
	filesScanned := 0
	perDir := map[string]int{}

	for _, dir := range scannedServiceDirs {
		full := repoPath(t, filepath.Join("backend", dir))
		entries, err := os.ReadDir(full)
		if err != nil {
			t.Fatalf("read %s: %v — the scanned service dirs are out of date, and a "+
				"scan that examines nothing proves nothing", full, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(full, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			filesScanned++
			perDir[dir]++
			src := string(data)
			rel := filepath.Join(dir, name)

			for _, m := range reExecCommand.FindAllStringSubmatch(src, -1) {
				if _, ok := seen[m[1]]; !ok {
					seen[m[1]] = rel
				}
			}
			for _, m := range reLookPath.FindAllStringSubmatch(src, -1) {
				if _, ok := seen[m[1]]; !ok {
					seen[m[1]] = rel
				}
			}
			for _, m := range reFindBin.FindAllStringSubmatch(src, -1) {
				for _, q := range reQuoted.FindAllStringSubmatch(m[1], -1) {
					if _, ok := seen[q[1]]; !ok {
						seen[q[1]] = rel
					}
				}
			}
		}
	}

	// Per-directory coverage: a total count alone would stay green if one
	// service were renamed away while another grew. Every scanned dir must have
	// contributed at least one file.
	for _, dir := range scannedServiceDirs {
		if perDir[dir] == 0 {
			t.Fatalf("%s contributed 0 scannable files. A service that backs a built-in "+
				"feature has moved or been renamed, and this check is now silently "+
				"ignoring it — which is exactly how the browser shipped without "+
				"chromium.", dir)
		}
	}

	refs := make([]binRef, 0, len(seen))
	for b, f := range seen {
		refs = append(refs, binRef{bin: b, file: f})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].bin < refs[j].bin })
	return refs, filesScanned
}

// TestEveryExecdBinaryShipsInTheImage is the gate.
func TestEveryExecdBinaryShipsInTheImage(t *testing.T) {
	refs, filesScanned := scanExecdBinaries(t)
	rootfs := rootfsPackages(t)

	// COVERAGE ASSERTIONS — this check's own failure mode is examining nothing.
	// A regex that stops matching, a directory that is renamed, or a build tag
	// that hides every file would all produce a clean, meaningless pass.
	// 9 is the count today (webbrowser 1, stream 7, desktop 1). Pinned at the
	// real number rather than a comfortable low bar, so a file that stops being
	// scanned — a build tag, a rename, a moved package — fails here instead of
	// quietly shrinking the check.
	if filesScanned < 9 {
		t.Fatalf("scanned only %d source files across %v, expected at least 9 — the scan "+
			"is no longer reaching the code it is supposed to check", filesScanned, scannedServiceDirs)
	}
	if len(refs) < 8 {
		t.Fatalf("found only %d exec'd binaries across %d files. The extraction has "+
			"stopped matching; a pass here would mean nothing.\nfound: %v",
			len(refs), filesScanned, refs)
	}

	var missing, unknown []string
	for _, r := range refs {
		if reason, ok := binExempt[r.bin]; ok {
			if reason == "" {
				t.Errorf("%s is exempt with an EMPTY reason — an exemption without a "+
					"reason is a silenced check", r.bin)
			}
			continue
		}
		pkg, ok := binPackage[r.bin]
		if !ok {
			unknown = append(unknown, fmt.Sprintf("%s (exec'd in %s)", r.bin, r.file))
			continue
		}
		if !rootfs[pkg] {
			missing = append(missing, fmt.Sprintf(
				"%s (exec'd in %s) needs package %q, which is NOT in build.sh's rootfs list",
				r.bin, r.file, pkg))
		}
	}

	if len(unknown) > 0 {
		t.Errorf("binaries with no classification:\n  %s\n\n"+
			"Every binary a built-in feature execs must be either mapped to the "+
			"package that provides it (binPackage) or exempted WITH A REASON "+
			"(binExempt). Leaving one unclassified is how the browser shipped "+
			"without chromium for two releases.", strings.Join(unknown, "\n  "))
	}
	if len(missing) > 0 {
		t.Errorf("built-in features exec binaries the bare-metal image does not ship:\n  %s\n\n"+
			"The Dockerfile having them is not enough. This is the third time: "+
			"/opt/vulos/apps shipped empty, a systemd User= that the init path "+
			"ignores, and chromium missing from the rootfs — all of them worked "+
			"in Docker and all of them were broken on a real box.",
			strings.Join(missing, "\n  "))
	}
}

// TestBrowserBinaryIsInTheImage names the specific regression, so a failure
// says what broke rather than only that something did.
func TestBrowserBinaryIsInTheImage(t *testing.T) {
	rootfs := rootfsPackages(t)
	if !rootfs["chromium"] {
		t.Fatal("the bare-metal rootfs does not install chromium, but " +
			"services/webbrowser/chrome.go's findBin looks for it and returns " +
			`"chromium not found" otherwise. The built-in browser is dead on ` +
			"every bare-metal box. cog does not substitute: it is a single-surface " +
			"WPE kiosk shell with no tabs, address bar or profile.")
	}
	// The stream pool uses cage only when the GPU tier is not software; every
	// other box takes the Xvfb path, which is the common bare-metal case.
	if !rootfs["xvfb"] {
		t.Fatal("the rootfs has no xvfb. services/stream/pool.go uses cage ONLY when " +
			"gpuInfo.Tier != TierSoftware and falls back to Xvfb otherwise — which " +
			"is most bare-metal boxes. Without it no streamed app gets a display.")
	}
	if !rootfs["xdotool"] {
		t.Fatal("the rootfs has no xdotool. It is the X11 input injector's fallback " +
			"when uinput is unavailable (stream/pool.go) and the VNC path's only " +
			"injector (stream/vnc.go). Without it a streamed window renders but " +
			"cannot be typed into.")
	}
}

// TestNoProprietaryBrowserInTheImage. Chromium is open source and ships;
// Chrome is proprietary and must not be redistributed inside the OS image.
// Adding it would be a licensing decision, not a packaging one.
func TestNoProprietaryBrowserInTheImage(t *testing.T) {
	rootfs := rootfsPackages(t)
	for _, p := range []string{"google-chrome", "google-chrome-stable", "chrome"} {
		if rootfs[p] {
			t.Errorf("the rootfs installs %q. Chrome is proprietary; redistributing it "+
				"inside a Vulos image is a licensing matter. Chromium is the open-source "+
				"build and is what findBin prefers.", p)
		}
	}
}
