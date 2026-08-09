package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"vulos/backend/internal/datadir"

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

	// Region is the home cell of this box (VULOS_REGION; default "eu").
	// Phase-0: only "eu" exists. A second cell is config-only: add its entry
	// to the CP's region map and set VULOS_REGION on boxes in that cell.
	Region string // VULOS_REGION
}

func Load(env string) *Config {
	// "prod" is the canonical production name; "main" is the legacy alias kept
	// for backwards compatibility with the init binary and any scripts that
	// still pass -env=main.
	if env == "prod" {
		env = "main"
	}

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

	// PRECEDENCE: the real process environment wins over the .env file.
	//
	// This used to be the other way round, which is backwards from every
	// dotenv convention and had a sharp edge specific to this program:
	// repoRoot above is derived from runtime.Caller(0), i.e. the path of THIS
	// SOURCE FILE ON THE MACHINE THAT COMPILED THE BINARY. So the tracked
	// .env sitting in a developer's checkout was consulted by that binary
	// wherever it later ran, and silently outranked anything the operator
	// actually exported. `PORT=9000 vulos-server` kept binding 8080 because a
	// file the operator had never seen said so, with no diagnostic.
	//
	// A file of defaults should lose to an explicit instruction. It still
	// beats the compiled-in fallback, so a checkout's .env keeps working for
	// the developer who wrote it.
	get := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v, ok := vars[key]; ok {
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
		// datadir.Root honours VULOS_DATA_DIR and already falls back to
		// /root/.vulos when HOME is unset.
		id, err := ulid.LoadOrCreate(datadir.Root())
		if err != nil {
			// Non-fatal: use in-memory ULID for this run.
			id = ulid.NewULID()
		}
		instanceID = id
	}

	// Default DomainMode varies by env: local/dev default to "local" (no public
	// domain required) so a developer can run without cloud accounts.
	defaultDomainMode := DomainModeFabric
	if env == "local" || env == "dev" {
		defaultDomainMode = DomainModeLocal
	}
	domainMode := DomainMode(get("VULOS_DOMAIN_MODE", string(defaultDomainMode)))
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

	region := get("VULOS_REGION", "eu")

	return &Config{
		Port:       get("PORT", "8080"),
		InstanceID: instanceID,
		Hostname:   hostname,
		NodeID:     nodeID,
		DomainMode: domainMode,
		NodeMode:   nodeMode,
		Domain:     domain,
		Region:     region,
	}
}
