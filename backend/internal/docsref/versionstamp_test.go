package docsref

import (
	"regexp"
	"strings"
	"testing"
)

// ─── The shipped binary must know its own version ────────────────────────────
//
// `cmd/server` declares `var Version = "dev"` and prints it for `--version`.
// Two things build that binary for shipping, and on 2026-08-19 only one of them
// stamped it:
//
//	Dockerfile:78   -ldflags="-s -w -X main.Version=${VERSION}"   OK  container
//	build.sh        -ldflags="-s -w"                              BAD .img.gz + rootfs
//
// So the container reported the release tag and the OS image reported `dev`.
// Two shipping artifacts of the same release disagreed about what they were.
//
// WHY IT SURVIVED, WHICH IS THE PART WORTH KEEPING. `.github/workflows/
// release.yml` has a step called "Verify tag matches binary version". It
// compiles its OWN throwaway binary with its own ldflags and checks that one.
// It never inspects a shipped artifact. A gate that builds its own subject
// cannot fail for the subject it is meant to guard — it was testing the Go
// toolchain's `-X` flag, which was never in doubt.
//
// This test reads the two build recipes instead, because they are what actually
// ships. It cannot prove the emitted binary is correct — only a check against a
// built artifact could — but it makes the omission that caused this impossible
// to reintroduce silently.
func TestShippedBuildsStampTheVersion(t *testing.T) {
	type recipe struct {
		file  string
		marks []string // a line must contain all of these to be the cmd/server build
	}
	recipes := []recipe{
		{file: "build.sh", marks: []string{"go build", "./cmd/server"}},
		{file: "Dockerfile", marks: []string{"go build", "cmd/server"}},
	}

	stamp := regexp.MustCompile(`-X\s+main\.Version=\S+`)

	for _, r := range recipes {
		src := readRepoFile(t, r.file)

		// Join continuation lines so a `\`-wrapped multi-line go build reads as
		// one. Without this the ldflags and the package path sit on different
		// lines and every match below silently fails to find its subject.
		joined := strings.ReplaceAll(src, "\\\n", " ")

		found, stamped := 0, 0
		for _, line := range strings.Split(joined, "\n") {
			trimmed := strings.TrimSpace(line)
			// Skip comments — this file's own explanatory text quotes the exact
			// flag, and a comment must never satisfy the assertion.
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			hasAll := true
			for _, m := range r.marks {
				if !strings.Contains(line, m) {
					hasAll = false
					break
				}
			}
			if !hasAll {
				continue
			}
			found++
			if stamp.MatchString(line) {
				stamped++
			}
		}

		// COVERAGE COUNT FIRST. If the build line moves or is reworded, `found`
		// goes to zero, every check below is vacuously satisfied, and this test
		// passes while guarding nothing — the exact failure it exists to prevent.
		if found == 0 {
			t.Fatalf("%s: found no non-comment `go build ... cmd/server` line. "+
				"The recipe has changed shape and this check has lost its subject; "+
				"it must not pass by finding nothing.", r.file)
		}
		if stamped != found {
			t.Errorf("%s: %d of %d `go build ... cmd/server` lines carry `-X main.Version=`.\n"+
				"An unstamped shipped binary reports `dev` regardless of the release tag, "+
				"and release.yml cannot catch it because that step compiles its own copy.",
				r.file, stamped, found)
		}
	}
}
