package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock Commander
// ---------------------------------------------------------------------------

type mockCmd struct {
	// outputs maps "cmd arg1 arg2..." → (stdout, error).
	outputs map[string]mockResult
	// calls records every invocation for assertion.
	calls []string
}

type mockResult struct {
	out []byte
	err error
}

func newMockCmd() *mockCmd {
	return &mockCmd{outputs: make(map[string]mockResult)}
}

func (m *mockCmd) key(cmd string, args []string) string {
	return strings.Join(append([]string{cmd}, args...), " ")
}

func (m *mockCmd) set(out string, err error, cmd string, args ...string) {
	m.outputs[m.key(cmd, args)] = mockResult{out: []byte(out), err: err}
}

func (m *mockCmd) Output(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	k := m.key(cmd, args)
	m.calls = append(m.calls, k)
	if r, ok := m.outputs[k]; ok {
		return r.out, r.err
	}
	// Default: succeed with empty output.
	return nil, nil
}

func (m *mockCmd) RunLines(ctx context.Context, w io.Writer, cmd string, args ...string) error {
	k := m.key(cmd, args)
	m.calls = append(m.calls, k)
	if r, ok := m.outputs[k]; ok {
		if r.err != nil {
			return r.err
		}
		w.Write(r.out)
		return nil
	}
	return nil
}

