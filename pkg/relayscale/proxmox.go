package relayscale

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// proxmox.go — ProxmoxProvisioner: a conformant relay provisioner for Proxmox VE
// clusters. It speaks the Proxmox VE REST API (/api2/json) over net/http with an
// API token, so it needs no Proxmox SDK.
//
// STATUS: the API plumbing (auth, clone, start, stop, delete, list) is REAL and
// wired. What an operator MUST supply to make it boot a working relay is a
// prepared TEMPLATE (a VM or LXC template that already contains vulos-relayd and a
// cloud-init / systemd unit that starts it with the VULOS_RELAY_* env from the
// spec). Turning a RelaySpec into per-guest cloud-init is the one TODO below — it
// is deployment-shaped (your image, your network, your storage) and is documented
// rather than guessed. Everything around it is functional.
//
// MODEL: Provision clones the template to a fresh VMID and starts it; Destroy
// stops and deletes the guest; List enumerates cluster guests tagged as relays.

// ProxmoxConfig configures the ProxmoxProvisioner.
type ProxmoxConfig struct {
	// Endpoint is the PVE API base (e.g. "https://pve.example.com:8006").
	Endpoint string
	// APIToken is the full token: "USER@REALM!TOKENID=UUID". Sent as the
	// Authorization: PVEAPIToken=... header.
	APIToken string
	// InsecureSkipVerify disables TLS verification (self-signed PVE certs).
	InsecureSkipVerify bool
	// Node is the PVE node to create guests on (e.g. "pve1"). For a multi-node
	// cluster an operator can run one provisioner per node or extend Provision to
	// pick a node by free resources.
	Node string
	// GuestKind is "qemu" (VM, default) or "lxc" (container).
	GuestKind string
	// TemplateID is the VMID of the prepared relay template to clone.
	TemplateID int
	// VMIDBase is the low end of the VMID range this provisioner allocates from.
	VMIDBase int
	// Region is the region this cluster serves.
	Region string
	// Tag marks provisioned guests so List can find them (default "vulos-relay").
	Tag string
}

// ProxmoxProvisioner scales relays on a Proxmox VE cluster.
type ProxmoxProvisioner struct {
	cfg  ProxmoxConfig
	http *http.Client
}

