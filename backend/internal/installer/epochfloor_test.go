//go:build linux

package installer

import (
	"os"
	"path/filepath"
	"testing"
)

// readEpochFloor's contract, and why the two error cases are not the same.
//
// Floor 0 does not mean "no opinion" — it means "accept every retired release
// key". This function used to be readEpochFloorBestEffort and returned 0 on ANY
// error, so a device whose epoch record had been corrupted silently accepted
// certs the root had revoked, on the path that decides which OS gets written to
// the disk. It was the odd one out in its own caller: the manifest, its parse,
// its required fields and its signature all refuse when missing.
//
// ABSENT must stay non-error: a device that has never accepted a release cert
// genuinely has no record, and refusing there would make a first install
// impossible. That asymmetry is the whole design, so both halves are pinned —
// a change that made absent fail, or corrupt succeed, breaks one of these.

func TestReadEpochFloor_AbsentRecordIsFloorZeroNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch.json") // deliberately not created

	got, err := readEpochFloor(path)
	if err != nil {
		t.Fatalf("readEpochFloor(absent) = %v, want no error — a device that has never accepted a cert must still be installable", err)
	}
	if got != 0 {
		t.Fatalf("readEpochFloor(absent) = %d, want 0", got)
	}
}

func TestReadEpochFloor_ReadsARecordedFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch.json")
	if err := os.WriteFile(path, []byte(`{"floor":7}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readEpochFloor(path)
	if err != nil {
		t.Fatalf("readEpochFloor = %v, want nil", err)
	}
	if got != 7 {
		t.Fatalf("readEpochFloor = %d, want 7 — a floor that does not survive the read revokes nothing", got)
	}
}

func TestReadEpochFloor_CorruptRecordFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch.json")
	// Present, and not parseable. This is how a local attacker resets a floor:
	// truncating or scribbling on one small file is far easier than forging a
	// root signature.
	if err := os.WriteFile(path, []byte(`{"floor":`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readEpochFloor(path)
	if err == nil {
		t.Fatalf("readEpochFloor(corrupt) = %d, nil — a corrupt record must NOT degrade to floor 0, which accepts every retired release key", got)
	}
}

func TestReadEpochFloor_UnreadableRecordFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still readable, so this case cannot be provoked")
	}
	path := filepath.Join(t.TempDir(), "epoch.json")
	if err := os.WriteFile(path, []byte(`{"floor":7}`), 0o000); err != nil {
		t.Fatal(err)
	}

	// Present and well-formed, but unreadable — distinct from absent, and the
	// distinction is the point: os.ReadFile fails for both, and only the
	// fs.ErrNotExist branch may return 0.
	got, err := readEpochFloor(path)
	if err == nil {
		t.Fatalf("readEpochFloor(unreadable) = %d, nil — an unreadable record must fail closed, not report floor 0", got)
	}
}
