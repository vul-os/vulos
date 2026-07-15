package appnet

// external_upstream.go — ADOPT-A-PORT: let an owner register an already-running
// LOCAL (loopback) service so the OS reverse-proxies to it THROUGH the same
// :8080 auth gateway pipeline as any namespaced app — same session auth, same
// entitlement gating, same X-Vulos-* strip+inject, same rate limiting, same
// relay reachability. No new trust path: the gateway resolves an external
// upstream from GetForProfile exactly where it would resolve a real namespace,
// so every guard that protects a namespaced app protects an adopted port too.
//
// An external upstream is a synthetic Namespace pointing at 127.0.0.1:{port}.
// It is owner-scoped (keyed by userID) so only the user who adopted the port can
// reach it, and it is validated loopback-only against OS-reserved ports.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExternalUpstream records one adopted loopback service.
type ExternalUpstream struct {
	AppID     string    `json:"app_id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	Profile   string    `json:"profile"`
	Port      int       `json:"port"`
	Product   string    `json:"product,omitempty"` // CP entitlement required, if any
	CreatedAt time.Time `json:"created_at"`
}

// reservedAdoptPorts are host ports an adopted upstream may never claim: they
// belong to the OS itself or to other subsystems. Adopting them would either
// let a user hijack a system service's traffic through the gateway or collide
// with a launched app's namespace host port.
func reservedAdoptPorts() map[int]struct{} {
	r := map[int]struct{}{
		53:   {}, // DNS
		5353: {}, // mDNS
	}
	// The gateway's own listen port (default 8080): adopting it would loop the
	// gateway back onto itself.
	if _, portStr, ok := splitHostPortRaw(GatewayLoopbackAddr()); ok {
		if p, err := strconv.Atoi(portStr); err == nil {
			r[p] = struct{}{}
		}
	}
	return r
}

// ValidateAdoptablePort enforces the loopback-adopt rules: the target is always
// 127.0.0.1:{port} (the caller supplies only a port — never a host — so it is
// loopback by construction), the port must be a real user port, and it must not
// be an OS-reserved port or fall inside the namespace host-port range
// (7070-7999) the launcher hands out.
func ValidateAdoptablePort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("port %d out of range (1-65535)", port)
	}
	if port < 1024 {
		return fmt.Errorf("port %d is a privileged/well-known port and cannot be adopted", port)
	}
	if port >= namespaceHostPortMin && port <= namespaceHostPortMax {
		return fmt.Errorf("port %d is inside the reserved app namespace range %d-%d", port, namespaceHostPortMin, namespaceHostPortMax)
	}
	if _, bad := reservedAdoptPorts()[port]; bad {
		return fmt.Errorf("port %d is reserved by the OS and cannot be adopted", port)
	}
	return nil
}

// namespaceHostPortMin/Max mirror the PortPool range used by the launcher
// (main.go: appnet.NewPortPool(7070, 7999)). Adopted ports must stay clear of it.
const (
	namespaceHostPortMin = 7070
	namespaceHostPortMax = 7999
)

// splitHostPortRaw splits "host:port" without failing on a bare host. ok is
// false when there is no ":port" suffix.
func splitHostPortRaw(addr string) (host, port string, ok bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", false
	}
	return addr[:i], addr[i+1:], true
}

// ---------------------------------------------------------------------------
// Manager registry (live, in-memory) — resolved by GetForProfile
// ---------------------------------------------------------------------------

// RegisterExternalUpstream adds (or replaces) an adopted loopback upstream for
// (ownerID, appID, profile). The port is validated loopback-only against
// OS-reserved ports. It becomes resolvable by GetForProfile for that owner.
func (m *Manager) RegisterExternalUpstream(u ExternalUpstream) error {
	if u.AppID == "" || u.OwnerID == "" {
		return fmt.Errorf("external upstream requires app_id and owner_id")
	}
	if err := ValidateAdoptablePort(u.Port); err != nil {
		return err
	}
	if u.Profile == "" {
		u.Profile = "default"
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	key := u.OwnerID + "-" + net02ProfileKey(u.Profile, u.AppID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.externalUpstreams == nil {
		m.externalUpstreams = make(map[string]*ExternalUpstream)
	}
	cp := u
	m.externalUpstreams[key] = &cp
	return nil
}

// RemoveExternalUpstream drops the adopted upstream for (ownerID, appID, profile).
func (m *Manager) RemoveExternalUpstream(appID, ownerID, profile string) {
	if profile == "" {
		profile = "default"
	}
	key := ownerID + "-" + net02ProfileKey(profile, appID)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.externalUpstreams, key)
}

// GetExternalUpstream returns the adopted upstream for (ownerID, appID, profile).
func (m *Manager) GetExternalUpstream(appID, ownerID, profile string) (*ExternalUpstream, bool) {
	if profile == "" {
		profile = "default"
	}
	key := ownerID + "-" + net02ProfileKey(profile, appID)
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.externalUpstreams[key]
	if !ok {
		return nil, false
	}
	cp := *u
	return &cp, true
}

// ListExternalUpstreamsForOwner returns every adopted upstream owned by ownerID.
func (m *Manager) ListExternalUpstreamsForOwner(ownerID string) []ExternalUpstream {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ExternalUpstream
	for _, u := range m.externalUpstreams {
		if u.OwnerID == ownerID {
			out = append(out, *u)
		}
	}
	return out
}

// externalNamespace builds the synthetic Namespace that makes an adopted
// loopback port look like a normal app namespace to the gateway proxy.
func externalNamespace(u *ExternalUpstream) *Namespace {
	return &Namespace{
		Name:    "vulos_ext_" + u.AppID,
		AppID:   u.AppID,
		OwnerID: u.OwnerID,
		NSIP:    "127.0.0.1",
		AppPort: u.Port,
		Active:  true,
	}
}

// GetAnyForApp returns the first ACTIVE real namespace serving appID, regardless
// of owner or profile. Used only by the anonymous public-web path (PublicHandler),
// where there is no user to scope by. It deliberately does NOT consult the
// external-upstream registry, so an adopted loopback port can never be served on
// the public web even if its visibility is flipped to "public".
func (m *Manager) GetAnyForApp(appID string) (*Namespace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ns, ok := m.namespaces[appID]; ok && ns.Active {
		return ns, true
	}
	for _, ns := range m.namespaces {
		if ns.Active && (ns.AppID == appID || strings.HasSuffix(ns.AppID, "-"+appID)) {
			return ns, true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// ExternalUpstreamStore — JSON persistence so adopted ports survive reboot
// ---------------------------------------------------------------------------

const externalUpstreamDBFilename = "app_external_upstreams.json"

// externalUpstreamDB is the on-disk representation. Key: ownerID + "::" + appID + "::" + profile.
type externalUpstreamDB struct {
	Upstreams map[string]*ExternalUpstream `json:"upstreams"`
}

// ExternalUpstreamStore persists ExternalUpstream records to disk as JSON.
// All methods are safe for concurrent use.
type ExternalUpstreamStore struct {
	mu   sync.RWMutex
	path string
	db   externalUpstreamDB
}

func externalKey(ownerID, appID, profile string) string {
	if profile == "" {
		profile = "default"
	}
	return ownerID + "::" + appID + "::" + profile
}

// NewExternalUpstreamStore opens the store at VULOS_EXTERNAL_UPSTREAM_DB or the
// default ~/.vulos/db path.
func NewExternalUpstreamStore() (*ExternalUpstreamStore, error) {
	path := os.Getenv("VULOS_EXTERNAL_UPSTREAM_DB")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		path = filepath.Join(home, ".vulos", "db", externalUpstreamDBFilename)
	}
	return NewExternalUpstreamStoreAt(path)
}

// NewExternalUpstreamStoreAt opens the store backed by path.
func NewExternalUpstreamStoreAt(path string) (*ExternalUpstreamStore, error) {
	s := &ExternalUpstreamStore{
		path: path,
		db:   externalUpstreamDB{Upstreams: make(map[string]*ExternalUpstream)},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ExternalUpstreamStore) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("external upstream store: read %s: %w", s.path, err)
	}
	var db externalUpstreamDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("external upstream store: parse %s: %w", s.path, err)
	}
	if db.Upstreams == nil {
		db.Upstreams = make(map[string]*ExternalUpstream)
	}
	s.db = db
	return nil
}

func (s *ExternalUpstreamStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("external upstream store: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(s.db, "", "  ")
	if err != nil {
		return fmt.Errorf("external upstream store: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("external upstream store: write tmp: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// Set stores (or replaces) an upstream and persists.
func (s *ExternalUpstreamStore) Set(u ExternalUpstream) error {
	if u.Profile == "" {
		u.Profile = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := u
	s.db.Upstreams[externalKey(u.OwnerID, u.AppID, u.Profile)] = &cp
	return s.save()
}

// Get returns the stored upstream for (ownerID, appID, profile), or nil.
func (s *ExternalUpstreamStore) Get(ownerID, appID, profile string) *ExternalUpstream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.db.Upstreams[externalKey(ownerID, appID, profile)]
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}

// Delete removes the upstream record for (ownerID, appID, profile) and persists.
func (s *ExternalUpstreamStore) Delete(ownerID, appID, profile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.db.Upstreams, externalKey(ownerID, appID, profile))
	return s.save()
}

// All returns a copy of all stored upstreams (used at boot to re-hydrate the
// Manager's live registry).
func (s *ExternalUpstreamStore) All() []ExternalUpstream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExternalUpstream, 0, len(s.db.Upstreams))
	for _, u := range s.db.Upstreams {
		out = append(out, *u)
	}
	return out
}
