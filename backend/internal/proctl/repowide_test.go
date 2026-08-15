package proctl_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// backendRoot is the tree both tests below walk.
//
// ONE constant, shared, because the coverage check exists to prove the OFFENDER
// scan reaches services/ — and it can only prove that if it walks the same
// value. Written first as two separate literals, which mutation testing
// immediately exposed: narrowing the offender scan to ".." left the coverage
// test green, so the check that was supposed to catch exactly that defect was
// blind to it. That is the hollow-gate shape, found in the gate written to
// prevent hollow gates.
const backendRoot = "../.."

// No package outside proctl may signal a FOREIGN pid.
//
// internal/procgroup's repo-wide gate already covers two shapes:
// `syscall.Kill(-pid, …)` (process-group kills) and `cmd.Process.Kill()`. Both
// are about a pid this server SPAWNED, and both are guarded by
// procgroup.ShouldSignal, whose test is `cmd.ProcessState == nil`.
//
// This gate covers the third shape, which that one does not match and cannot
// guard: `syscall.Kill(pid, sig)` with a POSITIVE pid that came from somewhere
// other than an exec.Cmd — a /proc listing, an HTTP request body, a pidfile.
// There is no ProcessState for such a pid, so ShouldSignal is not merely
// unused there, it is unavailable, and a reviewer reaching for the existing
// guard finds it does not apply and signals anyway.
//
// The hazard is identical and slightly worse: a pid posted by a browser was
// read seconds earlier and is stale by construction, where a child's pid is at
// least fresh. proctl.Controller is the sanctioned implementation, and it
// requires the (pid, starttime) pair before it will signal anything.
//
// Verified capable of failing: adding `syscall.Kill(somePid, syscall.SIGKILL)`
// to backend/services/telemetry/telemetry.go makes this test report it.
func TestNoUnguardedForeignPidKills(t *testing.T) {
	// Only the implementation is exempt. Nothing else in the backend has any
	// business signalling a pid it did not spawn — verified: at the time this
	// gate was written the backend contained ZERO positive-pid kills, so the
	// list starts empty rather than starting with today's violations
	// grandfathered in, which is how a gate ships already defeated.
	exempt := map[string]string{
		"internal/proctl/proctl.go": "the sanctioned implementation",
	}

	// Matches syscall.Kill(<not a minus sign>. The negative form is the
	// process-GROUP kill that procgroup's gate owns; matching it here too
	// would double-report every site and train a reader to ignore both.
	kill := regexp.MustCompile(`syscall\.Kill\(\s*[^-\s)]`)

	var offenders []string
	// backendRoot is the same reach as procgroup's gate, and for the same
	// recorded reason: an earlier version of that test walked only internal/
	// and therefore missed every services/ package the bug actually lived in.
	err := filepath.WalkDir(backendRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(backendRoot)+"/")
		for k := range exempt {
			if strings.HasSuffix(rel, k) {
				return nil
			}
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			// Prose about the pattern is not the pattern. Both gates document
			// themselves at length and would otherwise flag their own comments.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if kill.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, trimmed))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("these signal a pid by number with no proof the pid still identifies the "+
			"intended process. A pid is only a process while that process lives; once it is "+
			"reaped the kernel may reuse the number, and the signal then lands on a stranger. "+
			"Use proctl.Controller, which requires the (pid, starttime) pair:\n%s",
			strings.Join(offenders, "\n"))
	}
}

// A gate that walks the wrong tree passes forever. procgroup's gate shipped
// with exactly that defect — "../" reached only internal/ — so this asserts the
// walk actually reaches services/, the directory most likely to acquire the
// bug, rather than trusting the relative path to be right.
func TestGateWalksTheWholeBackend(t *testing.T) {
	seen := 0
	_ = filepath.WalkDir(backendRoot, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(filepath.ToSlash(path), "/services/") &&
			strings.HasSuffix(path, ".go") {
			seen++
		}
		return nil
	})
	// A real number, not >0: a single stray file would satisfy >0 while the
	// walk was still failing to descend.
	if seen < 100 {
		t.Fatalf("the walk reached only %d .go files under services/ — it is not covering the backend", seen)
	}
}
