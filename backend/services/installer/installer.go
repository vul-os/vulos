// Package installer provides HTTP handlers for the Vula OS bare-metal
// installer.  It exposes four endpoints:
//
//	GET  /api/installer/disks    — list block devices (lsblk -J)
//	GET  /api/installer/status   — live-USB vs installed
//	POST /api/installer/install  — partition → format → rsync → bootctl
//	GET  /api/installer/progress — WebSocket streaming install %
//
// Route wiring is left to the caller via RegisterHandlers so that this package
// can be imported without touching cmd/server/main.go.
package installer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/wsutil"
)

// ---------------------------------------------------------------------------
// Command abstraction — real exec vs mock for tests
// ---------------------------------------------------------------------------

// Commander runs external commands.  The real implementation shells out;
// tests inject a fake.
type Commander interface {
	// Output runs cmd with args and returns combined stdout+stderr, or an
	// error if the process exits non-zero.
	Output(ctx context.Context, cmd string, args ...string) ([]byte, error)

	// RunLines runs cmd with args, writing stdout lines to w as they arrive.
	// It returns when the command exits.
	RunLines(ctx context.Context, w io.Writer, cmd string, args ...string) error
}

// osCommander delegates to os/exec.
type osCommander struct{}

func (o *osCommander) Output(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, cmd, args...).Output()
}

func (o *osCommander) RunLines(ctx context.Context, w io.Writer, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Stdout = w
	return c.Run()
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Partition is a child partition as reported by lsblk.
type Partition struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Type string `json:"type"`
}

// Disk is a block device as reported by lsblk.
type Disk struct {
	Name       string      `json:"name"`
	Size       string      `json:"size"`
	Model      string      `json:"model"`
	Partitions []Partition `json:"partitions"`
}

// StatusResponse is the payload for GET /api/installer/status.
type StatusResponse struct {
	// Mode is "live" (squashfs/overlay root, i.e. live USB) or "installed".
	Mode    string `json:"mode"`
	RootDev string `json:"root_dev,omitempty"`
}

// InstallRequest is the JSON body for POST /api/installer/install.
type InstallRequest struct {
	// Disk is the block device name (e.g. "sda", "nvme0n1").
	Disk string `json:"disk"`
	// Confirm must be true; absent/false rejects the request so an
	// accidental POST never triggers a destructive operation.
	Confirm bool `json:"confirm"`
}

// ---------------------------------------------------------------------------
// Progress hub
// ---------------------------------------------------------------------------

// progressHub broadcasts install progress (0–100) to all connected WebSocket
// clients.  -1 is the sentinel meaning "done".
type progressHub struct {
	mu          sync.Mutex
	subscribers []chan int
	latest      int
	done        bool
	doneErr     error
}

func newProgressHub() *progressHub { return &progressHub{} }

func (h *progressHub) subscribe() chan int {
	ch := make(chan int, 64)
	h.mu.Lock()
	ch <- h.latest // send current value immediately
	h.subscribers = append(h.subscribers, ch)
	h.mu.Unlock()
	return ch
}

func (h *progressHub) unsubscribe(ch chan int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.subscribers {
		if s == ch {
			h.subscribers = append(h.subscribers[:i], h.subscribers[i+1:]...)
			return
		}
	}
}

// publish fans out pct to every subscriber.  -1 signals completion.
func (h *progressHub) publish(pct int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pct >= 0 && pct > h.latest {
		h.latest = pct
	}
	for _, ch := range h.subscribers {
		select {
		case ch <- pct:
		default:
		}
	}
}

func (h *progressHub) setDone(err error) {
	h.mu.Lock()
	h.done = true
	h.doneErr = err
	h.mu.Unlock()
	h.publish(-1)
}

