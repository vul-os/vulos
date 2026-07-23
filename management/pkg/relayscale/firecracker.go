package relayscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// firecracker.go — FirecrackerProvisioner: a conformant relay provisioner that
// boots relayd inside Firecracker microVMs. It talks to the Firecracker API over
// its per-VM unix socket (HTTP-over-UDS via a custom net/http transport), which is
// the real control surface — no SDK needed.
//
// STATUS: the API sequence to configure + start a microVM (boot-source, rootfs
// drive, machine-config, network, InstanceStart) is wired against the real
// Firecracker HTTP API. What an operator MUST supply is the host-side plumbing
// that is inherently deployment-specific and therefore left as clearly-marked
// TODOs: (a) launching the firecracker/jailer PROCESS with a fresh API socket per
// VM, and (b) preparing the tap device + rootfs image carrying vulos-relayd. This
// keeps the provisioner honest — it satisfies the interface and drives the real
// API, and the remaining integration points are documented, not faked.
//
// MODEL: Provision boots one microVM and records it in an on-disk registry;
// Destroy sends InstanceHalt (via the API) then removes the socket + registry
// entry; List reads the registry. The registry is a small JSON file so relay
// inventory survives a control-plane restart.

// FirecrackerConfig configures the FirecrackerProvisioner.
type FirecrackerConfig struct {
	// KernelImagePath is the uncompressed vmlinux the microVMs boot.
	KernelImagePath string
	// RootDrivePath is the base rootfs image (ext4) containing vulos-relayd. Each
	// VM gets a copy (TODO: copy-on-write via a per-VM overlay).
	RootDrivePath string
	// BootArgs are the kernel cmdline (e.g. "console=ttyS0 reboot=k panic=1 pci=off").
	BootArgs string
	// VCPUs / MemMiB size each microVM.
	VCPUs  int
	MemMiB int
	// RunDir holds per-VM API sockets + the instance registry.
	RunDir string
	// Region this host serves.
	Region string
	// FirecrackerBin is the firecracker binary path (for the process-launch TODO).
	FirecrackerBin string
}

// FirecrackerProvisioner boots relay microVMs on the local host.
type FirecrackerProvisioner struct {
	cfg FirecrackerConfig
	mu  sync.Mutex // guards the registry file
}

// NewFirecrackerProvisioner builds the provisioner. Fail closed on missing config.
func NewFirecrackerProvisioner(cfg FirecrackerConfig) (*FirecrackerProvisioner, error) {
	if cfg.VCPUs == 0 {
		cfg.VCPUs = 1
	}
	if cfg.MemMiB == 0 {
		cfg.MemMiB = 256
	}
	if cfg.BootArgs == "" {
		cfg.BootArgs = "console=ttyS0 reboot=k panic=1 pci=off"
	}
	if cfg.FirecrackerBin == "" {
		cfg.FirecrackerBin = "firecracker"
	}
	if cfg.KernelImagePath == "" || cfg.RootDrivePath == "" || cfg.RunDir == "" || cfg.Region == "" {
		return nil, fmt.Errorf("%w: need kernel+rootfs+rundir+region", ErrNotConfigured)
	}
	if err := os.MkdirAll(cfg.RunDir, 0o750); err != nil {
		return nil, fmt.Errorf("%w: rundir: %v", ErrNotConfigured, err)
	}
	return &FirecrackerProvisioner{cfg: cfg}, nil
}

func (f *FirecrackerProvisioner) Name() string  { return "firecracker" }
func (f *FirecrackerProvisioner) Enabled() bool { return true }

// socketPath is the per-VM Firecracker API socket.
func (f *FirecrackerProvisioner) socketPath(id string) string {
	return filepath.Join(f.cfg.RunDir, id+".sock")
}