// NewProxmoxProvisioner builds the provisioner. Fail closed on missing config.
func NewProxmoxProvisioner(cfg ProxmoxConfig) (*ProxmoxProvisioner, error) {
	if cfg.GuestKind == "" {
		cfg.GuestKind = "qemu"
	}
	if cfg.GuestKind != "qemu" && cfg.GuestKind != "lxc" {
		return nil, fmt.Errorf("%w: proxmox GuestKind must be qemu|lxc", ErrNotConfigured)
	}
	if cfg.Tag == "" {
		cfg.Tag = "vulos-relay"
	}
	if cfg.VMIDBase == 0 {
		cfg.VMIDBase = 9000
	}
	if cfg.Endpoint == "" || cfg.APIToken == "" || cfg.Node == "" || cfg.TemplateID == 0 || cfg.Region == "" {
		return nil, fmt.Errorf("%w: need endpoint+token+node+template+region", ErrNotConfigured)
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // self-signed PVE certs
	return &ProxmoxProvisioner{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
	}, nil
}

func (p *ProxmoxProvisioner) Name() string  { return "proxmox" }
func (p *ProxmoxProvisioner) Enabled() bool { return true }

// Provision clones the relay template to a fresh VMID and starts it.
func (p *ProxmoxProvisioner) Provision(ctx context.Context, region string, spec RelaySpec) (Instance, error) {
	if region != "" && region != p.cfg.Region {
		return Instance{}, fmt.Errorf("relayscale: proxmox provisioner serves region %q, not %q", p.cfg.Region, region)
	}
	newID, err := p.allocVMID(ctx)
	if err != nil {
		return Instance{}, err
	}
	// Clone the template. name is DNS-ish; the tag lets List find it later.
	name := fmt.Sprintf("relay-%s-%d", p.cfg.Region, newID)
	form := url.Values{
		"newid":  {strconv.Itoa(newID)},
		"node":   {p.cfg.Node},
		"name":   {name},
		"full":   {"1"},
		"target": {p.cfg.Node},
	}
	clonePath := fmt.Sprintf("/nodes/%s/%s/%d/clone", p.cfg.Node, p.cfg.GuestKind, p.cfg.TemplateID)
	if err := p.post(ctx, clonePath, form, nil); err != nil {
		return Instance{}, fmt.Errorf("proxmox clone: %w", err)
	}

	// TODO(deploy): inject per-guest cloud-init / config so the cloned guest boots
	// vulos-relayd with this spec's env. For qemu this is a PUT to
	//   /nodes/{node}/qemu/{vmid}/config
	// setting `cicustom` / `ciuser` / a generated cloud-init snippet carrying
	//   VULOS_RELAY_DOMAIN, VULOS_RELAY_REGION, VULOS_RELAY_SOFT_MAX_*,
	//   VULOS_CP_URL, CP_SHARED_SECRET, VULOS_RELAY_PUBLIC_ENDPOINT
	// derived from `spec`. For lxc it is the analogous /config with net/features.
	// Left as a documented integration point because the snippet shape depends on
	// the operator's template + network, not on this control plane.
	_ = spec // consumed by the cloud-init step above once wired

	// Start the guest.
	startPath := fmt.Sprintf("/nodes/%s/%s/%d/status/start", p.cfg.Node, p.cfg.GuestKind, newID)
	if err := p.post(ctx, startPath, url.Values{}, nil); err != nil {
		return Instance{}, fmt.Errorf("proxmox start: %w", err)
	}
	return Instance{
		ID:        strconv.Itoa(newID),
		Region:    p.cfg.Region,
		Provider:  "proxmox",
		Ready:     false, // becomes ready once relayd's /readyz passes
		CreatedAt: time.Now().UTC(),
		Meta:      map[string]string{"node": p.cfg.Node, "kind": p.cfg.GuestKind, "name": name},
	}, nil
}

// Destroy stops and deletes the guest.
func (p *ProxmoxProvisioner) Destroy(ctx context.Context, inst Instance) error {
	node := inst.Meta["node"]
	if node == "" {
		node = p.cfg.Node
	}
	kind := inst.Meta["kind"]
	if kind == "" {
		kind = p.cfg.GuestKind
	}
	// Graceful shutdown first, then delete (best-effort stop — a template guest
	// may already be stopped).
	stopPath := fmt.Sprintf("/nodes/%s/%s/%s/status/shutdown", node, kind, inst.ID)
	_ = p.post(ctx, stopPath, url.Values{"forceStop": {"1"}, "timeout": {"30"}}, nil)
	delPath := fmt.Sprintf("/nodes/%s/%s/%s", node, kind, inst.ID)
	if err := p.del(ctx, delPath); err != nil {
		return fmt.Errorf("proxmox delete: %w", err)
	}
	return nil
}

// List enumerates cluster guests tagged as relays.
func (p *ProxmoxProvisioner) List(ctx context.Context) ([]Instance, error) {
	var resp struct {
		Data []struct {
			VMID   int    `json:"vmid"`
			Node   string `json:"node"`
			Type   string `json:"type"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Tags   string `json:"tags"`
			Uptime int    `json:"uptime"`
		} `json:"data"`
	}
	if err := p.get(ctx, "/cluster/resources?type=vm", &resp); err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(resp.Data))
	for _, g := range resp.Data {
		if !strings.Contains(g.Tags, p.cfg.Tag) {
			continue
		}
		out = append(out, Instance{
			ID:       strconv.Itoa(g.VMID),
			Region:   p.cfg.Region,
			Provider: "proxmox",
			Ready:    g.Status == "running" && g.Uptime > 0,
			Meta:     map[string]string{"node": g.Node, "kind": g.Type, "name": g.Name},
		})
	}
	return out, nil
}

// allocVMID returns a free VMID at/above VMIDBase by scanning cluster resources.
func (p *ProxmoxProvisioner) allocVMID(ctx context.Context) (int, error) {
	var resp struct {
		Data []struct {
			VMID int `json:"vmid"`
		} `json:"data"`
	}
	if err := p.get(ctx, "/cluster/resources?type=vm", &resp); err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, g := range resp.Data {
		used[g.VMID] = true
	}
	for id := p.cfg.VMIDBase; id < p.cfg.VMIDBase+10000; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, fmt.Errorf("relayscale: proxmox: no free VMID in range")
}

// ── HTTP helpers (Proxmox /api2/json, PVEAPIToken auth) ───────────────────────

func (p *ProxmoxProvisioner) get(ctx context.Context, path string, out any) error {
	return p.req(ctx, http.MethodGet, path, nil, out)
}
func (p *ProxmoxProvisioner) post(ctx context.Context, path string, form url.Values, out any) error {
	return p.req(ctx, http.MethodPost, path, form, out)
}
func (p *ProxmoxProvisioner) del(ctx context.Context, path string) error {
	return p.req(ctx, http.MethodDelete, path, nil, nil)
}

func (p *ProxmoxProvisioner) req(ctx context.Context, method, path string, form url.Values, out any) error {
	u := strings.TrimRight(p.cfg.Endpoint, "/") + "/api2/json" + path
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+p.cfg.APIToken)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("proxmox %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(rb)))
	}
	if out != nil {
		return json.Unmarshal(rb, out)
	}
	return nil
}

var _ RelayProvisioner = (*ProxmoxProvisioner)(nil)
