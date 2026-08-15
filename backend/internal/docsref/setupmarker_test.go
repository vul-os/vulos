package docsref

// setupmarker_test.go — one file means "this box has been set up", and exactly
// one thing may write it.
//
// The original defect had two halves. TestBuildDoesNotPreCompleteSetup (in
// kiosk_test.go) guards the first: a build must not ship the marker. This file
// guards the second: the marker must have a real writer, and it must be the
// single-purpose route rather than a shell command smuggled through /api/exec.
//
// That mattered because /api/exec is admin-gated AND kill-switchable
// (VULOS_DISABLE_EXEC → 503). A box whose operator had hardened it could never
// record that setup had finished, so the wizard re-ran on every boot — and could
// not succeed on the second run either, because the account it asks for already
// existed and register fails on a duplicate username.
//
// Both checks are source-level on purpose. The behaviour is covered by real
// tests (cmd/server/routes_setup_test.go drives the routes; the frontend's
// firstboot-* specs drive the shell); what those cannot see is the mechanism
// quietly acquiring a SECOND implementation somewhere else, which is exactly how
// the reader and the writer came to disagree in the first place.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const setupMarkerLiteral = "/var/lib/vulos/.setup-complete"

// TestSetupMarkerPathHasOneOwnerInGo asserts that the marker's absolute path is
// written down in exactly one non-test Go file.
//
// It is a magic path: the shell's whole first-boot decision hangs off whether
// this exact string exists on disk. A second os.Stat of a hand-typed copy is how
// a typo becomes "this box has never been set up" forever, and a second writer
// is how the two halves drift apart again.
func TestSetupMarkerPathHasOneOwnerInGo(t *testing.T) {
	var owners []string
	root := filepath.Join(repoRoot, "backend")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(b), setupMarkerLiteral) {
			rel, _ := filepath.Rel(root, path)
			owners = append(owners, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	want := "cmd/server/routes_setup.go"
	if len(owners) != 1 || owners[0] != want {
		t.Fatalf("the setup marker path %q is written in %v; it must appear in exactly "+
			"one non-test Go file (%s), which is where the status read and the "+
			"completion write share it. A second copy is a second answer to "+
			"\"has this box been set up?\"", setupMarkerLiteral, owners, want)
	}
}

// TestWizardMarksSetupThroughItsOwnRoute asserts that the setup wizard records
// completion by calling the route that exists for it, and does NOT go back to
// touching the marker through the general-purpose exec endpoint.
func TestWizardMarksSetupThroughItsOwnRoute(t *testing.T) {
	src := readRepoFile(t, "frontend/src/auth/Setup.tsx")

	if !strings.Contains(src, "'/api/setup/complete'") {
		t.Error("the setup wizard no longer posts to /api/setup/complete — whatever " +
			"marks the box set up now, it is not the single-purpose owner-gated " +
			"route, and the wizard may be back to writing the marker as a side " +
			"effect of something else")
	}

	// The specific regression: a shell command that creates the marker, sent
	// through an endpoint an operator is encouraged to disable.
	for i, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, ".setup-complete") {
			continue
		}
		if strings.Contains(line, "touch") || strings.Contains(line, "/api/exec") {
			t.Errorf("Setup.tsx line %d writes the setup marker through a shell "+
				"command:\n  %s\n\n/api/exec is admin-gated AND kill-switchable "+
				"(VULOS_DISABLE_EXEC → 503). On a box with exec disabled the marker "+
				"is never written, the wizard runs again on the next boot, and the "+
				"account step then fails on a duplicate username — a wizard the box "+
				"cannot leave. Use POST /api/setup/complete.",
				i+1, strings.TrimSpace(line))
		}
	}
}
