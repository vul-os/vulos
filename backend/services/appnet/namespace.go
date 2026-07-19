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

	// externalUpstreams holds ADOPT-A-PORT registrations: owner-scoped loopback
	// services the gateway proxies to as if they were namespaced apps. Keyed the
	// same way GetForProfile keys namespaces (userID + "-" + profileKey) so it
	// resolves in the SAME lookup, right after the real-namespace miss.
	externalUpstreams map[string]*ExternalUpstream
}

// metadataDenyCIDRv4 is the IPv4 link-local range that contains every cloud
// instance-metadata service (IMDS): AWS/GCP/Azure/OpenStack all answer on
// 169.254.169.254, and the whole 169.254.0.0/16 link-local block is never a
// legitimate egress target for a sandboxed app.  Dropping the range in each
// app's network namespace (SSRF-APPNET-01) prevents an installed native app
// from reaching cloud IMDS to steal instance credentials via the host's
// masquerade route.
const metadataDenyCIDRv4 = "169.254.0.0/16"

// metadataDenyCIDRv6 lists the IPv6 ranges that reach cloud metadata or are
// otherwise link-local: fe80::/10 (IPv6 link-local, covers ND) and
// fd00:ec2::254 (AWS IPv6 IMDS).  Applied best-effort — a namespace with IPv6
// disabled simply has no ip6tables to configure.
var metadataDenyCIDRv6 = []string{
	"fe80::/10",         // IPv6 link-local
	"fd00:ec2::254/128", // AWS IPv6 instance metadata
}

// nsStep is a single ordered command in the namespace-setup sequence.  Kept as
// a named type (rather than an anonymous struct) so the full ordered ruleset
// can be produced by a pure builder and unit-tested without root/iproute2.
type nsStep struct {
	desc string
	args []string
}

// net02ProfileKey returns the composite map key for a (profile, appID) pair.
// "default" profile maps to the bare appID for backwards-compatibility.
func net02ProfileKey(profile, appID string) string {
	if profile == "" || profile == "default" {
		return appID
	}
	return profile + ":" + appID
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

	for _, step := range namespaceSteps(ns) {
		if err := run(ctx, step.args[0], step.args[1:]...); err != nil {
			// Try to clean up on failure
			m.destroy(ctx, ns)
			return nil, fmt.Errorf("%s: %w", step.desc, err)
		}
	}

	// SSRF-APPNET-01 (IPv6, best-effort): drop egress to IPv6 link-local /
	// metadata ranges inside the namespace.  Applied outside the hard-failing
	// step loop because a namespace with IPv6 disabled has no ip6tables to
	// program — the IPv4 drop above is the load-bearing guard for cloud IMDS
	// (169.254.169.254), and this is defence-in-depth for IPv6-enabled hosts.
	for _, cidr := range metadataDenyCIDRv6 {
		if err := run(ctx, "ip", "netns", "exec", ns.Name,
			"ip6tables", "-I", "OUTPUT", "1", "-d", cidr, "-j", "DROP"); err != nil {
			log.Printf("[appnet] ns %s: ip6tables metadata drop for %s skipped: %v", ns.Name, cidr, err)
		}
	}

	ns.Active = true
	m.namespaces[appID] = ns
	log.Printf("[appnet] namespace %s created: host:%d → %s:%d", ns.Name, ns.HostPort, ns.NSIP, ns.AppPort)
	return ns, nil
}

// namespaceSteps returns the ordered command sequence that builds a namespace's
// networking and firewall rules.  It is a pure function of ns (no side effects)
// so the ruleset — in particular the SSRF-APPNET-01 metadata drop and its
// ordering relative to the ACCEPT rules — is unit-testable without root or
// iproute2.
func namespaceSteps(ns *Namespace) []nsStep {
	return []nsStep{
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

		// SSRF-APPNET-01: block egress to the cloud instance-metadata range
		// (169.254.0.0/16, incl. 169.254.169.254 IMDS) FIRST in the namespace
		// OUTPUT chain.  The default OUTPUT policy is ACCEPT, so without this an
		// installed app could reach the host's IMDS over the masquerade route
		// and exfiltrate instance credentials.  Inserted at the head (-I OUTPUT
		// 1) so it is evaluated before any subsequent rule, and neither the
		// established/related nor gateway ACCEPT rules match a NEW connection to
		// a metadata address anyway.
		{"block metadata (v4)", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-I", "OUTPUT", "1",
			"-d", metadataDenyCIDRv4, "-j", "DROP"}},

		// Allow established/related connections (so response packets to the host get through)
		{"allow established", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-A", "OUTPUT",
			"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}},

		// Block inter-namespace traffic — apps can't initiate connections to other namespaces
		{"block inter-ns", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-A", "OUTPUT",
			"-d", "10.200.0.0/16", "-j", "DROP"}},

		// Allow the namespace to reach the gateway (host).  Inserted at the head,
		// but only matches the host veth IP on :8080 — never a metadata address,
		// so it cannot shadow the metadata drop above.
		{"allow gateway", []string{"ip", "netns", "exec", ns.Name,
			"iptables", "-I", "OUTPUT",
			"-d", ns.HostIP, "-p", "tcp", "--dport", "8080", "-j", "ACCEPT"}},
	}
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
// Preserved for backwards-compatibility; equivalent to GetForProfile(appID, userID, "default").
func (m *Manager) GetForUser(appID, userID string) (*Namespace, bool) {
	return m.GetForProfile(appID, userID, "default")
}

// GetForProfile finds a namespace for a given appID + profile owned by the
// specified user.  Profile "default" (or "") retains backwards-compatible
// behaviour: the key is the bare userID-appID form so existing namespaces
// created before NET-02 are still found.
func (m *Manager) GetForProfile(appID, userID, profile string) (*Namespace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Compose the per-profile instance key the same way Launch does.
	// net02ProfileKey("default", appID) == appID, so the full key for the
	// default profile is userID + "-" + appID — unchanged from before NET-02.
	appKey := net02ProfileKey(profile, appID)
	key := userID + "-" + appKey
	if ns, ok := m.namespaces[key]; ok {
		return ns, true
	}
	// Fallback: search by owner + app suffix (handles old-style namespace keys
	// that predate the profile dimension or were created with raw instanceIDs).
	for _, ns := range m.namespaces {
		if ns.OwnerID == userID && strings.HasSuffix(ns.AppID, "-"+appKey) {
			return ns, true
		}
	}
	// ADOPT-A-PORT: after a real-namespace miss, resolve an owner-scoped external
	// (loopback) upstream. Returning a synthetic namespace here means the gateway
	// proxies to the adopted port through its full auth/entitlement/header/rate
	// pipeline with zero gateway changes — no new trust path.
	if u, ok := m.externalUpstreams[key]; ok {
		return externalNamespace(u), true
	}
	return nil, false
}

// AddNamespace inserts a pre-built Namespace directly into the manager's registry.
// Intended for testing and integration scenarios where system-level namespace
// creation is not possible or desired.
func (m *Manager) AddNamespace(key string, ns *Namespace) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.namespaces[key] = ns
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
