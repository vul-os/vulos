package appnet

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// Namespace manages a network namespace for an app.
// Inside the namespace the app binds to whatever port it wants (usually 80 or 8080).
// Outside, traffic is forwarded from a host port via a veth pair + iptables NAT.
type Namespace struct {
	Name     string // namespace name, e.g., "vulos_calculator"
	AppID    string // registry app ID
	OwnerID  string // user ID who launched this app
	HostPort int    // port exposed on the host (e.g., 7070)
	AppPort  int    // port the app uses inside the namespace (e.g., 80)
	VethHost string // host-side veth name
	VethNS   string // namespace-side veth name
	HostIP   string // host-side veth IP (e.g., 10.200.1.1)
	NSIP     string // namespace-side veth IP (e.g., 10.200.1.2)
	Active   bool
}

// Manager handles the lifecycle of all app network namespaces.
type Manager struct {
	mu         sync.Mutex
	namespaces map[string]*Namespace
	bridge     string // bridge interface name
	subnet     int    // next subnet octet
}

// NewManager creates an app network manager.
// Requires iproute2 and iptables on the host.
func NewManager() *Manager {
	return &Manager{
		namespaces: make(map[string]*Namespace),
		bridge:     "vulos-br0",
		subnet:     1,
	}
}

// Init enables IP forwarding and masquerade for namespace outbound traffic.
// No bridge needed — each app gets a point-to-point veth pair.
func (m *Manager) Init(ctx context.Context) error {
	if err := run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		log.Printf("[appnet] warning: could not enable ip_forward: %v", err)
	}

	// Disable bridge netfilter — even without a bridge, the kernel module can interfere
	// with veth traffic if it was loaded by Docker or a previous run
	run(ctx, "sysctl", "-w", "net.bridge.bridge-nf-call-iptables=0")
	run(ctx, "sysctl", "-w", "net.bridge.bridge-nf-call-ip6tables=0")

	// Masquerade for outbound from namespaces (so apps can reach the internet)
	run(ctx, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.200.0.0/16", "-j", "MASQUERADE")

	log.Printf("[appnet] initialized (point-to-point veth isolation)")
	return nil
}

// Create sets up a network namespace for an app.
// In direct mode, skips namespace creation and runs the app on localhost:hostPort.
func (m *Manager) Create(ctx context.Context, appID, ownerID string, hostPort, appPort int) (*Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ns, ok := m.namespaces[appID]; ok {
		return ns, nil // already exists
	}

	// Stable subnet per app — hash the appID to get a deterministic octet (2-254)
	octet := 2
	for _, c := range appID {
		octet = (octet*31 + int(c)) % 253
	}
	octet += 2 // range 2-254

	// Interface names max 15 chars — use short hash of instanceID
	shortID := fmt.Sprintf("%x", octet)
	if len(appID) > 6 {
		// Use first 6 chars of a hash for uniqueness
		h := 0
		for _, c := range appID {
			h = h*31 + int(c)
		}
		shortID = fmt.Sprintf("%06x", h&0xFFFFFF)
	}

	ns := &Namespace{
		Name:     fmt.Sprintf("vulos_%s", appID),
		AppID:    appID,
		OwnerID:  ownerID,
		HostPort: hostPort,
		AppPort:  appPort,
		VethHost: fmt.Sprintf("vh_%s", shortID),
		VethNS:   fmt.Sprintf("vn_%s", shortID),
		HostIP:   fmt.Sprintf("10.200.%d.1", octet),
		NSIP:     fmt.Sprintf("10.200.%d.2", octet),
	}

	// Clean up any stale namespace/veth from previous run
	run(ctx, "ip", "link", "del", ns.VethHost)
	run(ctx, "ip", "netns", "del", ns.Name)

	steps := []struct {
		desc string
		args []string
	}{
		// 1. Create the namespace
		{"create netns", []string{"ip", "netns", "add", ns.Name}},

		// 2. Create veth pair
		{"create veth pair", []string{"ip", "link", "add", ns.VethHost, "type", "veth", "peer", "name", ns.VethNS}},

		// 3. Move one end into the namespace
		{"move veth to ns", []string{"ip", "link", "set", ns.VethNS, "netns", ns.Name}},

		// 4. Configure host-side veth (point-to-point, no bridge)
		{"host veth ip", []string{"ip", "addr", "add", ns.HostIP + "/24", "dev", ns.VethHost}},
		{"host veth up", []string{"ip", "link", "set", ns.VethHost, "up"}},

		// 6. Configure namespace-side networking
		{"ns veth ip", []string{"ip", "netns", "exec", ns.Name, "ip", "addr", "add", ns.NSIP + "/24", "dev", ns.VethNS}},
		{"ns veth up", []string{"ip", "netns", "exec", ns.Name, "ip", "link", "set", ns.VethNS, "up"}},
		{"ns loopback", []string{"ip", "netns", "exec", ns.Name, "ip", "link", "set", "lo", "up"}},
		{"ns default route", []string{"ip", "netns", "exec", ns.Name, "ip", "route", "add", "default", "via", ns.HostIP}},

		// 7. Port forward: ONLY from localhost (127.0.0.1) — apps unreachable from outside.
		// All external access must go through the auth gateway at :8080/app/{appId}/
		{"localhost DNAT", []string{"iptables", "-t", "nat", "-A", "OUTPUT",
			"-d", "127.0.0.1", "-p", "tcp", "--dport", fmt.Sprintf("%d", ns.HostPort),
			"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", ns.NSIP, ns.AppPort)}},

		// Block external access to app ports (anything not from loopback)
		{"block external", []string{"iptables", "-A", "INPUT",
			"-p", "tcp", "--dport", fmt.Sprintf("%d", ns.HostPort),
			"!", "-i", "lo", "-j", "DROP"}},

		// Allow established/related connections (so response packets to the host get through)
		{"allow established", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-A", "OUTPUT",
			"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}},

		// Block inter-namespace traffic — apps can't initiate connections to other namespaces
		{"block inter-ns", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-A", "OUTPUT",
			"-d", "10.200.0.0/16", "-j", "DROP"}},

		// Allow the namespace to reach the gateway (host)
		{"allow gateway", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-I", "OUTPUT",
			"-d", ns.HostIP, "-p", "tcp", "--dport", "8080", "-j", "ACCEPT"}},
	}

	for _, step := range steps {
		if err := run(ctx, step.args[0], step.args[1:]...); err != nil {
			// Try to clean up on failure
			m.destroy(ctx, ns)
			return nil, fmt.Errorf("%s: %w", step.desc, err)
		}
	}

	ns.Active = true
	m.namespaces[appID] = ns
	log.Printf("[appnet] namespace %s created: host:%d → %s:%d", ns.Name, ns.HostPort, ns.NSIP, ns.AppPort)
	return ns, nil
}

