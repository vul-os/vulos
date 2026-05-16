package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"vulos/backend/internal/ulid"
)

var (
	_, srcFile, _, _ = runtime.Caller(0)
	repoRoot         = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(srcFile))))
)

// DomainMode controls how the public domain is derived.
//   - "fabric"  – {ulid}.vulos.org via Ziti overlay (default)
//   - "direct"  – {ulid}.vulos.org via direct public IP + ACME-DNS
//   - "own"     – operator-supplied domain (VULOS_DOMAIN)
//   - "local"   – no public domain, LAN/localhost only
type DomainMode string

const (
	DomainModeFabric DomainMode = "fabric"
	DomainModeDirect DomainMode = "direct"
	DomainModeOwn    DomainMode = "own"
	DomainModeLocal  DomainMode = "local"
)

// NodeMode describes the physical presence of the node.
//   - "server" – headless, serves remote users via tunnel
//   - "local"  – physical display, direct use
type NodeMode string

const (
	NodeModeServer NodeMode = "server"
	NodeModeLocal  NodeMode = "local"
)

type Config struct {
	Port string

	// Identity
	InstanceID string     // VULOS_INSTANCE_ID (ULID, generated+persisted at first boot)
	Hostname   string     // VULOS_HOSTNAME (human-readable, defaults to OS hostname)
	NodeID     string     // VULOS_NODE_ID (unique per node, defaults to Hostname)
	DomainMode DomainMode // VULOS_DOMAIN_MODE ("fabric"|"direct"|"own"|"local")
	NodeMode   NodeMode   // VULOS_MODE ("server"|"local")
	Domain     string     // VULOS_DOMAIN (explicit override; derived automatically in fabric/direct)
}

func Load(env string) *Config {
	envFiles := map[string]string{
		"main":  filepath.Join(repoRoot, ".env.main"),
		"dev":   filepath.Join(repoRoot, ".env.dev"),
		"local": filepath.Join(repoRoot, ".env"),
	}

	vars := map[string]string{}

	envFile := envFiles[env]
	if env == "local" {
		localFile := filepath.Join(repoRoot, ".env.local")
		if _, err := os.Stat(localFile); err == nil {
			envFile = localFile
		}
	}

	if data, err := os.ReadFile(envFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			key, val, _ := strings.Cut(line, "=")
			vars[strings.TrimSpace(key)] = strings.TrimSpace(val)
		}
	}

	get := func(key, fallback string) string {
		if v, ok := vars[key]; ok {
			return v
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}

	// Resolve OS hostname for defaults.
	osHostname, _ := os.Hostname()
	if osHostname == "" {
		osHostname = "vulos"
	}

	hostname := get("VULOS_HOSTNAME", osHostname)
	nodeID := get("VULOS_NODE_ID", hostname)

	// Instance ULID — load from env or from ~/.vulos/instance-id (generated once).
	instanceID := get("VULOS_INSTANCE_ID", "")
	if instanceID == "" {
		vulosDir := filepath.Join(os.Getenv("HOME"), ".vulos")
		if vulosDir == "/.vulos" {
			// Fallback when HOME is empty (e.g. container without HOME set).
			vulosDir = "/root/.vulos"
		}
		id, err := ulid.LoadOrCreate(vulosDir)
		if err != nil {
			// Non-fatal: use in-memory ULID for this run.
			id = ulid.NewULID()
		}
		instanceID = id
	}

	domainMode := DomainMode(get("VULOS_DOMAIN_MODE", string(DomainModeFabric)))
	switch domainMode {
	case DomainModeFabric, DomainModeDirect, DomainModeOwn, DomainModeLocal:
		// valid
	default:
		domainMode = DomainModeFabric
	}

	nodeMode := NodeMode(get("VULOS_MODE", string(NodeModeLocal)))
	switch nodeMode {
	case NodeModeServer, NodeModeLocal:
		// valid
	default:
		nodeMode = NodeModeLocal
	}

	domain := get("VULOS_DOMAIN", "")

	return &Config{
		Port:       get("PORT", "8080"),
		InstanceID: instanceID,
		Hostname:   hostname,
		NodeID:     nodeID,
		DomainMode: domainMode,
		NodeMode:   nodeMode,
		Domain:     domain,
	}
}
