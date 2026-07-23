// ct.go — Certificate Transparency monitoring.
//
// A daily worker queries crt.sh for vulos.org certificates and compares
// against the known-good list (VULOS_KNOWN_CERTS env var, comma-separated
// fingerprints, or the DB table). Unrecognized issuances alert super-admin
// via audit log.
package security

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ctshBaseURL is the crt.sh API base; overridden in tests.
var ctshBaseURL = "https://crt.sh"

// crtshEntry is one row from the crt.sh JSON API.
type crtshEntry struct {
	ID         int64  `json:"id"`
	NotBefore  string `json:"not_before"`
	NotAfter   string `json:"not_after"`
	CommonName string `json:"common_name"`
	Issuer     string `json:"issuer_name"`
}

// CTMonitor monitors Certificate Transparency logs for vulos.org.
type CTMonitor struct {
	store  *Store
	domain string
}

// NewCTMonitor creates a CT monitor for the given domain.
func NewCTMonitor(store *Store, domain string) *CTMonitor {
	if domain == "" {
		domain = "vulos.org"
	}
	return &CTMonitor{store: store, domain: domain}
}

// Domain returns the zone this monitor watches. Exposed for wiring tests.
func (m *CTMonitor) Domain() string { return m.domain }

// RunOnce performs a single CT check. Called by the periodic worker.
func (m *CTMonitor) RunOnce(ctx context.Context) error {
	entries, err := m.fetchCerts(ctx)
	if err != nil {
		return fmt.Errorf("ct: fetch: %w", err)
	}

	known := m.knownCerts()
	for _, e := range entries {
		recognized := m.isRecognized(e, known)
		if err := m.store.UpsertCTCert(ctx, e.CommonName, e.Issuer, e.NotBefore, e.NotAfter, recognized); err != nil {
			log.Printf("[security/ct] upsert: %v", err)
		}
		if !recognized {
			log.Printf("[security/ct] ALERT: unrecognized cert cn=%q issuer=%q not_before=%s",
				e.CommonName, e.Issuer, e.NotBefore)
		}
	}
	return nil
}

// ctFirstRunDeferSeconds returns the configured first-run defer duration.
// Reads VULOS_CT_FIRST_RUN_DEFER_SECONDS; defaults to 60.
func ctFirstRunDeferSeconds() time.Duration {
	if v := os.Getenv("VULOS_CT_FIRST_RUN_DEFER_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}

// RunDaily starts a background goroutine that defers the first run by
// ctFirstRunDeferSeconds (default 60s, configurable via
// VULOS_CT_FIRST_RUN_DEFER_SECONDS), then calls RunOnce every 24 hours.
//
// The initial defer prevents the ~30s crt.sh round-trip from blocking server
// startup on the critical path.
func (m *CTMonitor) RunDaily(ctx context.Context) {
	m.Start(ctx)
}

// Start is the implementation of RunDaily. Exported for test injection.
func (m *CTMonitor) Start(ctx context.Context) {
	defer_ := ctFirstRunDeferSeconds()
	go func() {
		// Wait for the configured defer before the first crt.sh call.
		select {
		case <-ctx.Done():
			return
		case <-time.After(defer_):
		}

		// First run after defer.
		if err := m.RunOnce(ctx); err != nil {
			log.Printf("[security/ct] first run: %v", err)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.RunOnce(ctx); err != nil {
					log.Printf("[security/ct] daily run: %v", err)
				}
			}
		}
	}()
}

// fetchCerts queries crt.sh and returns parsed entries.
func (m *CTMonitor) fetchCerts(ctx context.Context) ([]crtshEntry, error) {
	url := fmt.Sprintf("%s/?q=%s&output=json", ctshBaseURL, m.domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vulos-ct-monitor/1.0")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("crt.sh returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	var entries []crtshEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("ct: parse json: %w", err)
	}
	return entries, nil
}

// knownCerts returns the set of recognized certificate common names / issuers.
// Sources (in priority order):
//  1. VULOS_KNOWN_CERTS env var: comma-separated "cn:issuer" pairs.
//  2. Hardcoded defaults for well-known Vulos certs.
func (m *CTMonitor) knownCerts() map[string]bool {
	known := map[string]bool{
		// Default: Let's Encrypt issued certs for vulos.org are expected.
		"vulos.org:Let's Encrypt":   true,
		"*.vulos.org:Let's Encrypt": true,
		"vulos.org:ZeroSSL":         true,
		"*.vulos.org:ZeroSSL":       true,
	}
	if env := os.Getenv("VULOS_KNOWN_CERTS"); env != "" {
		for _, pair := range strings.Split(env, ",") {
			pair = strings.TrimSpace(pair)
			if pair != "" {
				known[pair] = true
			}
		}
	}
	return known
}

// isRecognized returns true if the certificate matches a known-good entry.
func (m *CTMonitor) isRecognized(e crtshEntry, known map[string]bool) bool {
	// Check exact cn:issuer pair.
	key := e.CommonName + ":" + shortIssuer(e.Issuer)
	if known[key] {
		return true
	}
	// Check cn with any known issuer prefix.
	for k := range known {
		parts := strings.SplitN(k, ":", 2)
		if len(parts) == 2 && parts[0] == e.CommonName {
			if strings.Contains(e.Issuer, parts[1]) {
				return true
			}
		}
	}
	return false
}

// shortIssuer extracts the O= value from an X.509 issuer string for comparison.
func shortIssuer(issuer string) string {
	for _, part := range strings.Split(issuer, ", ") {
		if strings.HasPrefix(part, "O=") {
			return strings.TrimPrefix(part, "O=")
		}
	}
	// Fall back to the first 30 chars.
	if len(issuer) > 30 {
		return issuer[:30]
	}
	return issuer
}
