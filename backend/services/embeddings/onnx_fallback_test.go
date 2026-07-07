package embeddings

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

// The no-tokenizer fallback used to call Python's builtin hash(), which is
// SALTED PER PROCESS (PYTHONHASHSEED). That produced different token ids — and
// therefore different embedding vectors — for the same text on every restart,
// silently collapsing retrieval quality. These tests pin the replacement
// (_fallback_token_id, FNV-1a) to be DETERMINISTIC across processes and to match
// a canonical FNV-1a so the id function can never drift back to a random one.

// fallbackHarness wraps the real fallbackTokenizerPy with a tiny main so we can
// run the exact same source the embedder ships, independent of onnxruntime.
const fallbackHarness = fallbackTokenizerPy + `
import sys, json
words = json.loads(sys.stdin.read())
print(json.dumps([_fallback_token_id(w) for w in words]))
`

// runFallback runs the fallback tokenizer in a FRESH python3 process (its own
// hash seed) and returns the token ids for the given words.
func runFallback(t *testing.T, hashSeed string, words []string) []int {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping fallback-tokenizer determinism test")
	}
	in, _ := json.Marshal(words)
	cmd := exec.Command(py, "-c", fallbackHarness)
	cmd.Stdin = bytes.NewReader(in)
	// A DIFFERENT seed per call is exactly what broke the old hash()-based
	// tokenizer; a deterministic tokenizer must ignore it.
	cmd.Env = append(os.Environ(), "PYTHONHASHSEED="+hashSeed)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("python fallback tokenizer failed: %v\nstderr: %s", err, errBuf.String())
	}
	var ids []int
	if err := json.Unmarshal(out.Bytes(), &ids); err != nil {
		t.Fatalf("parse ids %q: %v", out.String(), err)
	}
	return ids
}

func TestFallbackTokenizerIsDeterministicAcrossProcesses(t *testing.T) {
	words := []string{"invoice", "due", "tigris", "$128.40", "renewal", "contract"}
	// Two SEPARATE processes with DIFFERENT hash seeds. With the old hash()
	// tokenizer these would disagree; with FNV-1a they must be identical.
	a := runFallback(t, "0", words)
	b := runFallback(t, "12345", words)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("fallback tokenizer is NOT deterministic across processes:\n seed0     = %v\n seed12345 = %v", a, b)
	}
	if len(a) != len(words) {
		t.Fatalf("got %d ids for %d words", len(a), len(words))
	}
}

func TestFallbackTokenizerMatchesCanonicalFNV1a(t *testing.T) {
	words := []string{"invoice", "due", "tigris", "hello", "world"}
	got := runFallback(t, "0", words)
	for i, w := range words {
		h := fnv.New32a()
		_, _ = h.Write([]byte(w))
		want := int(h.Sum32() % 30000)
		if got[i] != want {
			t.Errorf("id(%q) = %d, want FNV-1a %d", w, got[i], want)
		}
	}
}