func (m *mockCmd) called(cmd string, args ...string) bool {
	k := m.key(cmd, args)
	for _, c := range m.calls {
		if c == k {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// parseLsblkJSON
// ---------------------------------------------------------------------------

func TestParseLsblkJSON_BasicDisk(t *testing.T) {
	raw := `{
		"blockdevices": [
			{
				"name": "sda",
				"size": "500G",
				"model": "Samsung SSD",
				"type": "disk",
				"children": [
					{"name":"sda1","size":"512M","model":null,"type":"part","children":null},
					{"name":"sda2","size":"499.5G","model":null,"type":"part","children":null}
				]
			},
			{
				"name": "sr0",
				"size": "1G",
				"model": "CDROM",
				"type": "rom",
				"children": null
			}
		]
	}`

	disks, err := parseLsblkJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parseLsblkJSON error: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk (rom excluded), got %d", len(disks))
	}
	d := disks[0]
	if d.Name != "sda" {
		t.Errorf("name = %q, want sda", d.Name)
	}
	if d.Model != "Samsung SSD" {
		t.Errorf("model = %q, want Samsung SSD", d.Model)
	}
	if len(d.Partitions) != 2 {
		t.Errorf("partitions = %d, want 2", len(d.Partitions))
	}
	if d.Partitions[0].Name != "sda1" {
		t.Errorf("partition[0] name = %q, want sda1", d.Partitions[0].Name)
	}
}

func TestParseLsblkJSON_NVMe(t *testing.T) {
	raw := `{
		"blockdevices": [
			{
				"name": "nvme0n1",
				"size": "1T",
				"model": "WD Black",
				"type": "disk",
				"children": [
					{"name":"nvme0n1p1","size":"512M","model":null,"type":"part","children":null}
				]
			}
		]
	}`
	disks, err := parseLsblkJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 {
		t.Fatalf("want 1 disk, got %d", len(disks))
	}
	if disks[0].Partitions[0].Name != "nvme0n1p1" {
		t.Errorf("unexpected partition name %q", disks[0].Partitions[0].Name)
	}
}

func TestParseLsblkJSON_InvalidJSON(t *testing.T) {
	_, err := parseLsblkJSON([]byte("not json"))
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestParseLsblkJSON_ModelNull(t *testing.T) {
	raw := `{"blockdevices":[{"name":"sdb","size":"2T","model":null,"type":"disk","children":null}]}`
	disks, err := parseLsblkJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if disks[0].Model != "" {
		t.Errorf("expected empty model, got %q", disks[0].Model)
	}
}

// ---------------------------------------------------------------------------
// GET /api/installer/disks
// ---------------------------------------------------------------------------

func TestHandleDisks_Success(t *testing.T) {
	mc := newMockCmd()
	lsblkOut := `{"blockdevices":[{"name":"sda","size":"256G","model":"KINGSTON","type":"disk","children":null}]}`
	mc.set(lsblkOut, nil, "lsblk", "-J", "-o", "NAME,SIZE,MODEL,TYPE")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/disks", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var disks []Disk
	if err := json.NewDecoder(rr.Body).Decode(&disks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(disks) != 1 || disks[0].Name != "sda" {
		t.Errorf("unexpected disks: %+v", disks)
	}
}

func TestHandleDisks_MethodNotAllowed(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/installer/disks", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestHandleDisks_LsblkError(t *testing.T) {
	mc := newMockCmd()
	mc.set("", fmt.Errorf("lsblk not found"), "lsblk", "-J", "-o", "NAME,SIZE,MODEL,TYPE")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/disks", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /api/installer/status
// ---------------------------------------------------------------------------

func TestHandleStatus_Live(t *testing.T) {
	mc := newMockCmd()
	// /proc/mounts with squashfs root
	mc.set("overlay / squashfs ro 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "live" {
		t.Errorf("mode = %q, want live", resp.Mode)
	}
}

func TestHandleStatus_Installed(t *testing.T) {
	mc := newMockCmd()
	mc.set("/dev/sda2 / ext4 rw 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var resp StatusResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Mode != "installed" {
		t.Errorf("mode = %q, want installed", resp.Mode)
	}
	if resp.RootDev != "/dev/sda2" {
		t.Errorf("root_dev = %q, want /dev/sda2", resp.RootDev)
	}
}

func TestHandleStatus_OverlayDetected(t *testing.T) {
	mc := newMockCmd()
	mc.set("overlayfs / overlay rw 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var resp StatusResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Mode != "live" {
		t.Errorf("mode = %q, want live", resp.Mode)
	}
}

func TestHandleStatus_MethodNotAllowed(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPut, "/api/installer/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/installer/install
// ---------------------------------------------------------------------------

func TestHandleInstall_Accepted(t *testing.T) {
	mc := newMockCmd()
	// All commands succeed with empty output by default.

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	body := `{"disk":"sda","confirm":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/install",
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

func TestHandleInstall_MissingConfirm(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	body := `{"disk":"sda","confirm":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInstall_InvalidDisk(t *testing.T) {
	cases := []string{"", "../sda", "sd a", "/dev/sda", "sda; rm -rf /"}
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	for _, bad := range cases {
		body := fmt.Sprintf(`{"disk":%q,"confirm":true}`, bad)
		req := httptest.NewRequest(http.MethodPost, "/api/installer/install",
			strings.NewReader(body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("disk=%q: status = %d, want 400", bad, rr.Code)
		}
	}
}

func TestHandleInstall_MethodNotAllowed(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/install", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestHandleInstall_InvalidJSON(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/installer/install",
		strings.NewReader("not-json"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleInstall_ConflictWhenRunning(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	// Inject a running hub.
	svc.hub = newProgressHub() // not done

	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	body := `{"disk":"sda","confirm":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/installer/install",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// validDiskName
// ---------------------------------------------------------------------------

func TestValidDiskName(t *testing.T) {
	valid := []string{"sda", "sdb", "nvme0n1", "mmcblk0", "hda", "vda"}
	for _, n := range valid {
		if !validDiskName(n) {
			t.Errorf("validDiskName(%q) = false, want true", n)
		}
	}

	invalid := []string{
		"", " ", "sda1 sdb", "../etc", "/dev/sda", "sda;rm", "sda\x00",
		strings.Repeat("a", 33),
	}
	for _, n := range invalid {
		if validDiskName(n) {
			t.Errorf("validDiskName(%q) = true, want false", n)
		}
	}
}

// ---------------------------------------------------------------------------
// partSuffix
// ---------------------------------------------------------------------------

func TestPartSuffix(t *testing.T) {
	cases := []struct {
		disk string
		n    int
		want string
	}{
		{"sda", 1, "1"},
		{"sda", 2, "2"},
		{"nvme0n1", 1, "p1"},
		{"nvme0n1", 2, "p2"},
		{"mmcblk0", 1, "p1"},
		{"hda", 1, "1"},
	}
	for _, tc := range cases {
		got := partSuffix(tc.disk, tc.n)
		if got != tc.want {
			t.Errorf("partSuffix(%q, %d) = %q, want %q", tc.disk, tc.n, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// progressHub
// ---------------------------------------------------------------------------

func TestProgressHub_SubscribeReceivesLatest(t *testing.T) {
	h := newProgressHub()
	h.publish(42)

	ch := h.subscribe()
	got := <-ch
	if got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
}

func TestProgressHub_BroadcastToMultiple(t *testing.T) {
	h := newProgressHub()
	ch1 := h.subscribe()
	ch2 := h.subscribe()

	// Drain the initial 0 from both.
	<-ch1
	<-ch2

	h.publish(50)
	if v := <-ch1; v != 50 {
		t.Errorf("ch1 expected 50, got %d", v)
	}
	if v := <-ch2; v != 50 {
		t.Errorf("ch2 expected 50, got %d", v)
	}
}

func TestProgressHub_SetDoneSentinel(t *testing.T) {
	h := newProgressHub()
	ch := h.subscribe()
	<-ch // drain initial 0

	h.setDone(nil)
	if v := <-ch; v != -1 {
		t.Errorf("expected -1 sentinel, got %d", v)
	}
	done, err := h.isDone()
	if !done {
		t.Error("hub should be done")
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProgressHub_SetDoneWithError(t *testing.T) {
	h := newProgressHub()
	h.setDone(fmt.Errorf("disk full"))

	done, err := h.isDone()
	if !done {
		t.Error("hub should be done")
	}
	if err == nil || err.Error() != "disk full" {
		t.Errorf("error = %v, want 'disk full'", err)
	}
}

func TestProgressHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := newProgressHub()
	ch := h.subscribe()
	<-ch // drain initial

	h.unsubscribe(ch)
	h.publish(99)

	select {
	case v, ok := <-ch:
		if ok {
			t.Errorf("expected no value after unsubscribe, got %d", v)
		}
	default:
		// correct — channel not written to
	}
}

// ---------------------------------------------------------------------------
// runInstall pipeline (integration-style with mock)
// ---------------------------------------------------------------------------

func TestRunInstall_Success(t *testing.T) {
	mc := newMockCmd()
	// blkid returns UUIDs
	mc.set("aaaa-1111", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda1")
	mc.set("bbbb-2222", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/sda2")

	svc := newWithCommander(mc)
	hub := newProgressHub()

	svc.runInstall("sda", hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("hub not done after runInstall")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify key commands were called.
	if !mc.called("parted", "-s", "/dev/sda",
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", "513MiB", "100%") {
		t.Error("parted was not called")
	}
	if !mc.called("mkfs.fat", "-F32", "-n", "EFI", "/dev/sda1") {
		t.Error("mkfs.fat was not called")
	}
	if !mc.called("mkfs.ext4", "-F", "-L", "vulos-root", "/dev/sda2") {
		t.Error("mkfs.ext4 was not called")
	}
}

func TestRunInstall_NVMe(t *testing.T) {
	mc := newMockCmd()
	mc.set("aaaa", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/nvme0n1p1")
	mc.set("bbbb", nil, "blkid", "-s", "UUID", "-o", "value", "/dev/nvme0n1p2")

	svc := newWithCommander(mc)
	hub := newProgressHub()

	svc.runInstall("nvme0n1", hub)

	done, err := hub.isDone()
	if !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if !mc.called("mkfs.fat", "-F32", "-n", "EFI", "/dev/nvme0n1p1") {
		t.Error("NVMe ESP partition not formatted")
	}
}

func TestRunInstall_PartitionFailure(t *testing.T) {
	mc := newMockCmd()
	mc.set("", fmt.Errorf("disk not found"), "parted", "-s", "/dev/sdb",
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", "513MiB", "100%")

	svc := newWithCommander(mc)
	hub := newProgressHub()

	svc.runInstall("sdb", hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("hub not done")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "partition") {
		t.Errorf("error should mention partition step, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// rsyncProgressRE
// ---------------------------------------------------------------------------

func TestRsyncProgressRE(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"    1,234,567  42%  12.34MB/s    0:00:03", 42},
		{"  100%", 100},
		{"   0%", 0},
		{"no match here", -1},
	}
	for _, tc := range cases {
		m := rsyncProgressRE.FindStringSubmatch(tc.line)
		if tc.want < 0 {
			if m != nil {
				t.Errorf("line=%q: expected no match, got %v", tc.line, m)
			}
			continue
		}
		if m == nil {
			t.Errorf("line=%q: expected match, got none", tc.line)
			continue
		}
		got, _ := strconv.Atoi(m[1])
		if got != tc.want {
			t.Errorf("line=%q: pct = %d, want %d", tc.line, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// LiveSessionInfo + GET /api/installer/live-session (NETB-04)
// ---------------------------------------------------------------------------

func TestLiveSessionInfo_LiveOverlay(t *testing.T) {
	mc := newMockCmd()
	mc.set("overlayfs / overlay rw 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	info := svc.LiveSessionInfo(context.Background())
	if !info.IsLive {
		t.Error("IsLive = false, want true for overlay root")
	}
	if info.RootDev != "overlayfs" {
		t.Errorf("RootDev = %q, want overlayfs", info.RootDev)
	}
}

func TestLiveSessionInfo_LiveSquashfs(t *testing.T) {
	mc := newMockCmd()
	mc.set("/dev/loop0 / squashfs ro 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	info := svc.LiveSessionInfo(context.Background())
	if !info.IsLive {
		t.Error("IsLive = false, want true for squashfs root")
	}
}

func TestLiveSessionInfo_Installed(t *testing.T) {
	mc := newMockCmd()
	mc.set("/dev/sda2 / ext4 rw 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	info := svc.LiveSessionInfo(context.Background())
	if info.IsLive {
		t.Error("IsLive = true, want false for installed ext4 root")
	}
}

func TestHandleLiveSession_Live(t *testing.T) {
	mc := newMockCmd()
	mc.set("overlay / overlay rw 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/live-session", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp LiveSession
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.IsLive {
		t.Error("is_live = false, want true")
	}
}

func TestHandleLiveSession_Installed(t *testing.T) {
	mc := newMockCmd()
	mc.set("/dev/nvme0n1p2 / ext4 rw 0 0\n", nil, "cat", "/proc/mounts")

	svc := newWithCommander(mc)
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/installer/live-session", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var resp LiveSession
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.IsLive {
		t.Error("is_live = true, want false for installed system")
	}
}

func TestHandleLiveSession_MethodNotAllowed(t *testing.T) {
	svc := newWithCommander(newMockCmd())
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/installer/live-session", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}
