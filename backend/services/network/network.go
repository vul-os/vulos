// Package network manages remote access configuration.
// Provides the base domain for subdomain routing and the public access URL.
package network

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/internal/config"
)

// Status is the network's current state.
type Status struct {
	URL        string `json:"url"`
	Domain     string `json:"domain"`
	InstanceID string `json:"instance_id"`
	Hostname   string `json:"hostname"`
	Mode       string `json:"mode"`
}

// Config holds remote access configuration.
type Config struct {
	AppURL string `json:"app_url"` // public URL to access this device
}

// Service manages remote access config.
type Service struct {
	mu  sync.Mutex
	cfg Config

	instanceID string
	hostname   string
	nodeMode   config.NodeMode
	domainMode config.DomainMode
	domain     string // explicit override (VULOS_DOMAIN or own-domain mode)

	// NET-09 additive: runtime connection-mode switching.
	// `mode` is the user-selected ConnectionMode persisted to ~/.vulos/db/network-mode.json.
	// `stopExternal` is set true when mode == ModeLocal — callers use ExternalListenerBlocked()
	// to decide whether to bind public-facing listeners.
	// `modePath` is the resolved persistence path (set by LoadMode).
	mode         ConnectionMode
	stopExternal bool
	modePath     string
}

// New creates a Service initialised from the supplied config.Config.
func New(c ...*config.Config) *Service {
	var instanceID, hostname, domain string
	var nodeMode config.NodeMode
	var domainMode config.DomainMode

	if len(c) > 0 && c[0] != nil {
		cfg := c[0]
		instanceID = cfg.InstanceID
		hostname = cfg.Hostname
		nodeMode = cfg.NodeMode
		domainMode = cfg.DomainMode
		domain = cfg.Domain
	} else {
		// Legacy path: no config supplied, read from environment.
		instanceID = os.Getenv("VULOS_INSTANCE_ID")
		hostname = os.Getenv("VULOS_HOSTNAME")
		nodeMode = config.NodeMode(getenv("VULOS_MODE", string(config.NodeModeLocal)))
		domainMode = config.DomainMode(getenv("VULOS_DOMAIN_MODE", string(config.DomainModeFabric)))
		domain = os.Getenv("VULOS_DOMAIN")
		if hostname == "" {
			h, _ := os.Hostname()
			if h == "" {
				h = "vulos"
			}
			hostname = h
		}
	}

	svc := &Service{
		instanceID: instanceID,
		hostname:   hostname,
		nodeMode:   nodeMode,
		domainMode: domainMode,
		domain:     domain,
	}
	svc.cfg = Config{AppURL: svc.defaultAppURL()}
	return svc
}


// Domain returns the public domain for this node.
//
// In "fabric" or "direct" modes the domain is derived as {ulid}.vulos.org.
// In "own" mode the explicit VULOS_DOMAIN value is returned.
// In "local" mode "localhost" is returned.
//
// A VULOS_DOMAIN env-var always takes precedence (backward-compat).
func (s *Service) Domain() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Env-override for backward compatibility.
	if d := os.Getenv("VULOS_DOMAIN"); d != "" {
		return d
	}

	switch s.domainMode {
	case config.DomainModeFabric, config.DomainModeDirect:
		if s.instanceID != "" {
			return fmt.Sprintf("%s.vulos.org", s.instanceID)
		}
		return "localhost"
	case config.DomainModeOwn:
		if s.domain != "" {
			return s.domain
		}
		return "localhost"
	default: // local
		return "localhost"
	}
}

// defaultAppURL derives the URL from the resolved domain + PORT.
func (s *Service) defaultAppURL() string {
	domain := s.Domain()
	port := getenv("PORT", "8080")
	scheme := "http"
	if port == "443" {
		scheme = "https"
	}
	if port == "80" || port == "443" {
		return fmt.Sprintf("%s://%s", scheme, domain)
	}
	return fmt.Sprintf("%s://%s:%s", scheme, domain, port)
}

// Configure updates the config at runtime (from OS Settings).
func (s *Service) Configure(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// Config returns the current config.
func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Status returns current state including identity fields.
func (s *Service) Status() Status {
	domain := s.Domain()
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		URL:        s.cfg.AppURL,
		Domain:     domain,
		InstanceID: s.instanceID,
		Hostname:   s.hostname,
		Mode:       string(s.nodeMode),
	}
}

// Stop is a no-op (kept for interface compatibility).
func (s *Service) Stop() {}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// NET-09: connection-mode switching
//
// Adds a user-selectable ConnectionMode (fabric/direct/own/local) that is
// persisted to disk and surfaced via /api/network/mode. Strictly additive —
// the Service struct fields above (instanceID/hostname/nodeMode/domainMode/
// domain) are untouched, the ULID is never regenerated, and LoadMode is the
// only entry-point that mutates the new fields.
// ---------------------------------------------------------------------------

// ConnectionMode is the high-level networking posture selected by the operator.
type ConnectionMode string

