// Package network manages remote access configuration.
// Provides the base domain for subdomain routing and the public access URL.
package network

import (
	"fmt"
	"os"
	"sync"

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

// InstanceID returns the stable ULID for this node.
func (s *Service) InstanceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceID
}

// Hostname returns the human-readable hostname.
func (s *Service) Hostname() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostname
}

// Mode returns the node operating mode ("server" or "local").
func (s *Service) Mode() config.NodeMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodeMode
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
