package docsref

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A systemd unit sitting in scripts/ that nothing installs is a claim about how
// the product runs, made by a file the product never reads.
//
// scripts/vulos.service was exactly that, and it cost something real. It set
// `User=vulos`, `Group=vulos`, `CapabilityBoundingSet=` and `AmbientCapabilities=`
// — a complete privilege-drop posture — for a server whose two shipped
// deployments both run as root:
//
//   - the container has no USER directive and debian:trixie-slim is uid 0;
//   - build.sh bakes its own vulos.service with no User=/Group= line at all
//     (build.sh:1322) and runs `/usr/local/bin/vulos-server -env prod`.
//
// The repo copy described neither, and was cited in roadmap/APP-LAUNCH-PATH.md
// as evidence that one of them dropped privilege. A reader checking the claim
// found a unit file saying exactly what they hoped, and no way to tell it was
// inert.
//
// It was not even the source for the deployment it did describe: install-vulos.sh
// writes the bundle's unit from an inline heredoc with ${VULOS_USER} and
// ${BIN_VULOS} expanded, and never reads scripts/vulos.service. The repo copy
// could only ever drift away from it.
//
// This gate does not require unit files to be installed. It requires that an
// uninstalled one is KNOWN to be uninstalled, in writing, here.
// # Run this with -count=1 when only scripts/ changed
//
// Go's test cache keys on the CONTENT of files the test opened, not on the
// listing of a directory it walked. Adding or removing a unit file therefore
// does not invalidate the cached result: while mutation-testing this gate,
// reintroducing scripts/vulos.service and adding a fresh orphan both reported
// `ok (cached)` and neither had actually run. The failures below are real, and
// were only visible with -count=1. CI runs cold so it sees them; a local
// `go test ./...` after editing only scripts/ may not.
func TestNoOrphanedSystemdUnits(t *testing.T) {
	const repoRoot = "../../.."

	// Units that nothing installs, each with the reason it is kept anyway.
	// Adding an entry is the point: it is cheap, and it forces the question
	// "does this file describe something that runs?" to be answered once.
	knownUnloaded := map[string]string{
		"vulos-bundle.target":   "self-host bundle; install-vulos.sh writes its own copy from an inline heredoc (UNIT_BUNDLE) and never reads this file",
		"vulos-fabric.service":  "self-host bundle; generated inline by install-vulos.sh (UNIT_FABRIC)",
		"vulos-diwan.service":   "self-host bundle; generated inline by install-vulos.sh (UNIT_DIWAN)",
		"vulos-lilmail.service": "self-host bundle; generated inline by install-vulos.sh (UNIT_LILMAIL)",
		"vulos-minio.service":   "self-host bundle; generated inline by install-vulos.sh (UNIT_MINIO)",
	}

	// A file counts as installed if an install-capable file names its path.
	// Prose in a .md does NOT count: docs/APPS.md names scripts/vulos-diwan.service
	// and installs nothing, which is the confusion this gate exists to prevent.
	installers := map[string]bool{".sh": true, ".mk": true, "Makefile": true, "Dockerfile": true}
	var installerText strings.Builder
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" || n == "out-arm64" || n == "output" {
				return filepath.SkipDir
			}
			return nil
		}
		if installers[filepath.Ext(path)] || installers[d.Name()] {
			if b, rerr := os.ReadFile(path); rerr == nil {
				installerText.Write(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	haystack := installerText.String()
	if len(haystack) < 100_000 {
		t.Fatalf("only %d bytes of installer text collected — this gate would pass by "+
			"reading nothing", len(haystack))
	}

	unitDir := filepath.Join(repoRoot, "scripts")
	entries, err := os.ReadDir(unitDir)
	if err != nil {
		t.Fatalf("read scripts/: %v", err)
	}

	var scanned, orphans []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch filepath.Ext(name) {
		case ".service", ".target", ".mount", ".timer", ".socket":
		default:
			continue
		}
		scanned = append(scanned, name)

		if strings.Contains(haystack, "scripts/"+name) {
			// Installed by something. If it is also registered as unloaded, the
			// register is now lying — that is a failure in the other direction.
			if _, listed := knownUnloaded[name]; listed {
				orphans = append(orphans, fmt.Sprintf(
					"%s is listed as never-installed but an installer names scripts/%s — "+
						"remove it from knownUnloaded", name, name))
			}
			continue
		}
		if _, listed := knownUnloaded[name]; !listed {
			orphans = append(orphans, fmt.Sprintf(
				"scripts/%s is a systemd unit that no .sh, Makefile or Dockerfile installs. "+
					"Either install it, delete it, or register it in knownUnloaded with the "+
					"reason it is kept — an inert unit file is read as a description of how "+
					"the product runs", name))
		}
	}

	// Coverage floor: this check's own failure mode is finding no unit files at
	// all — a moved directory or a changed suffix would leave it green forever.
	if len(scanned) < 5 {
		sort.Strings(scanned)
		t.Fatalf("scanned only %d unit files in scripts/ (%v) — expected at least 5. "+
			"If units moved, point this gate at the new location rather than lowering "+
			"the floor", len(scanned), scanned)
	}

	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Errorf("orphaned systemd units:\n  %s", strings.Join(orphans, "\n  "))
	}
}
