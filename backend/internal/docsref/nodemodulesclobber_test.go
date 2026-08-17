package docsref

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── The image build must not eat the host's node_modules ────────────────────
//
// THE DEFECT, measured 2026-08-17. scripts/baremetal-builder.Dockerfile is a
// LINUX builder and every harness runs it with the repo bind-mounted at /src.
// build.sh's frontend step then runs `npm ci` against that bind mount, so the
// install lands in the HOST's frontend/node_modules and replaces the
// platform-specific binaries with the container's Linux ones. On this macOS
// host the next person to touch the frontend got:
//
//	Cannot find module '@rolldown/binding-darwin-arm64'
//
// with frontend/node_modules/@rolldown holding only linux-arm64 bindings.
//
// WHY IT WAS INVISIBLE. It surfaces as a module-resolution crash at process
// START-UP, not as a failing assertion, so it reads as broken local tooling
// rather than as damage done by an unrelated image build — and the agent who
// ran the build is never the one who pays for it. build.sh carried a comment
// telling the reader to re-run `npm ci` on the host afterwards; nothing
// enforced it, and an instruction that has to be remembered 40 minutes later,
// by someone else, is not a control.
//
// THE FIX THIS PINS. Every caller passes `-v /src/frontend/node_modules`, an
// anonymous volume that shadows the bind mount so npm installs into a throwaway
// directory inside the container. `docker run --rm` discards it on exit and the
// host's tree is never written.
//
// WHY A TEST AND NOT JUST THE FLAG. The flag is per-call-site, so the failure
// mode is a NEW harness that copies the old shape. This test reads every shell
// script and workflow in the repo, finds each docker-run that both bind-mounts
// the source tree and runs build.sh, and requires the shadow on each one.

// dockerRunInvocation is one `docker run ...` command found in a file, already
// joined across its backslash continuations.
type dockerRunInvocation struct {
	file string
	line int
	text string
}

// findDockerRuns returns every docker-run invocation in path, with backslash
// continuations folded into a single string so the flags and the command body
// can be examined together.
func findDockerRuns(t *testing.T, path string) []dockerRunInvocation {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(b), "\n")
	var out []dockerRunInvocation
	for i := 0; i < len(lines); i++ {
		if !strings.Contains(lines[i], "docker run") {
			continue
		}
		start := i
		var sb strings.Builder
		for {
			cur := strings.TrimRight(lines[i], " \t")
			sb.WriteString(strings.TrimSuffix(cur, "\\"))
			sb.WriteString(" ")
			if !strings.HasSuffix(cur, "\\") || i+1 >= len(lines) {
				break
			}
			i++
		}
		out = append(out, dockerRunInvocation{file: path, line: start + 1, text: sb.String()})
	}
	return out
}

// bindMountsSource reports whether this invocation bind-mounts the repository
// itself (as opposed to a named cache volume). Both spellings in the tree are
// covered: `-v "$REPO":/src` in the scripts and `-v "$PWD":/src` in CI.
var bindMountsSource = regexp.MustCompile(`-v\s+"\$(REPO|PWD)":/src`)

// runsBuildSh reports whether the container's command actually invokes build.sh.
// The `-w /src/backend` invocations that run `go test` are deliberately NOT
// matched: they never run npm, so they cannot clobber anything.
var runsBuildSh = regexp.MustCompile(`\./build\.sh`)

// shadowsNodeModules is the fix: an anonymous volume at the node_modules path.
var shadowsNodeModules = regexp.MustCompile(`-v\s+/src/frontend/node_modules(\s|$)`)

// candidateFiles are the shell scripts and workflows that could host a build.
func candidateFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"scripts", ".github/workflows"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // a missing optional directory is not this test's business
			}
			if d.IsDir() {
				return nil
			}
			switch filepath.Ext(p) {
			case ".sh", ".yml", ".yaml":
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return out
}

func TestBuildShCallersShadowNodeModules(t *testing.T) {
	files := candidateFiles(t)
	if len(files) == 0 {
		t.Fatal("found no shell scripts or workflows to scan — the scan itself is broken")
	}

	checked := 0
	for _, f := range files {
		for _, inv := range findDockerRuns(t, f) {
			if !bindMountsSource.MatchString(inv.text) || !runsBuildSh.MatchString(inv.text) {
				continue
			}
			checked++
			if !shadowsNodeModules.MatchString(inv.text) {
				rel, _ := filepath.Rel(repoRoot, inv.file)
				t.Errorf("%s:%d — this docker run bind-mounts the source tree AND runs build.sh, "+
					"but does not pass `-v /src/frontend/node_modules`.\n"+
					"build.sh's `npm ci` will therefore install into the HOST's "+
					"frontend/node_modules and replace its platform-specific binaries with the "+
					"container's Linux ones (measured: `Cannot find module "+
					"'@rolldown/binding-darwin-arm64'`).\n"+
					"Add the anonymous volume so the container installs into a throwaway "+
					"directory instead.", rel, inv.line)
			}
		}
	}

	// Coverage assertion. Without this the test passes when the scan finds
	// nothing — a renamed directory, a changed quoting style, or a regex that
	// stopped matching would all report a clean bill of health for zero call
	// sites. There are five today (four smoke harnesses + the release workflow);
	// fewer than four means the scan, not the tree, has drifted.
	const minCallSites = 4
	if checked < minCallSites {
		t.Fatalf("only %d containerised build.sh call sites were examined, expected at least %d — "+
			"the scan has stopped finding them, so a green result here proves nothing. "+
			"Check bindMountsSource/runsBuildSh against the current invocation spelling.",
			checked, minCallSites)
	}
	// Only claim the clean bill of health if nothing above reported. t.Errorf
	// does not stop the function, so an unconditional success line here would
	// print "all shadow frontend/node_modules" directly beneath the failure
	// saying one of them does not — which is the shape of log that gets read
	// instead of the failure.
	if !t.Failed() {
		t.Logf("examined %d containerised build.sh call sites; all shadow frontend/node_modules", checked)
	}
}
