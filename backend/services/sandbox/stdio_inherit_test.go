package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No package that runs under `go test` may hand os.Stdout/os.Stderr to a
// long-lived child process.
//
// A child inherits the file descriptors it is given. Under `go test` those are
// the pipe the CI runner reads the step's output from. A child that outlives
// the test keeps that pipe open, the step's output never closes, and the runner
// SIGKILLs the step — surfacing as exit 137 AFTER every package has reported
// pass, which is why it read for a long time as "something kills the toolchain"
// rather than "a test leaked a process".
//
// services/sandbox spawns exactly this shape: a Python script server and a
// launcher, both with Setpgid, both previously wired to os.Stdout. This pins
// them.
//
// cmd/init is deliberately NOT covered by an equivalent rule: it is PID 1 on a
// real boot, writing to the console is its job, and it is //go:build linux so
// it never runs under go test on a developer machine.
func TestNoInheritedStdioOnSpawnedChildren(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	scanned := 0
	sawSandboxGo := false
	var offences []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		if name == "sandbox.go" {
			sawSandboxGo = true
		}
		for i, line := range strings.Split(string(src), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "//") {
				continue // a comment describing the rule must not violate it
			}
			if strings.Contains(t, "Stdout = os.Stdout") || strings.Contains(t, "Stderr = os.Stderr") {
				offences = append(offences, name+":"+itoa(i+1)+" "+t)
			}
		}
	}

	// Vacuity guard. The package holds exactly ONE non-test file today, so a
	// count floor would be 1 and prove almost nothing — a walk that silently
	// matched nothing would still clear it. Naming the file that carries the two
	// spawn sites is the real check: if sandbox.go is renamed or split, this
	// fails loudly rather than passing over an empty set.
	//
	// (The first version of this guessed a floor of 2 and failed immediately on
	// a one-file package. Counted, not guessed — the same mistake this suite has
	// made before with a vacuity floor set above the real number.)
	if !sawSandboxGo {
		t.Fatalf("sandbox.go was not scanned (%d files seen); the walk is broken and this "+
			"check would pass without examining the code it exists for", scanned)
	}

	if len(offences) > 0 {
		t.Errorf("a spawned child would inherit this process's stdio:\n  %s\n\n"+
			"Under `go test` that hands the child the CI runner's output pipe. If the child "+
			"outlives the test the pipe never closes and the runner SIGKILLs the step — exit "+
			"137 after every package has already reported pass. Use nil, or copy through a "+
			"pipe you close.", strings.Join(offences, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
