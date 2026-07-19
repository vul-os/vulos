// datadir_test.go — VULOS_DATA_DIR is documented in README.md and
// deploy/box/README.md as the way to point the box at a mounted volume. It
// previously had no reader at all: the data root was hardcoded to $HOME/.vulos,
// so an operator who mounted a volume per the docs silently lost state on every
// restart. These tests pin the documented behaviour.
package datadir

import (
	"path/filepath"
	"testing"
)

func TestResolve_HonoursVulosDataDir(t *testing.T) {
	t.Setenv("VULOS_DATA_DIR", "/mnt/vulos-data")
	if got := resolve(); got != "/mnt/vulos-data" {
		t.Errorf("VULOS_DATA_DIR ignored: got %q, want /mnt/vulos-data", got)
	}
}

func TestResolve_MakesRelativeOverrideAbsolute(t *testing.T) {
	t.Setenv("VULOS_DATA_DIR", "relative-data")
	got := resolve()
	if !filepath.IsAbs(got) {
		t.Errorf("relative VULOS_DATA_DIR not made absolute: %q", got)
	}
	if filepath.Base(got) != "relative-data" {
		t.Errorf("unexpected resolution of relative override: %q", got)
	}
}

func TestResolve_DefaultsToHomeVulos(t *testing.T) {
	// Existing installs must not move when the override is unset.
	t.Setenv("VULOS_DATA_DIR", "")
	t.Setenv("HOME", "/home/tester")
	want := filepath.Join("/home/tester", ".vulos")
	if got := resolve(); got != want {
		t.Errorf("default data dir moved: got %q, want %q", got, want)
	}
}

func TestResolve_FallsBackWhenHomeUnset(t *testing.T) {
	t.Setenv("VULOS_DATA_DIR", "")
	t.Setenv("HOME", "")
	got := resolve()
	if got == "/.vulos" {
		t.Fatal("resolved to bare /.vulos at the filesystem root")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("non-absolute fallback: %q", got)
	}
}

func TestJoin_IsBeneathRoot(t *testing.T) {
	got := Join("db", "auth.db")
	want := filepath.Join(Root(), "db", "auth.db")
	if got != want {
		t.Errorf("Join = %q, want %q", got, want)
	}
}
