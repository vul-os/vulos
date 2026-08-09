//go:build linux

package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── partitionNames ───────────────────────────────────────────────────────────

func TestPartitionNames_SATA(t *testing.T) {
	esp, data := partitionNames("/dev/sdb")
	if esp != "/dev/sdb1" {
		t.Errorf("esp = %q, want /dev/sdb1", esp)
	}
	if data != "/dev/sdb2" {
		t.Errorf("data = %q, want /dev/sdb2", data)
	}
}

func TestPartitionNames_NVMe(t *testing.T) {
	esp, data := partitionNames("/dev/nvme1n1")
	if esp != "/dev/nvme1n1p1" {
		t.Errorf("esp = %q, want /dev/nvme1n1p1", esp)
	}
	if data != "/dev/nvme1n1p2" {
		t.Errorf("data = %q, want /dev/nvme1n1p2", data)
	}
}

func TestPartitionNames_MMC(t *testing.T) {
	esp, data := partitionNames("/dev/mmcblk0")
	if esp != "/dev/mmcblk0p1" {
		t.Errorf("esp = %q, want /dev/mmcblk0p1", esp)
	}
	if data != "/dev/mmcblk0p2" {
		t.Errorf("data = %q, want /dev/mmcblk0p2", data)
	}
}

// ─── verifySquashfsDigest ─────────────────────────────────────────────────────

func TestVerifySquashfsDigest_NoManifest(t *testing.T) {
	// When the manifest does not exist, verification is skipped (no error).
	err := verifySquashfsDigest("/some/squashfs", "/nonexistent/stable.json")
	if err != nil {
		t.Errorf("expected nil (skip), got: %v", err)
	}
}

