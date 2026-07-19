// Package datadir resolves the single on-disk root under which the box keeps
// all of its persistent state (databases, keys, peering material, app data).
//
// The location is operator-overridable via VULOS_DATA_DIR, which is documented
// in README.md and deploy/box/README.md as the way to point the box at a
// mounted volume. Before this package existed the root was hardcoded to
// $HOME/.vulos at every call site, so an operator who mounted a volume per the
// docs silently lost all state on restart.
//
// The default remains $HOME/.vulos so existing installs do not move.
package datadir

import (
	"os"
	"path/filepath"
)

// Root returns the absolute path of the box data directory.
//
// Resolution order:
//
//  1. VULOS_DATA_DIR, if set and non-empty (made absolute if relative).
//  2. $HOME/.vulos
//  3. /root/.vulos, when HOME is unset (e.g. a container with no HOME).
//
// Resolution is performed on each call so that a process which changes its
// environment (notably tests using t.Setenv) observes the current value.
func Root() string {
	return resolve()
}

func resolve() string {
	if d := os.Getenv("VULOS_DATA_DIR"); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Container without HOME set: match the historical fallback rather
		// than writing to a bare "/.vulos" at the filesystem root.
		return "/root/.vulos"
	}
	return filepath.Join(home, ".vulos")
}

// Join returns a path beneath the data root, e.g. Join("db", "auth.db").
func Join(elem ...string) string {
	return filepath.Join(append([]string{Root()}, elem...)...)
}
