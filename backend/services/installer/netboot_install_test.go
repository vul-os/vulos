package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/osdist"
)

// ---------------------------------------------------------------------------
// NetbootInstallRequest validation
// ---------------------------------------------------------------------------

func TestHandleNetbootInstall_MethodNotAllowed(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/netboot-install", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestHandleNetbootInstall_MissingConfirm(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	body := `{"disk":"sda","confirm":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleNetbootInstall_InvalidDisk(t *testing.T) {
	cases := []string{"", "../sda", "/dev/sda", "sd a", "sda;rm"}
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	for _, bad := range cases {
		body := fmt.Sprintf(`{"disk":%q,"confirm":true}`, bad)
		req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
			strings.NewReader(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("disk=%q: status = %d, want 400", bad, rr.Code)
		}
	}
}

func TestHandleNetbootInstall_InvalidJSON(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader("not-json"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleNetbootInstall_ConflictWhenRunning(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	// Register a running hub.
	hub := newProgressHub()
	if err := registerNetbootHub(svc, hub); err != nil {
		t.Fatalf("registerNetbootHub: %v", err)
	}

	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	body := `{"disk":"sda","confirm":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}

	// Clean up: mark done so other tests don't see a stale running hub.
	hub.setDone(nil)
}

func TestHandleNetbootInstall_Accepted(t *testing.T) {
	mc := newMockCmd()
	// blkid returns UUIDs for fstab
	mc.set("aaaa-1111", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda1")
	mc.set("bbbb-2222", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda2")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	// Provide a valid squashfs_path so the pipeline doesn't try to open the
	// default /run/live/medium path (not present in the test environment).
	body := `{"disk":"sda","confirm":true,"squashfs_path":"/nonexistent/os-core.squashfs"}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rr.Code)
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "started" {
		t.Errorf("response status = %q, want started", resp["status"])
	}
}

// ---------------------------------------------------------------------------
// registerNetbootHub — allow re-register after completion
// ---------------------------------------------------------------------------

func TestRegisterNetbootHub_AllowReplaceWhenDone(t *testing.T) {
	svc := newWithCommander(newMockCmd())

	h1 := newProgressHub()
	h1.setDone(nil) // already complete

	if err := registerNetbootHub(svc, h1); err != nil {
		t.Fatalf("first register: %v", err)
	}
	h2 := newProgressHub()
	if err := registerNetbootHub(svc, h2); err != nil {
		t.Errorf("re-register after done should succeed, got: %v", err)
	}
	h2.setDone(nil)
}

func TestRegisterNetbootHub_BlockWhenRunning(t *testing.T) {
	svc := newWithCommander(newMockCmd())

	h1 := newProgressHub() // not done
	if err := registerNetbootHub(svc, h1); err != nil {
		t.Fatalf("first register: %v", err)
	}
	h2 := newProgressHub()
	err := registerNetbootHub(svc, h2)
	if err == nil {
		t.Error("expected conflict error, got nil")
	}
	// Clean up.
	h1.setDone(nil)
}

// ---------------------------------------------------------------------------
// writeInitialBootState
// ---------------------------------------------------------------------------

func TestWriteInitialBootState(t *testing.T) {
	// Create a temporary directory to act as netbootInstallMount.
	tmp := t.TempDir()
	origMount := netbootInstallMount

	// Swap the mount constant for the test.  Because the constant is a package-
	// level const we cannot change it at runtime; instead we test the helper
	// directly by constructing the cacheDir manually.
	cacheDir := filepath.Join(tmp, vulosCacheRelPath)

	// Create the slot manager (creates slot-a and slot-b dirs).
	sm, err := osdist.NewSlotManager(cacheDir)
	if err != nil {
		t.Fatalf("NewSlotManager: %v", err)
	}

	// Write initial boot state.
	bs := &osdist.BootState{
		Active:        osdist.SlotA,
		Pending:       osdist.SlotNone,
		BootCounter:   0,
		LastKnownGood: osdist.SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read back and verify.
	loaded, err := sm.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Active != osdist.SlotA {
		t.Errorf("active = %q, want %q", loaded.Active, osdist.SlotA)
	}
	if loaded.BootCounter != 0 {
		t.Errorf("boot_counter = %d, want 0", loaded.BootCounter)
	}
	if loaded.LastKnownGood != osdist.SlotA {
		t.Errorf("last_known_good = %q, want %q", loaded.LastKnownGood, osdist.SlotA)
	}
	_ = origMount // suppress unused warning
}

// ---------------------------------------------------------------------------
// copyWithProgress
// ---------------------------------------------------------------------------

func TestCopyWithProgress_Success(t *testing.T) {
	// Create a source file.
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "test.squashfs")
	content := []byte("fake squashfs content for testing")
	if err := os.WriteFile(srcPath, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "os-core.squashfs")

	hub := newProgressHub()
	ch := hub.subscribe()
	<-ch // drain initial 0

	if err := copyWithProgress(srcPath, destPath, hub, 35, 85); err != nil {
		t.Fatalf("copyWithProgress: %v", err)
	}

	// Destination should have the same content.
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dest content mismatch: got %q, want %q", got, content)
	}
}

