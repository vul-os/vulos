// Package network manages remote access configuration.
// Provides the base domain for subdomain routing and the public access URL.
package network

import (
	"fmt"
	"os"
	"sync"
)

// Status is the network's current state.
type Status struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
}

// Config holds remote access configuration.
type Config struct {
	AppURL string `json:"app_url"` // public URL to access this device
}

// Service manages remote access config.
type Service struct {
	mu  sync.Mutex
	cfg Config
}

func New() *Service {
	return &Service{cfg: Config{
		AppURL: defaultAppURL(),
	}}
}

// Domain returns the current base domain for subdomain routing.
func (s *Service) Domain() string {
	if d := os.Getenv("VULOS_DOMAIN"); d != "" {
		return d
	}
	return "localhost"
}

// defaultAppURL derives the URL from VULOS_DOMAIN + PORT.
func defaultAppURL() string {
	domain := os.Getenv("VULOS_DOMAIN")
	if domain == "" {
		domain = "localhost"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
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

// Status returns current state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		URL:    s.cfg.AppURL,
		Domain: s.Domain(),
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
