package stream

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every pkill in this repository must be anchored with -x.
//
// pkill treats its pattern as an UNANCHORED regex over the process name. So
// `pkill -9 sh` matches bash, dash and ssh, and `pkill -9 go` would match
// gopls and google-chrome. The pattern here is filepath.Base of a configured
// command, so its value is not fixed at compile time and cannot be reasoned
// about locally — any command whose basename is a substring of another running
// process's name turns a cleanup into a machine-wide kill.
//
// This is not hypothetical. pool.go reached out from a monitoring goroutine
// about three seconds after an app exited quickly and SIGKILLed the shell
// running the CI test step: one process dead with no warning signal, its
// children left as orphans, and the victim package differing between runs
// because the kill landed wherever go test had reached by then. It presented as
// "something kills the Go toolchain", cost most of a day, and survived eight
// rounds of hypotheses because every property it showed — no OOM, no resource
// pressure, not reproducible locally, not a fixed time or count or package —
// is exactly what a stray pkill from an unrelated goroutine looks like.
//
// The whole cost of that day was one missing flag.
func TestPkillIsAnchored(t *testing.T) {
	scanned := 0
	var offences []string

	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sh") {
			return nil
		}
		if strings.HasSuffix(path, "pkill_anchored_test.go") {
			return nil // this file describes the pattern; it does not use it
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanned++
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
				continue // prose about pkill is not a call to pkill
			}
			if !strings.Contains(trimmed, "pkill") {
				continue
			}
			// -x anchors the name; -f matches the full command line, which is
			// specific enough to be deliberate rather than accidental.
			if strings.Contains(trimmed, "\"-x\"") || strings.Contains(trimmed, "-x ") ||
				strings.Contains(trimmed, "-f ") || strings.Contains(trimmed, "\"-f\"") {
				continue
			}
			offences = append(offences, rel+":"+itoa(i+1)+" "+trimmed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Vacuity guard, counted rather than guessed: the walk covers the whole
	// backend plus scripts, which is hundreds of files. A broken walk would
	// report success having read almost nothing.
	if scanned < 100 {
		t.Fatalf("only %d files scanned; the walk is broken and this check would "+
			"pass without examining the repository", scanned)
	}

	if len(offences) > 0 {
		t.Errorf("unanchored pkill — matches any process whose name CONTAINS the pattern:\n  %s\n\n"+
			"Add -x (exact name) or -f (full command line). `pkill sh` kills bash; this exact "+
			"defect SIGKILLed the CI step's shell from a background goroutine and read as "+
			"'something kills the Go toolchain' for a day.", strings.Join(offences, "\n  "))
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