func TestCopyWithProgress_SrcNotFound(t *testing.T) {
	hub := newProgressHub()
	err := copyWithProgress("/nonexistent/path.squashfs", "/tmp/dest.squashfs", hub, 0, 100)
	if err == nil {
		t.Error("expected error for missing src, got nil")
	}
}

func TestCopyWithProgress_ProgressRange(t *testing.T) {
	// Write a larger source so progress is published multiple times.
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "big.squashfs")
	// 3 MiB so we get at least 3 progress updates (1 MiB buffer).
	content := make([]byte, 3<<20)
	for i := range content {
		content[i] = byte(i & 0xff)
	}
	if err := os.WriteFile(srcPath, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "os-core.squashfs")

	hub := newProgressHub()
	ch := hub.subscribe()
	<-ch // drain 0

	var pcts []int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for pct := range ch {
			if pct < 0 {
				return
			}
			pcts = append(pcts, pct)
		}
	}()

	if err := copyWithProgress(srcPath, destPath, hub, 35, 85); err != nil {
		t.Fatalf("copyWithProgress: %v", err)
	}
	hub.setDone(nil)
	<-done

	// All published pcts should be in [35, 85].
	for _, p := range pcts {
		if p < 35 || p > 85 {
			t.Errorf("progress %d outside range [35, 85]", p)
		}
	}
	// At least one progress value must have been published.
	if len(pcts) == 0 {
		t.Error("no progress values published")
	}
}

// ---------------------------------------------------------------------------
// runNetbootInstall pipeline (mock Commander)
// ---------------------------------------------------------------------------

func TestRunNetbootInstall_PartitionFailure(t *testing.T) {
	// verify-verity then verify-squashfs run before partition, so the medium must
	// carry a complete, correctly signed artifact set for the pipeline to reach
	// the partition step under test.
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)
	c := f.cfg()

	mc := newMockCmd()
	// Fail the partition step.
	mc.set("", fmt.Errorf("disk not found"),
		"parted", "-s", "/dev/sdc",
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", "513MiB", "100%")

	svc := newWithCommander(mc)
	svc.verifyCfg = &c
	hub := newProgressHub()

	req := NetbootInstallRequest{
		Disk:         "sdc",
		Confirm:      true,
		SquashfsPath: f.squashfsPath,
	}
	svc.runNetbootInstall(req, hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("hub not done after runNetbootInstall failure")
	}
	if err == nil {
		t.Fatal("expected error for failed partition step")
	}
	if !strings.Contains(err.Error(), "partition") {
		t.Errorf("error should mention partition, got: %v", err)
	}
}

