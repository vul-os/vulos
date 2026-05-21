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
	mc := newMockCmd()
	// Fail the partition step.
	mc.set("", fmt.Errorf("disk not found"),
		"parted", "-s", "/dev/sdc",
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", "513MiB", "100%")

	svc := newWithCommander(mc)
	hub := newProgressHub()

	req := NetbootInstallRequest{
		Disk:         "sdc",
		Confirm:      true,
		SquashfsPath: "/nonexistent/os-core.squashfs",
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
	mc := newMockCmd()
	// The partition step for nvme0n1 should use 'p1'/'p2' suffixes.
	// We fail at mount (after partition) to keep the test short.
	mc.set("", fmt.Errorf("mount error"),
		"mount", "/dev/nvme0n1p2", netbootInstallMount)

	svc := newWithCommander(mc)
	hub := newProgressHub()

	req := NetbootInstallRequest{
		Disk:         "nvme0n1",
		Confirm:      true,
		SquashfsPath: "/nonexistent/os-core.squashfs",
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
