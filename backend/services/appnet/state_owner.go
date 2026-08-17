package appnet

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// ─── STATE-01: who owns an installed app's state ─────────────────────────────
//
// THE DEFECT THIS CLOSES.  InstallFromRegistry creates the app directory and
// its bin/, static/ and data/ children as ROOT, mode 0755.  Launcher then runs
// the app as `setpriv --reuid=65534 --regid=65534` (appUID/appGID, nobody).  A
// 0755 root-owned data/ is READABLE by nobody and WRITABLE by nobody-else, so
// every app that keeps state — a database, an index, an uploaded file — installs
// cleanly and dies on its first write.  That is the "install succeeded, app
// cannot start" shape this catalogue keeps producing, one field further in.
//
// THE MODEL, stated once so it stops being re-derived per recipe:
//
//	bin/     root-owned, 0755  — CODE. The app may execute it and may not
//	                              rewrite it. An app that can overwrite its own
//	                              binary turns any bug in it into persistence.
//	static/  root-owned, 0755  — the served bundle. Same reasoning.
//	data/    APP-owned (65534) — STATE. The one directory the app writes.
//
// WHY IT IS HERE AND NOT IN SEVEN RECIPES.  Seven migrated entries carried
// `chown -R 65534:65534 data` in post_install, which is a stopgap repeated
// seven times: it is invisible in review, it is silently absent from the eighth
// entry, and it cannot be mutation-tested as a whole.  Worse, it is a
// PRIVILEGED command in a string that also runs on a developer box, where it
// fails — and POSTINSTALL-01 makes a failed post_install fatal, so those seven
// recipes could not install anywhere the installer was not root.
//
// WHY IT RUNS LAST.  post_install writes config files as root (gitea's app.ini,
// navidrome's toml), and the data-dir symlink step runs after that.  Handing
// ownership over before either would chown a directory and then fill it with
// root-owned files.  Running last means the tree is complete.
//
// WHY IT FOLLOWS THE SYMLINK.  On a fresh install data/ is REPLACED by a
// symlink into the owner's data directory, so the bytes the app writes do not
// live under appDir at all.  Chowning the link would set the mode of a symlink,
// which POSIX ignores.  The target tree is what has to change hands.

// appStateDir is the single subdirectory of an app directory that the app
// itself may write to.  Everything else it gets is read-only by design.
const appStateDir = "data"

// chownFn and geteuidFn are indirected so a test can observe exactly what the
// installer WOULD do on a real box.  No unprivileged test process on any
// platform can chown to uid 65534, and a test that skips the assertion when it
// cannot chown is a test that asserts nothing — this repo's dominant defect.
var (
	chownFn   = os.Lchown
	geteuidFn = os.Geteuid
)

// handOffStateDir gives the app's state directory to the uid the app runs as.
//
// It is a no-op when the installer is not root: chown(2) to another uid is a
// privileged operation, and `go test` / `make dev` are not root.  Skipping is
// safe there for the same reason it matters in production — an unprivileged
// installer runs the app as ITSELF (setpriv needs root too), so owner and app
// are already the same uid.
//
// Every other failure is returned.  A root installer that cannot hand over the
// state directory has produced an app that cannot write, and reporting success
// for that is the exact defect above.
func handOffStateDir(appDir string) error {
	stateDir := filepath.Join(appDir, appStateDir)
	if _, err := os.Lstat(stateDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("state dir %s: %w", stateDir, err)
	}
	// Resolve the symlink INSTALLED apps get: data/ points at the owner's data
	// directory, and that target is what the app writes into.
	target, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return fmt.Errorf("resolve state dir %s: %w", stateDir, err)
	}
	if geteuidFn() != 0 {
		log.Printf("[registry] not root (euid %d): leaving %s owned by the installing user — "+
			"an unprivileged installer also runs the app as itself, so owner and app match",
			geteuidFn(), target)
		return nil
	}
	return filepath.WalkDir(target, func(p string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := chownFn(p, appUID, appGID); err != nil {
			return fmt.Errorf("chown %s to %d:%d: %w", p, appUID, appGID, err)
		}
		return nil
	})
}
