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

// Init sets up the shared bridge interface.
func (m *Manager) Init(ctx context.Context) error {
	// Create bridge if it doesn't exist
	if err := run(ctx, "ip", "link", "show", m.bridge); err != nil {
		if err := run(ctx, "ip", "link", "add", m.bridge, "type", "bridge"); err != nil {
			return fmt.Errorf("create bridge: %w", err)
		}
		if err := run(ctx, "ip", "addr", "add", "10.200.0.1/16", "dev", m.bridge); err != nil {
			return fmt.Errorf("assign bridge IP: %w", err)
		}
		if err := run(ctx, "ip", "link", "set", m.bridge, "up"); err != nil {
			return fmt.Errorf("bring up bridge: %w", err)
		}
	}

	// Enable IP forwarding
	if err := run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		log.Printf("[appnet] warning: could not enable ip_forward: %v", err)
	}

	// Masquerade for outbound from namespaces
	run(ctx, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.200.0.0/16", "-j", "MASQUERADE")

	log.Printf("[appnet] bridge %s initialized", m.bridge)
	return nil
}

// Create sets up a network namespace for an app.
func (m *Manager) Create(ctx context.Context, appID string, hostPort, appPort int) (*Namespace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ns, ok := m.namespaces[appID]; ok {
		return ns, nil // already exists
	}

	m.subnet++
	octet := m.subnet

	ns := &Namespace{
		Name:     fmt.Sprintf("vulos_%s", appID),
		AppID:    appID,
		HostPort: hostPort,
		AppPort:  appPort,
		VethHost: fmt.Sprintf("vh_%s", appID),
		VethNS:   fmt.Sprintf("vn_%s", appID),
		HostIP:   fmt.Sprintf("10.200.%d.1", octet),
		NSIP:     fmt.Sprintf("10.200.%d.2", octet),
	}

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

		// 4. Attach host end to bridge
		{"attach to bridge", []string{"ip", "link", "set", ns.VethHost, "master", m.bridge}},

		// 5. Configure host-side veth
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

		// Block inter-namespace traffic — apps can't reach each other directly
		// They must go through the gateway at :8080/app/{appId}/
		{"block inter-ns", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-A", "OUTPUT",
			"-d", "10.200.0.0/16", "-j", "DROP"}},

		// But allow the namespace to reach the gateway (host bridge IP)
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

	// Remove bridge
	run(ctx, "ip", "link", "del", m.bridge)
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

// Get returns a specific namespace.
func (m *Manager) Get(appID string) (*Namespace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns, ok := m.namespaces[appID]
	return ns, ok
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
