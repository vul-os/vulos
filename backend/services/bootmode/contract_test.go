package bootmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wire contract of GET /api/setup/mode, pinned on both sides of the
// language boundary.
//
// The endpoint's three values are produced in Go and consumed in TypeScript,
// and for as long as they have existed the two sides have disagreed about what
// they MEAN — the frontend read "normal" as "the owner has already set this box
// up" and dismissed the fifteen-step first-boot wizard into a login screen. A
// rename that lands on only one side would be the same failure with new words,
// so the strings are checked against the frontend file that mirrors them.

// frontendMirror is the TypeScript module that must carry the same strings.
const frontendMirror = "../../../frontend/src/lib/bootmode.ts"

// TestModesIsComplete fails if a mode string is added to Detect without being
// added to Modes(), which is what the cross-language check enumerates.
func TestModesIsComplete(t *testing.T) {
	src, err := os.ReadFile("bootmode.go")
	if err != nil {
		t.Fatalf("read bootmode.go: %v", err)
	}
	body := string(src)

	declared := map[string]bool{}
	for _, m := range Modes() {
		declared[m] = true
	}

	// Every Mode: constant assignment in the source must be in Modes().
	for _, want := range []string{ModeInstanceAbsent, ModeSyncing, ModeInstanceReady} {
		if !declared[want] {
			t.Errorf("Modes() omits %q", want)
		}
	}
	if len(Modes()) != 3 {
		t.Fatalf("Modes() has %d entries; update this test and the frontend mirror deliberately", len(Modes()))
	}

	// The old names must not come back. They are the ones that were misread.
	for _, banned := range []string{`Mode: "normal"`, `Mode: "setup"`, `Mode: "sync"`} {
		if strings.Contains(body, banned) {
			t.Errorf("bootmode.go still emits %s — see the package doc for why these names were retired", banned)
		}
	}
}

// TestModeStringsMatchFrontend pins the Go→TypeScript half of the contract.
func TestModeStringsMatchFrontend(t *testing.T) {
	path := filepath.Clean(frontendMirror)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the frontend mirror of this endpoint must exist", path, err)
	}
	ts := string(data)

	for _, mode := range Modes() {
		if !strings.Contains(ts, "'"+mode+"'") {
			t.Errorf("%s does not carry mode %q — Go emits a value the shell has never heard of", path, mode)
		}
	}

	// And the reverse: the mirror must not carry a value Go cannot emit. A
	// stale string there is what a test fixture picks up, and a fixture
	// asserting a state the box cannot reach is how this defect stayed green
	// through two separate causes (frontend/e2e/onboarding-walk.ts mocked
	// mode:'setup', which no running server has ever returned).
	//
	// Comments are stripped first, deliberately: the mirror's docstring quotes
	// the retired names and the code that misread them, and that history is the
	// most useful thing on the page. The check is about what the module
	// EXPORTS, which is what a fixture or a branch can pick up.
	for _, stale := range []string{"'normal'", "'setup'", "'sync'"} {
		if strings.Contains(stripTSComments(ts), stale) {
			t.Errorf("%s still references retired mode %s in code (comments are exempt)", path, stale)
		}
	}
}

// stripTSComments removes /* … */ and // … comments. Crude on purpose — it is
// used only to keep prose out of a string search, never to parse anything.
func stripTSComments(src string) string {
	var b strings.Builder
	for {
		start := strings.Index(src, "/*")
		if start < 0 {
			break
		}
		end := strings.Index(src[start:], "*/")
		if end < 0 {
			src = src[:start]
			break
		}
		src = src[:start] + src[start+end+2:]
	}
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
