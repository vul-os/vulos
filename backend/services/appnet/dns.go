package appnet

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// DNSManager writes /etc/hosts entries for app subdomains.
// Maps calculator.vulos → namespace IP so apps can discover each other.
// Also lets the local browser resolve app subdomains.
type DNSManager struct {
	mu     sync.Mutex
	domain string // e.g., "vulos"
	netMgr *Manager
}

func NewDNSManager(domain string, netMgr *Manager) *DNSManager {
	return &DNSManager{domain: domain, netMgr: netMgr}
}

// Update rewrites /etc/hosts with current app→IP mappings.
func (d *DNSManager) Update() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	nsList := d.netMgr.List()

	// Read existing /etc/hosts and strip our managed section
	data, _ := os.ReadFile("/etc/hosts")
	lines := strings.Split(string(data), "\n")
	var clean []string
	inBlock := false
	for _, line := range lines {
		if line == "# --- vulos apps start ---" {
			inBlock = true
			continue
		}
		if line == "# --- vulos apps end ---" {
			inBlock = false
			continue
		}
		if !inBlock {
			clean = append(clean, line)
		}
	}

	// Remove trailing empty lines
	for len(clean) > 0 && strings.TrimSpace(clean[len(clean)-1]) == "" {
		clean = clean[:len(clean)-1]
	}

	// Add our section.
	// NET-04: emit {app}--default.{domain} so the hostname matches the
	// NET-01 ParseSubdomain format ({app}--{profile}.{ulid}.{domain}).
	// Profile is always "default" here; there is no ULID label at this layer
	// because Namespace does not carry one yet — the bare two-label form
	// "{app}--default.{domain}" is still parseable by ParseSubdomain when the
	// gateway passes the first label to it directly.
	clean = append(clean, "", "# --- vulos apps start ---")
	for _, ns := range nsList {
		hostname := fmt.Sprintf("%s--default.%s", ns.AppID, d.domain)
		clean = append(clean, fmt.Sprintf("%s\t%s", ns.NSIP, hostname))
	}
	clean = append(clean, "# --- vulos apps end ---", "")

	if err := os.WriteFile("/etc/hosts", []byte(strings.Join(clean, "\n")), 0644); err != nil {
		return fmt.Errorf("write /etc/hosts: %w", err)
	}

	log.Printf("[dns] updated /etc/hosts with %d app entries", len(nsList))
	return nil
}

// Remove clears all vulos entries from /etc/hosts.
func (d *DNSManager) Remove() {
	d.mu.Lock()
	defer d.mu.Unlock()

	data, _ := os.ReadFile("/etc/hosts")
	lines := strings.Split(string(data), "\n")
	var clean []string
	inBlock := false
	for _, line := range lines {
		if line == "# --- vulos apps start ---" {
			inBlock = true
			continue
		}
		if line == "# --- vulos apps end ---" {
			inBlock = false
			continue
		}
		if !inBlock {
			clean = append(clean, line)
		}
	}
	os.WriteFile("/etc/hosts", []byte(strings.Join(clean, "\n")), 0644)
}

// Resolve returns the namespace IP for an app subdomain.
// NET-04: uses ParseSubdomain to extract the app identifier from the new
// {app}--default.{domain} format as well as the legacy {app}.{domain} form.
func (d *DNSManager) Resolve(subdomain string) (string, bool) {
	appID, _, ok := ParseSubdomain(subdomain, d.domain)
	if !ok {
		// Fallback: strip domain suffix directly (handles bare {app}.{domain}).
		appID = strings.TrimSuffix(subdomain, "."+d.domain)
	}
	ns, nsOK := d.netMgr.Get(appID)
	if !nsOK {
		return "", false
	}
	return ns.NSIP, true
}
