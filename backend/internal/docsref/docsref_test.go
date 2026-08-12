// Package docsref holds no code. It exists for one test: the documentation's
// references to this repository's source must still resolve.
//
// # Why this is worth a test
//
// The docs name concrete files — `backend/cmd/installer`, `internal/crdtsync/
// policy.go`, `scripts/smoke-relay.sh` — and a reader follows them to check a
// claim or to start work. A path that has been renamed out from under a doc
// fails silently: nothing builds it, nothing lints it, and the reader is simply
// told to look somewhere that is not there.
//
// The whole corpus resolves today. This is here so it stays that way, and
// because the sweep that established it needed three attempts to stop producing
// false positives — that work should not have to be redone from scratch the next
// time someone wonders.
package docsref

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// repoRoot is two levels above the backend module.
const repoRoot = "../../.."

// A backtick span that looks like a path to a source file in this repository.
var pathRe = regexp.MustCompile("`([A-Za-z0-9_.][A-Za-z0-9_./-]*\\.(?:go|ts|tsx|js|jsx|sh|sql))`")

// Where a cited path may be rooted. The docs write paths relative to whichever
// directory makes the sentence clearest — `cmd/server/main.go` inside a
// paragraph about the backend, `backend/cmd/server/main.go` when the context is
// the whole tree — and both are legitimate.
var roots = []string{
	"",
	"backend/",
	"frontend/",
	"frontend/src/",
	"backend/internal/",
	"backend/services/",
	"backend/cmd/",
}

// Documents that legitimately name paths which do not exist here.
//
//   - CHANGELOG.md is a HISTORICAL RECORD. It says what a release changed, and
//     files it named have since been renamed or deleted. Holding a changelog to
//     the current tree would demand rewriting history to keep a test green,
//     which is the opposite of what a changelog is for. Eight of its entries
//     name paths that no longer exist, correctly.
//   - SW-CACHE-VERSIONS.md coordinates service workers across SEVERAL repos and
//     prefixes each path with its repo (`vulos/frontend/public/sw.js`,
//     `diwan/public/sw.js`, lilmail's `assets/sw.js`). Only the first is ours,
//     and it resolves once the prefix is understood; the others are not this
//     repository's files at all.
var skipDocs = map[string]bool{
	"CHANGELOG.md":         true,
	"SW-CACHE-VERSIONS.md": true,

	// (NODE-CAPABILITY.md and TYPE-SAFETY.md used to be skipped here. They are
	// scanned now: their planned entries carry the reader-visible marker
	// isPlannedWork() recognises, so every OTHER citation in them is checked.)
}

func resolves(t *testing.T, cited string) bool {
	t.Helper()
	// The docs sometimes prefix a path with this repo's own name when writing
	// for an audience reading several repos at once.
	cited = strings.TrimPrefix(cited, "vulos/")
	for _, r := range roots {
		if _, err := os.Stat(filepath.Join(repoRoot, r+cited)); err == nil {
			return true
		}
	}
	return false
}

// describesRemoval reports whether a line presents its citations as things that
// no longer exist. Deliberately narrow: it looks for an explicit past-tense
// statement, so "removes the need for" or "deletes a file" in ordinary prose
// does not silently exempt a real citation.

// isPlannedWork reports whether a line cites a path as something still TO BE
// written rather than something that exists.
//
// An UNCHECKED checklist item is the unambiguous case — "- [ ]
// backend/services/files/peer_share.go — ShareFile(...)" is a task, and
// demanding the file exist would make it impossible to write down what to build
// next. A checked item ("- [x]") is a claim and stays checked.
func isPlannedWork(line string) bool {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "- [ ]") || strings.HasPrefix(t, "* [ ]") {
		return true
	}
	// A TABLE row cannot carry a checkbox, and roadmap documents describe
	// planned deliverables in tables constantly. The marker is deliberately
	// prose a READER sees — "…does not exist yet" — rather than an HTML comment
	// or a magic token: someone reading the rendered page should be able to tell
	// a plan from a description without knowing this test exists.
	return strings.Contains(strings.ToLower(t), "does not exist yet")
}

func describesRemoval(line string) bool {
	l := strings.ToLower(line)
	for _, phrase := range []string{
		"was deleted", "were deleted", "deleted on", "deleted 20",
		"was removed", "were removed", "removed on", "removed 20",
		"a former ", "the former ", "no longer exists", "since deleted",
	} {
		if strings.Contains(l, phrase) {
			return true
		}
	}
	return false
}

func markdownFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	// NOTE: roadmap/*.md is NOT scanned, and that is a known gap rather than a
	// decision. Adding it on 2026-08-12 surfaced 13 stale citations across
	// CLUSTER.md, FILES.md, NODE-CAPABILITY.md, OFFLINE-DATA.md, SYNC.md and
	// TYPE-SAFETY.md — several naming files that were deliberately deleted, such
	// as services/sync/hotpath.go. Turning the scan on means fixing or annotating
	// each one first; doing it in the same change would have meant committing a
	// red gate.
	for _, pat := range []string{"docs/*.md", "roadmap/*.md", "*.md"} {
		m, err := filepath.Glob(filepath.Join(repoRoot, pat))
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		out = append(out, m...)
	}
	if len(out) < 10 {
		t.Fatalf("found only %d markdown files under %s — the repo layout moved and this "+
			"test would pass by reading almost nothing", len(out), repoRoot)
	}
	return out
}

func TestDocsCiteSourcePathsThatExist(t *testing.T) {
	// Paths the docs state DO NOT EXIST are handled by the test below; they must
	// not be reported here, where they would read as broken references.
	absent := absentByDesign()

	cited, dead := 0, []string{}
	for _, f := range markdownFiles(t) {
		if skipDocs[filepath.Base(f)] {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range pathRe.FindAllStringSubmatch(line, -1) {
				p := m[1]
				if !strings.Contains(p, "/") || absent[p] {
					continue
				}
				// A citation of something the SAME SENTENCE says is gone is a
				// historical statement, not a claim the file exists — "a former
				// backend/services/store/store.go", "deleted on 2026-07-20",
				// "removed 2026-07-20". Demanding those files exist would force
				// the docs to stop naming what was removed and why, which is the
				// most useful thing they record.
				//
				// The same allowance already exists for endpoints
				// (apiref_test.go's documentedAsRemoved) and for environment
				// variables (envref_test.go); this brings cited PATHS into line
				// with both.
				if describesRemoval(line) || isPlannedWork(line) {
					continue
				}
				cited++
				if !resolves(t, p) {
					dead = append(dead, filepath.Base(f)+":"+strconv.Itoa(i+1)+" → "+p)
				}
			}
		}
	}

	// A regex that stopped matching would report nothing dead and mean nothing.
	if cited < 50 {
		t.Fatalf("only %d source paths were found across the docs; the extraction is broken, "+
			"so a clean result here would prove nothing", cited)
	}
	if len(dead) > 0 {
		t.Errorf("the docs point a reader at %d source path(s) that do not exist:\n  %s",
			len(dead), strings.Join(dead, "\n  "))
	}
}

// absentByDesign lists paths the documentation explicitly says are NOT in the
// repository. REPRODUCIBLE-BUILDS.md opens by naming them:
//
//	"`scripts/gen-manifest.sh`, `scripts/build-squashfs.sh`,
//	 `scripts/ci-reproducible.sh`, `make rootfs` and `make reproducible-build`
//	 **do not exist**."
func absentByDesign() map[string]bool {
	return map[string]bool{
		"scripts/gen-manifest.sh":    true,
		"scripts/build-squashfs.sh":  true,
		"scripts/ci-reproducible.sh": true,
	}
}

// The inverse invariant, and the one that guards a security claim.
//
// REPRODUCIBLE-BUILDS.md describes reproducible builds and opens by saying so
// plainly: "Status: this describes an intended design, not an enforced
// guarantee", naming three scripts that do not exist and stating that no CI job
// verifies release digests. That banner is the honest part of the document.
//
// If someone adds those scripts, the banner becomes wrong in the DANGEROUS
// direction: a reader would be told a security property is unenforced while the
// machinery to enforce it sits in the tree unused, or worse, would keep reading
// "not enforced" after it had quietly become enforced. Either way the document
// needs revisiting, and nothing else would notice.
func TestScriptsDocumentedAsAbsentAreStillAbsent(t *testing.T) {
	doc := filepath.Join(repoRoot, "docs/REPRODUCIBLE-BUILDS.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("REPRODUCIBLE-BUILDS.md is unreadable (%v); it is the document this test "+
			"exists to keep honest", err)
	}
	src := string(b)
	if !strings.Contains(src, "do not exist") {
		t.Fatalf("REPRODUCIBLE-BUILDS.md no longer says those scripts do not exist. If the " +
			"status of reproducible builds changed, update this test deliberately rather " +
			"than deleting it.")
	}

	for p := range absentByDesign() {
		if !strings.Contains(src, "`"+p+"`") {
			t.Errorf("%s is in this test's absent-by-design list but REPRODUCIBLE-BUILDS.md no "+
				"longer names it; the two have drifted apart", p)
			continue
		}
		if _, err := os.Stat(filepath.Join(repoRoot, p)); err == nil {
			t.Errorf("%s now EXISTS, but REPRODUCIBLE-BUILDS.md still tells the reader it does "+
				"not and that reproducible builds are unenforced. Reproducibility is a security "+
				"claim; the document has to be brought in line with the tree deliberately.", p)
		}
	}
}