func TestRunNetbootInstall_NVMePartitionSuffix(t *testing.T) {
	// The verification steps run before partition; provide a complete signed
	// medium so the pipeline proceeds to partitioning.
	f := newVerifyFixture(t)
	f.signedMedium(t, 0)
	c := f.cfg()

	mc := newMockCmd()
	// The partition step for nvme0n1 should use 'p1'/'p2' suffixes.
	// We fail at mount (after partition) to keep the test short.
	mc.set("", fmt.Errorf("mount error"),
		"mount", "/dev/nvme0n1p2", netbootInstallMount)

	svc := newWithCommander(mc)
	svc.verifyCfg = &c
	hub := newProgressHub()

	req := NetbootInstallRequest{
		Disk:         "nvme0n1",
		Confirm:      true,
		SquashfsPath: f.squashfsPath,
	}
	svc.runNetbootInstall(req, hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("hub not done")
	}
	// Error is expected (we injected a mount failure).
	_ = err

	// Verify parted was called with p1/p2 (no partition suffix for NVMe).
	// partSuffix("nvme0n1", 1) = "p1" → disk + "p1" = "nvme0n1p1"
	// The parted call uses /dev/nvme0n1 which has no suffix.
	if !mc.called("parted", "-s", "/dev/nvme0n1",
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", "513MiB", "100%") {
		t.Error("parted was not called with /dev/nvme0n1")
	}
}

// ---------------------------------------------------------------------------
// Default squashfs path
// ---------------------------------------------------------------------------