func TestVerifySquashfsDigest_MatchingDigest(t *testing.T) {
	dir := t.TempDir()

	content := []byte("fake squashfs content")
	sqfsPath := filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(sqfsPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Compute expected sha256.
	h := sha256.Sum256(content)
	digest := hex.EncodeToString(h[:])

	manifest := `{"sha256":"` + digest + `"}`
	mPath := filepath.Join(dir, "stable.json")
	if err := os.WriteFile(mPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := verifySquashfsDigest(sqfsPath, mPath); err != nil {
		t.Errorf("expected nil for matching digest, got: %v", err)
	}
}

func TestVerifySquashfsDigest_MismatchDigest(t *testing.T) {
	dir := t.TempDir()

	sqfsPath := filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(sqfsPath, []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wrong sha256 in manifest.
	manifest := `{"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}`
	mPath := filepath.Join(dir, "stable.json")
	if err := os.WriteFile(mPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	err := verifySquashfsDigest(sqfsPath, mPath)
	if err == nil {
		t.Error("expected error for mismatched digest, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should mention mismatch, got: %v", err)
	}
}

func TestVerifySquashfsDigest_NoSHA256Field(t *testing.T) {
	dir := t.TempDir()

	sqfsPath := filepath.Join(dir, "os-core.squashfs")
	if err := os.WriteFile(sqfsPath, []byte("any content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Manifest without sha256 field — should skip verification.
	manifest := `{"path":"/vulos/os-core.squashfs","roothash":"abcdef"}`
	mPath := filepath.Join(dir, "stable.json")
	if err := os.WriteFile(mPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := verifySquashfsDigest(sqfsPath, mPath); err != nil {
		t.Errorf("expected nil when no sha256 field, got: %v", err)
	}
}

// ─── writeLiveBootEntry ───────────────────────────────────────────────────────

func TestWriteLiveBootEntry_CreatesEntry(t *testing.T) {
	tmpESP := t.TempDir()

	if err := writeLiveBootEntry(context.Background(), tmpESP); err != nil {
		t.Fatalf("writeLiveBootEntry: %v", err)
	}

	entryPath := filepath.Join(tmpESP, "loader", "entries", "vulos-live.conf")
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("entry file not created: %v", err)
	}
	content := string(data)

	checks := []string{
		"vulos.live=1",
		"toram",
		"os-core.squashfs",
		"initramfs.img",
		"vmlinuz",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("entry missing %q:\n%s", want, content)
		}
	}

	// loader.conf should also be present.
	loaderConf := filepath.Join(tmpESP, "loader", "loader.conf")
	if _, err := os.Stat(loaderConf); err != nil {
		t.Errorf("loader.conf not created: %v", err)
	}
}

// ─── copyFileWithProgress ─────────────────────────────────────────────────────

func TestCopyFileWithProgress_Success(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.squashfs")
	content := make([]byte, 3<<20) // 3 MiB
	for i := range content {
		content[i] = byte(i & 0xff)
	}
	if err := os.WriteFile(srcPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	destPath := filepath.Join(dir, "dest.squashfs")
	var pcts []int
	prog := func(pct int) { pcts = append(pcts, pct) }

	if err := copyFileWithProgress(srcPath, destPath, prog, 35, 88); err != nil {
		t.Fatalf("copyFileWithProgress: %v", err)
	}

	// Destination must have the same content.
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(got) != len(content) {
		t.Errorf("dest size = %d, want %d", len(got), len(content))
	}

	// All progress values must be in [35, 88].
	for _, p := range pcts {
		if p < 35 || p > 88 {
			t.Errorf("progress %d outside [35, 88]", p)
		}
	}
	if len(pcts) == 0 {
		t.Error("no progress values published")
	}
}

func TestCopyFileWithProgress_MissingSrc(t *testing.T) {
	prog := func(int) {}
	err := copyFileWithProgress("/nonexistent/src_bminit14", "/tmp/dest_bminit14", prog, 0, 100)
	if err == nil {
		t.Error("expected error for missing src, got nil")
	}
}

// ─── WriteBootableESP — validation ───────────────────────────────────────────

func TestWriteBootableESP_EmptyTargetDisk(t *testing.T) {
	err := WriteBootableESP(context.Background(), Config{})
	if err == nil {
		t.Error("expected error for empty TargetDisk, got nil")
	}
	if !strings.Contains(err.Error(), "TargetDisk") {
		t.Errorf("error should mention TargetDisk, got: %v", err)
	}
}

// TestWriteLiveBootEntry_OptionsIsOneLine is a regression test for a bug
// found by actually booting the netboot sibling of this entry writer's
// output in QEMU (NETB-05, scripts/netboot-install-smoke.sh): "options" was
// split across bare indented continuation lines (no repeated "options"
// keyword), which the Boot Loader Specification does not recognise —
// systemd-boot silently drops everything after the first "options" line.
// The real kernel command line QEMU reported after booting a disk built this
// way was missing vulos.live=1/toram/vulos.squashfs= entirely, even though
// TestWriteLiveBootEntry_CreatesEntry's substring checks all still "passed"
// against the file text — they never checked line structure. This test does.
func TestWriteLiveBootEntry_OptionsIsOneLine(t *testing.T) {
	tmpESP := t.TempDir()

	if err := writeLiveBootEntry(context.Background(), tmpESP); err != nil {
		t.Fatalf("writeLiveBootEntry: %v", err)
	}

	entryPath := filepath.Join(tmpESP, "loader", "entries", "vulos-live.conf")
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("entry file not created: %v", err)
	}
	content := string(data)

	var optionsLines []string
	for _, ln := range strings.Split(content, "\n") {
		if strings.HasPrefix(ln, "options") {
			optionsLines = append(optionsLines, ln)
		} else if strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t") {
			t.Fatalf("entry has a bare indented continuation line — the Boot Loader "+
				"Specification does not support this and systemd-boot silently drops it "+
				"(it must repeat the \"options\" keyword instead, or stay on one line): %q\nfull entry:\n%s", ln, content)
		}
	}
	if len(optionsLines) != 1 {
		t.Fatalf("expected exactly 1 line starting with \"options\", got %d: %v", len(optionsLines), optionsLines)
	}
	for _, want := range []string{"vulos.live=1", "toram", "vulos.squashfs="} {
		if !strings.Contains(optionsLines[0], want) {
			t.Errorf("the single options line is missing %q — it would never reach the kernel cmdline: %s", want, optionsLines[0])
		}
	}
}