func (h *progressHub) isDone() (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done, h.doneErr
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// Service holds dependencies and mutable installation state.
type Service struct {
	cmd      Commander
	mu       sync.Mutex
	hub      *progressHub
	// isAdmin is called for the destructive /install endpoint to gate access to
	// admin users only.  If nil, no auth check is performed (useful in tests;
	// the live-USB flow always supplies a real check via cmd/server/main.go).
	isAdmin  func(r *http.Request) bool
}

// New returns a Service backed by real OS commands.
func New() *Service {
	return &Service{cmd: &osCommander{}}
}

// NewWithAdminGate returns a Service backed by real OS commands and wired with
// an admin-role predicate that guards POST /api/installer/install.
func NewWithAdminGate(isAdmin func(r *http.Request) bool) *Service {
	return &Service{cmd: &osCommander{}, isAdmin: isAdmin}
}

// newWithCommander creates a Service using the provided Commander (for tests).
func newWithCommander(c Commander) *Service {
	return &Service{cmd: c}
}

// RegisterHandlers wires installer endpoints onto mux.
// Call this from wherever your http.ServeMux is configured.
func RegisterHandlers(mux *http.ServeMux, svc *Service) {
	mux.HandleFunc("/api/installer/disks", svc.handleDisks)
	mux.HandleFunc("/api/installer/status", svc.handleStatus)
	mux.HandleFunc("/api/installer/install", svc.handleInstall)
	mux.HandleFunc("/api/installer/progress", svc.handleProgress)
	mux.HandleFunc("/api/installer/live-session", svc.handleLiveSession)
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (s *Service) handleDisks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	disks, err := s.listDisks(r.Context())
	if err != nil {
		log.Printf("[installer] listDisks: %v", err)
		http.Error(w, "failed to list disks", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(disks)
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := s.detectStatus(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Service) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// SEC: disk wipe and OS installation is a destructive privileged operation.
	// Gate on admin role when the service has been wired with an isAdmin predicate.
	if s.isAdmin != nil && !s.isAdmin(r) {
		http.Error(w, `{"error":"admin only"}`, http.StatusForbidden)
		return
	}

	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "confirm must be true", http.StatusBadRequest)
		return
	}
	if !validDiskName(req.Disk) {
		http.Error(w, "invalid or missing disk name", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.hub != nil {
		done, _ := s.hub.isDone()
		if !done {
			s.mu.Unlock()
			http.Error(w, "installation already in progress", http.StatusConflict)
			return
		}
	}
	hub := newProgressHub()
	s.hub = hub
	s.mu.Unlock()

	go s.runInstall(req.Disk, hub)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// handleProgress upgrades to WebSocket and streams progress messages until
// the installation completes or the client disconnects.
func (s *Service) handleProgress(w http.ResponseWriter, r *http.Request) {
	conn, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[installer] ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	// Wait for an install to start (hub may be nil before the first POST).
	var hub *progressHub
	for {
		s.mu.Lock()
		hub = s.hub
		s.mu.Unlock()
		if hub != nil {
			break
		}
		// Poll every 200 ms; bail if the client goes away.
		conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, _, e := conn.NextReader(); e == nil {
			// client sent something unexpected or closed cleanly
			return
		}
		conn.SetReadDeadline(time.Time{})
	}

	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	type msg struct {
		Percent int    `json:"percent"`
		Done    bool   `json:"done,omitempty"`
		Error   string `json:"error,omitempty"`
	}

	for pct := range ch {
		if pct < 0 {
			// Installation finished.
			_, hubErr := hub.isDone()
			m := msg{Percent: 100, Done: true}
			if hubErr != nil {
				m.Error = hubErr.Error()
			}
			conn.WriteJSON(m)
			return
		}
		if err := conn.WriteJSON(msg{Percent: pct}); err != nil {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Disk listing
// ---------------------------------------------------------------------------

type lsblkRoot struct {
	Blockdevices []lsblkDev `json:"blockdevices"`
}

type lsblkDev struct {
	Name     string     `json:"name"`
	Size     string     `json:"size"`
	Model    *string    `json:"model"`
	Type     string     `json:"type"`
	Children []lsblkDev `json:"children"`
}

func (s *Service) listDisks(ctx context.Context) ([]Disk, error) {
	out, err := s.cmd.Output(ctx, "lsblk", "-J", "-o", "NAME,SIZE,MODEL,TYPE")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	return parseLsblkJSON(out)
}

func parseLsblkJSON(data []byte) ([]Disk, error) {
	var root lsblkRoot
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	var disks []Disk
	for _, d := range root.Blockdevices {
		if d.Type != "disk" {
			continue
		}
		disk := Disk{Name: d.Name, Size: d.Size}
		if d.Model != nil {
			disk.Model = strings.TrimSpace(*d.Model)
		}
		for _, c := range d.Children {
			disk.Partitions = append(disk.Partitions, Partition{
				Name: c.Name,
				Size: c.Size,
				Type: c.Type,
			})
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

// ---------------------------------------------------------------------------
// Status detection
// ---------------------------------------------------------------------------

func (s *Service) detectStatus(ctx context.Context) StatusResponse {
	out, err := s.cmd.Output(ctx, "cat", "/proc/mounts")
	if err != nil {
		return StatusResponse{Mode: "live"} // safe default
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[1] != "/" {
			continue
		}
		dev, fsType := fields[0], fields[2]
		if fsType == "squashfs" || strings.Contains(fsType, "overlay") ||
			strings.Contains(dev, "overlay") {
			return StatusResponse{Mode: "live", RootDev: dev}
		}
		return StatusResponse{Mode: "installed", RootDev: dev}
	}
	// Fallback: check for live-medium marker.
	if _, e := s.cmd.Output(ctx, "test", "-e", "/run/live/medium"); e == nil {
		return StatusResponse{Mode: "live"}
	}
	return StatusResponse{Mode: "installed"}
}

// ---------------------------------------------------------------------------
// Installation pipeline
// ---------------------------------------------------------------------------

const targetMount = "/mnt/vulos-install"

// partSuffix returns "p1"/"p2" for NVMe/eMMC devices, "1"/"2" otherwise.
func partSuffix(disk string, n int) string {
	if strings.HasPrefix(disk, "nvme") || strings.HasPrefix(disk, "mmcblk") {
		return fmt.Sprintf("p%d", n)
	}
	return strconv.Itoa(n)
}

func (s *Service) runInstall(disk string, hub *progressHub) {
	dev := "/dev/" + disk
	espPart := dev + partSuffix(disk, 1)
	rootPart := dev + partSuffix(disk, 2)
	ctx := context.Background()

	type step struct {
		name string
		pct  int // hub progress after this step completes (rsync drives its own)
		fn   func() error
	}

	steps := []step{
		{name: "partition", pct: 5, fn: func() error { return s.partition(ctx, dev) }},
		{name: "format-esp", pct: 8, fn: func() error { return s.formatESP(ctx, espPart) }},
		{name: "format-root", pct: 12, fn: func() error { return s.formatRoot(ctx, rootPart) }},
		{name: "mount", pct: 15, fn: func() error { return s.mountTargets(ctx, espPart, rootPart) }},
		// rsync drives hub progress from 15→93 internally.
		{name: "rsync", pct: 93, fn: func() error { return s.rsyncRootfs(ctx, hub) }},
		{name: "fstab", pct: 95, fn: func() error { return s.writeFstab(ctx, espPart, rootPart) }},
		{name: "bootctl", pct: 99, fn: func() error { return s.installBootloader(ctx) }},
		{name: "unmount", pct: 100, fn: func() error { return s.unmount(ctx) }},
	}

	hub.publish(0)

	for i, st := range steps {
		log.Printf("[installer] step %d/%d: %s", i+1, len(steps), st.name)
		if err := st.fn(); err != nil {
			log.Printf("[installer] %s failed: %v", st.name, err)
			hub.setDone(fmt.Errorf("%s: %w", st.name, err))
			return
		}
		hub.publish(st.pct)
	}

	hub.setDone(nil)
}

// partition creates a GPT with a 512 MiB ESP and a root partition for the rest.
func (s *Service) partition(ctx context.Context, dev string) error {
	_, err := s.cmd.Output(ctx, "parted", "-s", dev,
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", "513MiB", "100%",
	)
	return err
}

func (s *Service) formatESP(ctx context.Context, part string) error {
	_, err := s.cmd.Output(ctx, "mkfs.fat", "-F32", "-n", "EFI", part)
	return err
}

func (s *Service) formatRoot(ctx context.Context, part string) error {
	_, err := s.cmd.Output(ctx, "mkfs.ext4", "-F", "-L", "vulos-root", part)
	return err
}

func (s *Service) mountTargets(ctx context.Context, espPart, rootPart string) error {
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", targetMount); err != nil {
		return err
	}
	if _, err := s.cmd.Output(ctx, "mount", rootPart, targetMount); err != nil {
		return err
	}
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", targetMount+"/boot/efi"); err != nil {
		return err
	}
	if _, err := s.cmd.Output(ctx, "mount", espPart, targetMount+"/boot/efi"); err != nil {
		return err
	}
	return nil
}

// rsyncProgressRE matches the percentage in rsync --info=progress2 output.
var rsyncProgressRE = regexp.MustCompile(`(\d{1,3})%`)

func (s *Service) rsyncRootfs(ctx context.Context, hub *progressHub) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		err := s.cmd.RunLines(ctx, pw,
			"rsync",
			"-aHAX",
			"--info=progress2",
			"--exclude=/proc",
			"--exclude=/sys",
			"--exclude=/dev",
			"--exclude=/run",
			"--exclude=/tmp",
			"--exclude=/mnt",
			"/",
			targetMount+"/",
		)
		pw.CloseWithError(err)
		errCh <- err
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		if m := rsyncProgressRE.FindStringSubmatch(scanner.Text()); m != nil {
			raw, _ := strconv.Atoi(m[1])
			// Map rsync 0-100 → hub 15-93.
			mapped := 15 + (raw*78)/100
			hub.publish(mapped)
		}
	}
	return <-errCh
}

func (s *Service) partUUID(ctx context.Context, part string) (string, error) {
	out, err := s.cmd.Output(ctx, "blkid", "-s", "UUID", "-o", "value", part)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Service) writeFstab(ctx context.Context, espPart, rootPart string) error {
	rootUUID, err := s.partUUID(ctx, rootPart)
	if err != nil {
		return fmt.Errorf("root UUID: %w", err)
	}
	espUUID, err := s.partUUID(ctx, espPart)
	if err != nil {
		return fmt.Errorf("esp UUID: %w", err)
	}

	fstab := fmt.Sprintf(
		"# Generated by Vula OS installer\n"+
			"UUID=%s  /          ext4  defaults,noatime  0 1\n"+
			"UUID=%s  /boot/efi  vfat  umask=0077        0 2\n",
		rootUUID, espUUID,
	)

	// Write via sh so the path handling works inside a chroot-less environment.
	_, err = s.cmd.Output(ctx, "sh", "-c",
		fmt.Sprintf("printf '%%s' %q > %s/etc/fstab", fstab, targetMount))
	return err
}

func (s *Service) installBootloader(ctx context.Context) error {
	_, err := s.cmd.Output(ctx, "bootctl",
		"--path="+targetMount+"/boot/efi",
		"--root="+targetMount,
		"install",
	)
	return err
}

func (s *Service) unmount(ctx context.Context) error {
	s.cmd.Output(ctx, "umount", targetMount+"/boot/efi") //nolint:errcheck
	s.cmd.Output(ctx, "umount", targetMount)             //nolint:errcheck
	return nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validDiskName accepts only alphanumeric characters to prevent path traversal
// and shell injection (e.g. "sda", "nvme0n1", "mmcblk0").
func validDiskName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