func TestNetbootInstallRequest_DefaultSquashfsPath(t *testing.T) {
	mc := newMockCmd()
	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterNetbootHandlers(mux, svc)

	// No squashfs_path in body → should default to defaultNetbootSquashfsPath.
	body := `{"disk":"sda","confirm":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/netboot-install",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// The request is accepted even when squashfs_path is empty (default is set
	// server-side).  The pipeline will fail asynchronously if the file doesn't
	// exist — that's not tested here.
	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// writeSlotABootEntry — unit test against a temp ESP tree
// ---------------------------------------------------------------------------

func TestWriteSlotABootEntry_CreatesEntryFile(t *testing.T) {
	// Build a minimal fake ESP tree under a temp dir.
	espMount := t.TempDir()

	// We need a real Commander for the mkdir + sh write calls.
	// Use a partial mock that delegates mkdir/sh to a real exec.
	// Since we're on the test machine (not Linux target), use the real osCommander.
	svc := New() // real commander

	if err := svc.writeSlotABootEntry(context.Background(), espMount); err != nil {
		// This may fail on non-Linux hosts because cp of /boot/vmlinuz fails.
		// The entry file itself should still be created.
		t.Logf("writeSlotABootEntry: %v (may be expected on non-Linux)", err)
	}

	entryPath := filepath.Join(espMount, "loader", "entries", "vulos-slot-a.conf")
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("entry file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "vulos.slot=a") {
		t.Errorf("entry missing vulos.slot=a: %s", content)
	}
	if !strings.Contains(content, "os-core.squashfs") {
		t.Errorf("entry missing squashfs reference: %s", content)
	}
	if !strings.Contains(content, "initramfs.img") {
		t.Errorf("entry missing initramfs reference: %s", content)
	}
}

// TestWriteSlotABootEntry_CarriesTokenInitramfsHookRequires pins the boot entry
// to the contract the initramfs hook actually enforces, rather than to whatever
// the entry happened to say.
//
// The netboot root partition holds only the slot-a squashfs; the OS is inside
// it. scripts/initramfs/vulos-live is the only thing that mounts it, it is
// gated on `cmdline_has vulos.live`, and it is the sole consumer of
// vulos.squashfs=. So an entry without the vulos.live token boots a machine to
// a partition containing no operating system — and that is exactly what shipped
// here, while the function's own doc comment said the token WAS passed.
//
// Asserting both together is the point: vulos.squashfs= without vulos.live is
// meaningless, so a test that checked only the former would have stayed green
// through the bug.
func TestWriteSlotABootEntry_CarriesTokenInitramfsHookRequires(t *testing.T) {
	espMount := t.TempDir()
	svc := New() // real commander, matching TestWriteSlotABootEntry_CreatesEntryFile

	// May error on a non-Linux host (cp of /boot/vmlinuz), but the entry file
	// itself is still written — that is what we assert on.
	if err := svc.writeSlotABootEntry(context.Background(), espMount); err != nil {
		t.Logf("writeSlotABootEntry: %v (may be expected off-target)", err)
	}
	raw, err := os.ReadFile(filepath.Join(espMount, "loader", "entries", "vulos-slot-a.conf"))
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	content := string(raw)

	// The hook's own matcher accepts a bare token or key=value; assert the form
	// we actually write, so a rename to something the hook does NOT match fails.
	if !strings.Contains(content, "vulos.live=0") {
		t.Errorf("entry omits vulos.live=0 — scripts/initramfs/vulos-live is gated on cmdline_has vulos.live, "+
			"so without it the slot-a squashfs is never mounted and the box boots a partition with no OS:\n%s", content)
	}
	if !strings.Contains(content, "vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs") {
		t.Errorf("entry omits the slot-a squashfs path the hook reads:\n%s", content)
	}
	// toram belongs to the live-USB path only: this box mounts the squashfs from
	// its own disk, and pulling it into RAM would defeat the slot layout.
	if strings.Contains(content, "toram") {
		t.Errorf("slot-a entry must not pass toram — the squashfs is mounted from the local slot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// installNetbootBootctl / installNetbootLoader — bootctl --path/--root shape
// ---------------------------------------------------------------------------
//
// Regression for a bug found by actually running the install pipeline against
// real bootctl (systemd 257) in a privileged Linux container (NETB-05): both
// functions passed espMount/netbootInstallMount+"/boot/efi" — an ABSOLUTE
// path that already contains the --root prefix — as --path. bootctl
// concatenates --root and --path, so the real invocation resolved to
// "<netbootInstallMount><netbootInstallMount>/boot/efi", which never exists,
// and bootctl failed with "Failed to open parent directory" on every real
// install — aborting the pipeline at the very first bootctl call, long before
// the vulos.squashfs=/vulos.live=0 tokens (dfff94f5, and the vulos-live hook
// fix alongside this test) ever mattered. Confirmed the fix against real
// bootctl: "--path=/boot/efi --root=X" succeeds, "--path=X/boot/efi
// --root=X" does not. These tests pin the exact args so a future edit can't
// silently reintroduce the double-prefixed form without booting a real
// system (or a container with real bootctl) to notice.
func TestInstallNetbootBootctl_PathIsRelativeToRoot(t *testing.T) {
	mc := newMockCmd()
	svc := newWithCommander(mc)

	espMount := netbootInstallMount + "/boot/efi"
	if err := svc.installNetbootBootctl(context.Background(), espMount); err != nil {
		t.Fatalf("installNetbootBootctl: %v", err)
	}

	if !mc.called("bootctl", "--path=/boot/efi", "--root="+netbootInstallMount, "install", "--no-variables") {
		t.Errorf("bootctl was not called with the expected root-relative --path; calls: %v", mc.calls)
	}
	for _, c := range mc.calls {
		if strings.Contains(c, netbootInstallMount+netbootInstallMount) {
			t.Errorf("bootctl called with a double-prefixed path (would fail against real bootctl): %s", c)
		}
	}
}

func TestInstallNetbootLoader_PathIsRelativeToRoot(t *testing.T) {
	mc := newMockCmd()
	svc := newWithCommander(mc)

	if err := svc.installNetbootLoader(context.Background()); err != nil {
		t.Fatalf("installNetbootLoader: %v", err)
	}

	if !mc.called("bootctl", "--path=/boot/efi", "--root="+netbootInstallMount, "install") {
		t.Errorf("bootctl was not called with the expected root-relative --path; calls: %v", mc.calls)
	}
	for _, c := range mc.calls {
		if strings.Contains(c, netbootInstallMount+netbootInstallMount) {
			t.Errorf("bootctl called with a double-prefixed path (would fail against real bootctl): %s", c)
		}
	}
}

// ---------------------------------------------------------------------------
// writeFileViaCommander — real newlines, not literal "\n"
// ---------------------------------------------------------------------------
//
// Regression for the bug found by actually booting a netboot-installed disk
// in QEMU (NETB-05, scripts/netboot-install-smoke.sh): the previous shape —
// s.cmd.Output(ctx, "sh", "-c", fmt.Sprintf("printf '%%s' %q > %s", content, path))
// — used Go's %q to interpolate content into shell script TEXT. %q renders a
// real newline byte as the two characters '\' 'n', and `printf '%s'` never
// re-interprets backslash escapes, so every real newline in the written
// content became the literal two-character sequence "\n" in the file. The
// existing entry-file tests (TestWriteSlotABootEntry_CreatesEntryFile etc.)
// used strings.Contains on substrings like "vulos.live=0" — which still
// matched even with every line glued onto one via literal "\n" text, so they
// stayed green through the bug. This test asserts what those didn't: the
// written file actually contains REAL newline bytes, and does not contain
// the literal two-character escape sequence.
func TestWriteFileViaCommander_RealNewlines(t *testing.T) {
	svc := New() // real commander — this bug only exists in the real shell path.
	dir := t.TempDir()
	path := dir + "/multi-line.conf"
	content := "title   Vulos OS (slot-a)\nlinux   /EFI/vulos/vmlinuz\noptions root=LABEL=vulos-root\n"

	if err := svc.writeFileViaCommander(context.Background(), content, path); err != nil {
		t.Fatalf("writeFileViaCommander: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	got := string(raw)

	if got != content {
		t.Fatalf("written content does not match exactly:\n got:  %q\n want: %q", got, content)
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("written file has no real newline bytes at all: %q", got)
	}
	if strings.Contains(got, `\n`) {
		t.Fatalf("written file contains the LITERAL two-character sequence backslash-n "+
			"(the exact bug this test guards against): %q", got)
	}
	if lines := strings.Count(got, "\n"); lines != 3 {
		t.Fatalf("expected 3 real newlines (one per content line), got %d: %q", lines, got)
	}
}

// TestWriteSlotABootEntry_RealNewlines pins the actual production call site
// (not just the shared helper in isolation): the written vulos-slot-a.conf
// must be genuinely multi-line, matching what systemd-boot's entry parser
// requires (title/linux/initrd/options each on their own line).
func TestWriteSlotABootEntry_RealNewlines(t *testing.T) {
	espMount := t.TempDir()
	svc := New()

	if err := svc.writeSlotABootEntry(context.Background(), espMount); err != nil {
		t.Logf("writeSlotABootEntry: %v (may be expected off-target)", err)
	}
	raw, err := os.ReadFile(filepath.Join(espMount, "loader", "entries", "vulos-slot-a.conf"))
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	content := string(raw)

	if strings.Contains(content, `\n`) {
		t.Fatalf("entry file contains literal backslash-n instead of real newlines — "+
			"systemd-boot cannot parse a one-line entry:\n%s", content)
	}
	// A valid entry has "title", "linux", "initrd", "options" as separate
	// lines — grep-style, one directive per real line.
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	wantPrefixes := []string{"title", "linux", "initrd", "options"}
	for _, want := range wantPrefixes {
		found := false
		for _, ln := range lines {
			if strings.HasPrefix(strings.TrimSpace(ln), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no line starts with %q — entry is not properly line-structured:\n%s", want, content)
		}
	}
}

// TestWriteSlotABootEntry_OptionsIsOneLine is a regression test for the bug
// found by actually booting a netboot-installed disk in QEMU (NETB-05,
// scripts/netboot-install-smoke.sh): "options" was split across a bare
// indented continuation line (no repeated "options" keyword), which the Boot
// Loader Specification does not recognise — systemd-boot silently drops
// everything after the first "options" line. The real
// `Kernel command line:` QEMU reported after booting a disk built this way
// was "initrd=... root=LABEL=vulos-root ro" with NOTHING else: vulos.live=0,
// vulos.slot=a, and vulos.squashfs= were all silently dropped, so the
// initramfs hook's `cmdline_has vulos.live` gate failed and the machine
// booted the bare (un-overlaid) vulos-root partition, which has no
// /sbin/init — "No init found. Try passing init= bootarg." at the initramfs
// shell. TestWriteSlotABootEntry_CarriesTokenInitramfsHookRequires'
// substring checks all still "passed" against that broken shape — they never
// checked line structure. This test does.
func TestWriteSlotABootEntry_OptionsIsOneLine(t *testing.T) {
	espMount := t.TempDir()
	svc := New()

	if err := svc.writeSlotABootEntry(context.Background(), espMount); err != nil {
		t.Logf("writeSlotABootEntry: %v (may be expected off-target)", err)
	}
	raw, err := os.ReadFile(filepath.Join(espMount, "loader", "entries", "vulos-slot-a.conf"))
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	content := string(raw)

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
	for _, want := range []string{"vulos.live=0", "vulos.slot=a", "vulos.squashfs="} {
		if !strings.Contains(optionsLines[0], want) {
			t.Errorf("the single options line is missing %q — it would never reach the kernel cmdline: %s", want, optionsLines[0])
		}
	}
}

// ---------------------------------------------------------------------------
// OWNSTATE-01 — the owner's state directories
// ---------------------------------------------------------------------------
//
// THE DEFECT THESE EXIST TO STOP, observed on a real arm64 QEMU boot of a
// netboot-installed disk with dm-verity active: an owner account was created,
// the guest was rebooted, and afterwards /api/auth/status answered
// {"has_users":false} and the login 401'd. /root/.vulos had been recreated from
// nothing. The cause is the one NETB-03 fixed for /var/cache/vulos — the final
// `mount -o bind $MERGED $rootmnt` in scripts/initramfs/vulos-live shadows the
// ext4, so the data directory resolves inside the overlay whose upper layer is
// a tmpfs in RAM.
//
// The initramfs binds the on-disk subtree back out, and it can only bind a
// directory that already exists: until that rebind the partition is $rootmnt
// mounted read-only, and a mkdir into it is exactly the failure
// roadmap/BOOT-FOUR-ERRORS.md documents. The running OS cannot create it
// either — once the overlay is bound over $rootmnt the partition is unreachable
// by path for the life of the machine. So the installer is the only place these
// can come from, and if this step stops running the fix silently stops working
// while every mount-topology assertion in backend/internal/docsref stays green
// (that harness fabricates the directories).

func TestCreateOwnerStateDirs(t *testing.T) {
	m := newMockCmd()
	svc := newWithCommander(m)

	if err := svc.createOwnerStateDirs(context.Background(), "/mnt/target"); err != nil {
		t.Fatalf("createOwnerStateDirs: %v", err)
	}

	if len(ownerStateDirs) == 0 {
		t.Fatal("ownerStateDirs is empty, so this test asserts nothing")
	}
	for _, d := range ownerStateDirs {
		target := "/mnt/target/" + d.rel
		if !m.called("mkdir", "-p", "-m", d.mode, target) {
			t.Errorf("no `mkdir -p -m %s %s`; without it the initramfs has nothing to bind "+
				"and the owner's account goes back to living in RAM.\ncalls: %v",
				d.mode, target, m.calls)
		}
		// mkdir -m applies the mode only when it CREATES the directory. On a
		// re-install over an existing partition it would silently keep whatever
		// mode was there, so the chmod is not redundant.
		if !m.called("chmod", d.mode, target) {
			t.Errorf("no `chmod %s %s`; mkdir -m does not fix the mode of a directory that "+
				"already existed.\ncalls: %v", d.mode, target, m.calls)
		}
	}
}

// TestOwnerStateDirsCoverTheDataDirAndVarLib pins WHICH directories, not just
// that some are created. Both are load-bearing and for different reasons, and
// a list that lost either would still pass the test above.
func TestOwnerStateDirsCoverTheDataDirAndVarLib(t *testing.T) {
	have := map[string]string{}
	for _, d := range ownerStateDirs {
		have[d.rel] = d.mode
	}

	// backend/internal/datadir resolves to $HOME/.vulos, and the vulos.service
	// build.sh writes sets HOME=/root and never sets VULOS_DATA_DIR. This is
	// auth.db, auth.key, db/instance.json, the peering identity, the device key
	// and the vaults.
	if mode, ok := have["root/.vulos"]; !ok {
		t.Errorf("ownerStateDirs no longer contains root/.vulos — that is the box's data "+
			"directory, and without it on the partition the owner's account does not "+
			"survive a reboot on a netboot-installed disk. have: %v", have)
	} else if mode != "0700" {
		t.Errorf("root/.vulos is created %s, not 0700. It holds the owner's password hash, "+
			"live session records, the session-signing secret, the box's Ed25519 private "+
			"key and the credential vaults, none of which is encrypted at rest — this "+
			"project ships no LUKS on any boot path. The directory mode is the only "+
			"access control there is.", mode)
	}

	// NOT under the data dir: cmd/server hardcodes /var/lib/vulos for the
	// .setup-complete marker, the LAN TLS cert/key and the signing epoch floor.
	// Persisting only the data dir would leave a box that has an owner re-running
	// the setup wizard and forgetting its OTA anti-rollback floor on every boot.
	if _, ok := have["var/lib/vulos"]; !ok {
		t.Errorf("ownerStateDirs no longer contains var/lib/vulos — cmd/server writes the "+
			".setup-complete marker, the LAN TLS material and the signing epoch floor "+
			"there, outside the data directory. have: %v", have)
	}

	// A mountpoint, not a store: the initramfs mounts a tmpfs back over it so an
	// installed-app manifest cannot outlive the Flatpak payload in /var/lib/flatpak
	// that it points at (roadmap/APP-DIR-PERSISTENCE.md).
	if _, ok := have["root/.vulos/apps"]; !ok {
		t.Errorf("ownerStateDirs no longer contains root/.vulos/apps. The initramfs mounts "+
			"a tmpfs over it to keep app manifests as volatile as their payloads, and it "+
			"cannot mkdir a mountpoint into $rootmnt. have: %v", have)
	}
}

// TestNetbootPipelineActuallyRunsTheStateDirsStep closes the gap between "the
// function is correct" and "the function is called".
//
// runNetbootInstall builds its step list inline, and netboot_e2e_linux_test.go
// restates that list rather than sharing it. So a step can be present in one and
// absent from the other, and every other test in this package would stay green —
// including the E2E one, which would then be installing a disk the real
// installer does not produce. Both are read here.
// The anchors below are STEP-LIST entries, not bare call sites. The first
// version of this test looked for "createOwnerStateDirs(ctx" — which is also
// how the function's own DEFINITION begins, so deleting the pipeline step left
// it green. A mutation caught that; the anchors are now the step declarations,
// which exist only where the step is actually scheduled.
func TestNetbootPipelineActuallyRunsTheStateDirsStep(t *testing.T) {
	for _, tc := range []struct {
		file, anchor string
	}{
		{"netboot_install.go", `{name: "state-dirs"`},      // the shipped pipeline
		{"netboot_e2e_linux_test.go", `step("state-dirs"`}, // the pipeline the smoke harness proves
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if !strings.Contains(string(src), tc.anchor) {
			t.Errorf("%s no longer schedules a %s step. The directories it makes are the "+
				"only thing scripts/initramfs/vulos-live can bind the owner's account out "+
				"of; without this step a netboot-installed box loses its owner on every "+
				"reboot, and every mount-topology test still passes because that harness "+
				"fabricates the directories itself.", tc.file, tc.anchor)
		}
		if !strings.Contains(string(src), "createOwnerStateDirs(ctx") {
			t.Errorf("%s no longer calls createOwnerStateDirs at all.", tc.file)
		}
	}
}