// Exec runs a command inside an app's network namespace.
func (m *Manager) Exec(ctx context.Context, appID string, cmd string, args ...string) *exec.Cmd {
	m.mu.Lock()
	ns, ok := m.namespaces[appID]
	m.mu.Unlock()

	if !ok {
		return nil
	}

	fullArgs := append([]string{"netns", "exec", ns.Name, cmd}, args...)
	return exec.CommandContext(ctx, "ip", fullArgs...)
}

// Destroy tears down a namespace and its networking.
func (m *Manager) Destroy(ctx context.Context, appID string) error {
	m.mu.Lock()
	ns, ok := m.namespaces[appID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("namespace %s not found", appID)
	}
	delete(m.namespaces, appID)
	m.mu.Unlock()

	return m.destroy(ctx, ns)
}

func (m *Manager) destroy(ctx context.Context, ns *Namespace) error {
	// Remove iptables rules
	run(ctx, "iptables", "-t", "nat", "-D", "OUTPUT",
		"-d", "127.0.0.1", "-p", "tcp", "--dport", fmt.Sprintf("%d", ns.HostPort),
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", ns.NSIP, ns.AppPort))
	run(ctx, "iptables", "-D", "INPUT",
		"-p", "tcp", "--dport", fmt.Sprintf("%d", ns.HostPort),
		"!", "-i", "lo", "-j", "DROP")

	// Remove veth (removing one end removes both)
	run(ctx, "ip", "link", "del", ns.VethHost)

	// Remove namespace
	if err := run(ctx, "ip", "netns", "del", ns.Name); err != nil {
		return fmt.Errorf("delete netns: %w", err)
	}

	ns.Active = false
	log.Printf("[appnet] namespace %s destroyed", ns.Name)
	return nil
}

// DestroyAll tears down everything on shutdown.
func (m *Manager) DestroyAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.namespaces))
	for id := range m.namespaces {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.Destroy(ctx, id)
	}
	log.Printf("[appnet] all namespaces destroyed")
}

// List returns all active namespaces.
func (m *Manager) List() []*Namespace {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*Namespace
	for _, ns := range m.namespaces {
		result = append(result, ns)
	}
	return result
}

// Get returns a specific namespace by exact key.
func (m *Manager) Get(key string) (*Namespace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, ok := m.namespaces[key]
	return ns, ok
}

// GetForUser finds a namespace for a given appID owned by the specified user.
func (m *Manager) GetForUser(appID, userID string) (*Namespace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Try exact instanceID first
	key := userID + "-" + appID
	if ns, ok := m.namespaces[key]; ok {
		return ns, true
	}
	// Search by owner + app suffix
	for _, ns := range m.namespaces {
		if ns.OwnerID == userID && strings.HasSuffix(ns.AppID, "-"+appID) {
			return ns, true
		}
	}
	return nil, false
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
