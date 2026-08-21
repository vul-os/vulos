package docsref

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// ─── A stale duplicate beside the tool that rewrites it ──────────────────────
//
// On 2026-08-17 an untracked `backend/registry.json` was found in this tree: a
// byte-identical, stale copy of the real `registry.json`, which lives at the
// REPO ROOT.
//
// It sat in the one directory where it does damage. `backend/cmd/sign` defaults
// every `-registry` flag to the bare relative string "registry.json", and
// `backend/` is the documented cwd for those commands — the Makefile runs
// `cd $(BACKEND) && go run ./cmd/sign …`. The Makefile is careful and passes
// `-registry ../registry.json`, so `make sign-registry` was never at risk. A
// person debugging by hand types `go run ./cmd/sign sign-registry` without the
// flag, and with the duplicate present that signs and rewrites the COPY, then
// prints a success line naming the number of entries it signed. The shipping
// registry stays unsigned while the operator has just watched signing succeed.
//
// That is the failure this repository is worst at: an operation that reports
// success for a thing nobody ships. It is worse than an error, because the
// error would have been noticed.
//
// Nothing at RUNTIME reads this path — the server resolves `$VULOS_REGISTRY`
// (the Dockerfile sets `/opt/vulos/registry.json`) or `<appsDir>/../registry.json`
// — so this is not a live product defect. It is a trap laid for whoever next
// runs the signing ceremony by hand, which is a rare, high-stakes, manual
// operation performed under exactly the conditions where a wrong-file success
// goes unquestioned.
//
// `cmd/sign` now resolves and prints the ABSOLUTE path it acted on, so the same
// mistake would at least be legible in the output. This test removes the bait.
func TestNoStrayRegistryBesideTheSigner(t *testing.T) {
	// Directories where a `registry.json` must never appear, each because a
	// relative default would resolve to it. `backend/` is the documented cwd
	// for cmd/sign; `backend/cmd/sign/` is where someone might `cd` next.
	forbidden := []string{
		"backend",
		"backend/cmd/sign",
	}

	// COVERAGE COUNT. If repoRoot ever stops resolving, every os.Stat below
	// returns "not exist" and this test passes while checking nothing — the
	// exact shape it exists to catch. Prove the root is real by requiring the
	// canonical registry to be found at it.
	canonical := filepath.Join(repoRoot, "registry.json")
	canonicalBytes, err := os.ReadFile(shippedRegistryPath(t))
	if err != nil {
		t.Fatalf("cannot read the canonical registry at %s: %v\n"+
			"Without it this test cannot tell 'no stray copy exists' from "+
			"'I am looking in the wrong place', and would pass either way.",
			canonical, err)
	}
	if len(canonicalBytes) == 0 {
		t.Fatalf("%s is empty; this check has lost its subject", canonical)
	}
	canonicalSum := hex.EncodeToString(sha256Sum(canonicalBytes))

	checked := 0
	for _, dir := range forbidden {
		stray := filepath.Join(repoRoot, dir, "registry.json")
		checked++

		info, err := os.Stat(stray)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Errorf("stat %s: %v", stray, err)
			continue
		}

		// Report whether it is a stale copy or a divergent one. Both are
		// wrong; the divergent case is worse and should say so.
		detail := "unreadable"
		if b, rerr := os.ReadFile(stray); rerr == nil {
			if hex.EncodeToString(sha256Sum(b)) == canonicalSum {
				detail = "byte-identical to the canonical registry — a stale duplicate"
			} else {
				detail = "DIFFERENT from the canonical registry — it has already diverged"
			}
		}

		t.Errorf("%s exists (%d bytes, %s).\n"+
			"backend/cmd/sign defaults -registry to the relative \"registry.json\" and "+
			"backend/ is the documented cwd, so a hand-run `go run ./cmd/sign sign-registry` "+
			"would sign THIS file and report success while the shipped registry at %s "+
			"stayed unsigned. Delete it; the canonical registry lives at the repo root.",
			stray, info.Size(), detail, canonical)
	}

	if checked != len(forbidden) {
		t.Fatalf("checked %d of %d forbidden locations", checked, len(forbidden))
	}
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}