// Provision boots one relay microVM.
func (f *FirecrackerProvisioner) Provision(ctx context.Context, region string, spec RelaySpec) (Instance, error) {
	if region != "" && region != f.cfg.Region {
		return Instance{}, fmt.Errorf("relayscale: firecracker provisioner serves region %q, not %q", f.cfg.Region, region)
	}
	id := fmt.Sprintf("fc-%s-%d", f.cfg.Region, time.Now().UnixNano())
	sock := f.socketPath(id)

	// TODO(deploy): launch the firecracker (or jailer) PROCESS bound to `sock`
	// before the API calls below. e.g.:
	//   cmd := exec.CommandContext(ctx, f.cfg.FirecrackerBin, "--api-sock", sock)
	//   cmd.Start()  // and track the pid in the registry for Destroy
	// Left out because host process supervision (cgroups, jailer chroot, tap
	// device creation) is host-specific and typically owned by a unit manager.
	// Without this the API calls will fail to dial the socket — which is the
	// correct fail-closed behaviour until the host plumbing is wired.

	client := udsClient(sock)

	// 1) machine-config (vcpus + mem).
	mc := map[string]any{"vcpu_count": f.cfg.VCPUs, "mem_size_mib": f.cfg.MemMiB, "smt": false}
	if err := fcPut(ctx, client, "/machine-config", mc); err != nil {
		return Instance{}, fmt.Errorf("firecracker machine-config: %w", err)
	}
	// 2) boot-source (kernel + cmdline). The relay env is passed via the rootfs's
	// init (cloud-init-style) or an appended cmdline — TODO(deploy): thread
	// spec.Domain / spec.CPURL etc. into the guest's relayd unit here.
	bs := map[string]any{"kernel_image_path": f.cfg.KernelImagePath, "boot_args": f.cfg.BootArgs}
	if err := fcPut(ctx, client, "/boot-source", bs); err != nil {
		return Instance{}, fmt.Errorf("firecracker boot-source: %w", err)
	}
	// 3) rootfs drive. TODO(deploy): give each VM a per-VM overlay of RootDrivePath
	// rather than sharing the base image read-write.
	drive := map[string]any{
		"drive_id": "rootfs", "path_on_host": f.cfg.RootDrivePath,
		"is_root_device": true, "is_read_only": false,
	}
	if err := fcPut(ctx, client, "/drives/rootfs", drive); err != nil {
		return Instance{}, fmt.Errorf("firecracker drive: %w", err)
	}
	// 4) network-interface. TODO(deploy): create a host tap device and reference it
	// here so the relay is reachable; omitted because tap creation is host-side.
	_ = spec

	// 5) start.
	if err := fcPut(ctx, client, "/actions", map[string]any{"action_type": "InstanceStart"}); err != nil {
		return Instance{}, fmt.Errorf("firecracker start: %w", err)
	}

	inst := Instance{
		ID:        id,
		Region:    f.cfg.Region,
		Provider:  "firecracker",
		Ready:     false,
		CreatedAt: time.Now().UTC(),
		Meta:      map[string]string{"socket": sock},
	}
	if err := f.registryAdd(inst); err != nil {
		return Instance{}, err
	}
	return inst, nil
}

// Destroy halts the microVM and removes its registry entry + socket.
func (f *FirecrackerProvisioner) Destroy(ctx context.Context, inst Instance) error {
	sock := inst.Meta["socket"]
	if sock == "" {
		sock = f.socketPath(inst.ID)
	}
	// Best-effort graceful halt via the API; the VM may already be gone.
	_ = fcPut(ctx, udsClient(sock), "/actions", map[string]any{"action_type": "SendCtrlAltDel"})
	// TODO(deploy): also kill the tracked firecracker/jailer pid + tear down the
	// tap device here.
	_ = os.Remove(sock)
	return f.registryDel(inst.ID)
}

// List reads the on-disk registry.
func (f *FirecrackerProvisioner) List(_ context.Context) ([]Instance, error) {
	return f.registryLoad()
}

// ── on-disk registry (JSON file under RunDir) ─────────────────────────────────

func (f *FirecrackerProvisioner) registryFile() string {
	return filepath.Join(f.cfg.RunDir, "instances.json")
}

func (f *FirecrackerProvisioner) registryLoad() ([]Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadLocked()
}

func (f *FirecrackerProvisioner) loadLocked() ([]Instance, error) {
	b, err := os.ReadFile(f.registryFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var insts []Instance
	if err := json.Unmarshal(b, &insts); err != nil {
		return nil, err
	}
	return insts, nil
}

func (f *FirecrackerProvisioner) saveLocked(insts []Instance) error {
	sort.Slice(insts, func(i, j int) bool { return insts[i].ID < insts[j].ID })
	b, err := json.MarshalIndent(insts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.registryFile(), b, 0o640)
}

func (f *FirecrackerProvisioner) registryAdd(inst Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	insts, err := f.loadLocked()
	if err != nil {
		return err
	}
	insts = append(insts, inst)
	return f.saveLocked(insts)
}

func (f *FirecrackerProvisioner) registryDel(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	insts, err := f.loadLocked()
	if err != nil {
		return err
	}
	out := insts[:0]
	for _, i := range insts {
		if i.ID != id {
			out = append(out, i)
		}
	}
	return f.saveLocked(out)
}

// ── Firecracker HTTP-over-unix-socket helpers ─────────────────────────────────

// udsClient returns an http.Client whose transport dials the given unix socket
// for every request, so a standard http://localhost URL reaches the Firecracker
// API on `sock`.
func udsClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
}

// fcPut issues a Firecracker PUT with a JSON body to the given API path.
func fcPut(ctx context.Context, client *http.Client, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("firecracker PUT %s: %s: %s", path, resp.Status, strings.TrimSpace(string(rb)))
	}
	return nil
}

var _ RelayProvisioner = (*FirecrackerProvisioner)(nil)
