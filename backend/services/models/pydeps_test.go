package models

import (
	"strings"
	"testing"
)

// TestCheckPythonDeps_HonestReport asserts the deps check returns a coherent,
// honest report (Ready implies all sub-checks true; an install hint is always
// present). It does NOT assert a specific environment — it must work whether or
// not onnxruntime is installed in the test box.
func TestCheckPythonDeps_HonestReport(t *testing.T) {
	st := CheckPythonDeps()
	if st.InstallHint == "" {
		t.Fatal("install hint must always be present so the operator knows the fix")
	}
	if !strings.Contains(st.InstallHint, "onnxruntime") || !strings.Contains(st.InstallHint, "tokenizers") {
		t.Fatalf("install hint should name the required packages: %q", st.InstallHint)
	}
	// Ready is only true if every underlying dep is present (no false-positive
	// "ready" that would let the UI claim semantic when embed would fail).
	if st.Ready && !(st.Python3 && st.Onnxruntime && st.Tokenizers) {
		t.Fatalf("Ready must imply all deps present: %+v", st)
	}
	// If python3 is absent, nothing else can be true.
	if !st.Python3 && (st.Onnxruntime || st.Tokenizers || st.Ready) {
		t.Fatalf("no python3 but deps reported present: %+v", st)
	}
}

// TestListSurfacesCatalogAndDeps confirms List() now exposes the curated catalog
// and the python-deps status, and no longer claims NeedsRegistry.
func TestListSurfacesCatalogAndDeps(t *testing.T) {
	l, err := New(t.TempDir()).List()
	if err != nil {
		t.Fatal(err)
	}
	if l.NeedsRegistry {
		t.Fatal("NeedsRegistry should now be false (a curated catalog exists)")
	}
	if len(l.Catalog) == 0 {
		t.Fatal("List should surface the curated download catalog")
	}
	if l.PythonDeps.InstallHint == "" {
		t.Fatal("List should surface the python-deps install hint")
	}
}