const (
	// ModeFabric routes traffic through the Vulos relay fabric (default).
	ModeFabric ConnectionMode = "fabric"
	// ModeDirect uses direct WAN exposure with periodic re-enrollment.
	ModeDirect ConnectionMode = "direct"
	// ModeOwn uses the operator's own domain / reverse proxy.
	ModeOwn ConnectionMode = "own"
	// ModeLocal disables external listeners entirely (LAN-only).
	ModeLocal ConnectionMode = "local"
)

// net9ValidModes is the canonical ordered list returned by the API.
var net9ValidModes = []ConnectionMode{ModeFabric, ModeDirect, ModeOwn, ModeLocal}

// ValidConnectionModes returns the supported ConnectionMode values in canonical order.
func ValidConnectionModes() []ConnectionMode {
	out := make([]ConnectionMode, len(net9ValidModes))
	copy(out, net9ValidModes)
	return out
}

// IsValidConnectionMode reports whether m is one of the supported modes.
func IsValidConnectionMode(m ConnectionMode) bool {
	for _, v := range net9ValidModes {
		if v == m {
			return true
		}
	}
	return false
}

// net9ModeFile is the on-disk persistence format.
type net9ModeFile struct {
	Mode ConnectionMode `json:"mode"`
}

// LoadMode resolves ~/.vulos/db/network-mode.json under `home` and loads any
// persisted mode. If the file is missing or invalid, the mode defaults to
// ModeFabric and is persisted. Safe to call once at server start-up; subsequent
// SetMode calls re-use the path resolved here.
func (s *Service) LoadMode(home string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dbDir := filepath.Join(home, "db")
	s.modePath = filepath.Join(dbDir, "network-mode.json")

	// Default before reading — guarantees a valid mode even on I/O failure.
	s.mode = ModeFabric
	s.stopExternal = false

	raw, err := os.ReadFile(s.modePath)
	if err == nil {
		var f net9ModeFile
		if jerr := json.Unmarshal(raw, &f); jerr == nil && IsValidConnectionMode(f.Mode) {
			s.mode = f.Mode
		}
	}
	s.stopExternal = s.mode == ModeLocal

	// Ensure directory exists, then persist the (possibly default) mode so the
	// file is present on disk at 0600 from the very first call.
	_ = os.MkdirAll(dbDir, 0700)
	_ = s.persistModeLocked()
}

// GetMode returns the current ConnectionMode. Defaults to ModeFabric if
// LoadMode was never called.
func (s *Service) GetMode() ConnectionMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == "" {
		return ModeFabric
	}
	return s.mode
}

// SetMode validates and persists a new ConnectionMode. Returns an error if
// the supplied mode is unsupported. Switching to ModeLocal sets
// ExternalListenerBlocked()==true; switching to ModeDirect best-effort writes
// a re-enroll signal file (see TriggerDirectReenroll). The ULID and other
// identity fields are untouched.
func (s *Service) SetMode(mode ConnectionMode) error {
	if !IsValidConnectionMode(mode) {
		return fmt.Errorf("invalid connection mode %q (valid: fabric, direct, own, local)", mode)
	}

	s.mu.Lock()
	prev := s.mode
	s.mode = mode
	s.stopExternal = mode == ModeLocal
	err := s.persistModeLocked()
	s.mu.Unlock()

	if err != nil {
		return err
	}

	// Best-effort re-enroll signal when entering direct mode.
	if mode == ModeDirect && prev != ModeDirect {
		s.triggerDirectReenrollBestEffort()
	}
	return nil
}

// ExternalListenerBlocked reports whether external listeners must remain bound
// only to loopback. True iff mode == ModeLocal.
func (s *Service) ExternalListenerBlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopExternal
}

// TriggerDirectReenroll writes a best-effort signal file next to the mode file
// so external supervisors / next start-up can pick up the request. Errors are
// swallowed — this is advisory only.
func (s *Service) TriggerDirectReenroll() {
	s.triggerDirectReenrollBestEffort()
}

// triggerDirectReenrollBestEffort writes ~/.vulos/db/network-reenroll.signal
// containing the RFC3339 timestamp. Best-effort: silently ignores all errors.
func (s *Service) triggerDirectReenrollBestEffort() {
	s.mu.Lock()
	path := s.modePath
	s.mu.Unlock()

	if path == "" {
		return // LoadMode never called — nothing to anchor to.
	}
	signalPath := filepath.Join(filepath.Dir(path), "network-reenroll.signal")
	tmp := signalPath + ".tmp"
	payload := []byte(time.Now().UTC().Format(time.RFC3339) + "\n")
	if err := os.WriteFile(tmp, payload, 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, signalPath)
}

// persistModeLocked writes the current mode to disk atomically (tmp+rename) at
// permission 0600. Caller must hold s.mu.
func (s *Service) persistModeLocked() error {
	if s.modePath == "" {
		return nil // LoadMode not called yet — nothing to persist to.
	}
	data, err := json.Marshal(net9ModeFile{Mode: s.mode})
	if err != nil {
		return err
	}
	tmp := s.modePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.modePath); err != nil {
		return err
	}
	// Belt-and-braces: re-chmod in case umask/Rename changed bits.
	_ = os.Chmod(s.modePath, 0600)
	return nil
}
