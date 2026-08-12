package docsref

import (
	"regexp"
	"strings"
	"testing"
)

// The kiosk browser list exists TWICE and the copies must agree.
//
// findKioskBrowser in backend/cmd/init/main.go is the initramfs path's PID 1.
// /usr/local/bin/vulos-kiosk, written by build.sh, is the systemd path's — and
// an installed Vulos boots systemd, so that shell copy is the one a real
// machine with a monitor actually runs.
//
// Duplication was the pragmatic choice: extracting the Go logic would mean
// shipping another binary into the initramfs to serve a five-line search. The
// cost of that choice is drift, and drift here is silent in the worst way — a
// browser added to the Go list but not the script means the OS shows a blank
// screen on exactly the hardware the new entry was added for, while every test
// in the repository stays green.
//
// So the invariant is asserted instead: same browsers, same order.

var (
	// The candidate strings in findKioskBrowser's findBinary calls, bare names
	// only — the absolute /usr/bin/… variants are the same programs.
	goKioskRe = regexp.MustCompile(`findBinary\(\s*((?:"[^"]+",?\s*)+)\)`)
	// `for cand in cog chromium chromium-browser; do`
	shKioskRe = regexp.MustCompile(`for cand in ([a-z0-9 _-]+); do`)
)

func kioskBrowsersFromGo(t *testing.T) []string {
	t.Helper()
	src := readRepoFile(t, "backend/cmd/init/main.go")

	start := strings.Index(src, "func findKioskBrowser()")
	if start < 0 {
		t.Fatal("findKioskBrowser is gone from backend/cmd/init/main.go; this check has lost its subject")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of findKioskBrowser")
	}
	body := src[start : start+end]

	var out []string
	seen := map[string]bool{}
	for _, m := range goKioskRe.FindAllStringSubmatch(body, -1) {
		for _, raw := range strings.Split(m[1], ",") {
			name := strings.Trim(strings.TrimSpace(raw), `"`)
			// Absolute paths name the same binaries as the bare candidates.
			if name == "" || strings.HasPrefix(name, "/") {
				continue
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no browser candidates parsed out of findKioskBrowser; the matcher is broken " +
			"and would report agreement with an empty list")
	}
	return out
}

func kioskBrowsersFromShell(t *testing.T) []string {
	t.Helper()
	src := readRepoFile(t, "build.sh")
	m := shKioskRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("build.sh no longer writes a `for cand in …` browser search into " +
			"/usr/local/bin/vulos-kiosk; the systemd path has no kiosk")
	}
	return strings.Fields(m[1])
}

func TestKioskBrowsersMatchInit(t *testing.T) {
	goList := kioskBrowsersFromGo(t)
	shList := kioskBrowsersFromShell(t)

	if strings.Join(goList, " ") != strings.Join(shList, " ") {
		t.Errorf("the two kiosk browser lists disagree.\n"+
			"  backend/cmd/init/main.go (initramfs PID 1): %v\n"+
			"  build.sh → /usr/local/bin/vulos-kiosk (systemd): %v\n"+
			"An installed box boots systemd, so the second list is what a machine with a "+
			"monitor actually runs. A browser present in one and not the other shows a "+
			"blank screen on the hardware it was added for, with every test still green.",
			goList, shList)
	}
}

// A display must get the OS, not a login prompt. The two units are mutually
// exclusive on the SAME condition, so exactly one runs on any given box.
func TestKioskAndConsoleAreMutuallyExclusiveOnADisplay(t *testing.T) {
	src := readRepoFile(t, "build.sh")

	if !strings.Contains(src, "ConditionPathExistsGlob=/dev/dri/card*") {
		t.Error("vulos-kiosk.service is no longer conditioned on a DRM device existing; " +
			"a headless box would spend every boot retrying a compositor it cannot start")
	}
	if !strings.Contains(src, "ConditionPathExistsGlob=!/dev/dri/card*") {
		t.Error("vulos-console.service is no longer conditioned on the ABSENCE of a DRM " +
			"device, so a box with a monitor can show the text status screen instead of " +
			"the OS — which is the defect this pair was written to remove")
	}
	if !strings.Contains(src, "systemctl enable vulos-kiosk.service") {
		t.Error("vulos-kiosk.service is written but never enabled, so it never starts")
	}
}
