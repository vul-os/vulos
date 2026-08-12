package procgroup_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No package may signal a process GROUP by pid without the reaped-process guard.
//
// The hazard is described in procgroup's package doc. This gate exists because
// fixing it one package at a time did not work: services/stream was cleaned up
// first and gained its own local check, and services/sandbox, services/webbrowser
// and internal/gpuhost were still doing it — including in services/webbrowser,
// the package CI's backend job dies on.
//
// Scoped to the whole backend rather than one directory for exactly that reason.
//
// EXEMPTIONS, each deliberate:
//   - internal/procgroup itself, which is the sanctioned implementation.
//   - services/appnet, whose App holds an *os.Process rather than an *exec.Cmd
//     and therefore has no ProcessState to consult. Guarding it needs a way to
//     know the process was reaped, which that type does not currently carry —
//     recorded here rather than silently skipped.
func TestNoUnguardedProcessGroupKills(t *testing.T) {
	exempt := map[string]string{
		"procgroup/procgroup.go": "the implementation",
		"appnet/launcher.go":     "holds *os.Process, no ProcessState available",
		"gpuhost/supervisor.go":  "guarded inline: ProcessState checked before Getpgid",
	}

	var offenders []string
	// "../.." is the BACKEND ROOT. ".." reaches only internal/, which is how the
	// first version of this gate missed services/webbrowser, services/sandbox and
	// services/stream entirely — every package the hazard actually lived in.
	// Caught by reintroducing an unguarded kill in webbrowser and watching this
	// test stay green.
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		for k := range exempt {
			if strings.HasSuffix(rel, k) {
				return nil
			}
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		lines := strings.Split(string(src), "\n")
		// A site wrapped in `if procgroup.ShouldSignal(cmd)` is guarded, and the
		// check has to see that or it flags correct code. Detected by looking
		// back a few lines rather than by adding the file to `exempt`: exempting
		// a whole FILE blinds this to every future kill added to it, which is how
		// a guard quietly stops covering its subject. A check that flags correct
		// lines gets switched off, so the lookback matters as much as the match.
		guarded := func(i int) bool {
			for j := i - 1; j >= 0 && j >= i-5; j-- {
				if strings.Contains(lines[j], "ShouldSignal(") {
					return true
				}
			}
			return false
		}
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue // prose about the pattern is not the pattern
			}
			// TWO shapes, not one. This check originally matched only
			// syscall.Kill(-pid) — process-GROUP kills — and reported thirteen
			// sites fixed, which read as complete. It was not: cmd.Process.Kill()
			// is a single-process SIGKILL carrying the identical hazard, and four
			// of those were sitting unguarded behind `if Process != nil`.
			//
			// That nil check is the trap. Process stays non-nil after Wait reaps
			// the process, so it passes precisely when the pid may already have
			// been recycled onto something else — and the SIGKILL lands on a
			// stranger. A guard covering one shape of a two-shape bug is worse
			// than none, because the count of fixed sites is read as coverage.
			if strings.Contains(line, "syscall.Kill(-") || strings.Contains(line, ".Process.Kill()") {
				if guarded(i) {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, trimmed))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("these signal a process GROUP by pid with no check that the child is "+
			"still running. Once a child is reaped the kernel may reuse its pid, and the "+
			"negative form then kills whatever group owns that number — use "+
			"procgroup.Signal:\n%s", strings.Join(offenders, "\n"))
	}
}
