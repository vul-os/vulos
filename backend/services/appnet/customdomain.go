package appnet

// PUBWEB-07: Custom domain support for published apps.
//
// Flow:
//  1. POST /api/apps/{id}/domain  {"domain":"mysite.example.com"}
//     - Generates a challenge token and records a CustomDomain entry with
//       status "pending".
//     - Returns {domain, challenge_token, txt_record, status:"pending"}.
//     - The user must add a DNS TXT record:
//         _vulos-verify.mysite.example.com  →  <challenge_token>
//  2. POST /api/apps/{id}/domain/verify
//     - Performs a live DNS TXT lookup for _vulos-verify.<domain>.
//     - On success: writes a Caddy virtual-host snippet for the custom domain
//       (ACME TLS), persists status "verified", returns the updated entry.
//     - On failure: returns 409 with {status:"pending", error:"..."}.
//  3. DELETE /api/apps/{id}/domain
//     - Removes the custom domain record and its Caddy snippet.
//     - The app reverts to its default vulos.org subdomain.
//  4. GET /api/apps/{id}/domain
//     - Returns the current CustomDomain record (pending or verified).
//
// Confinement: all logic lives in appnet/; no edits to main.go.
// RegisterCustomDomainHandlers must be called alongside the other
// Register*Handlers in the server's aggregate setup.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"vulos/backend/internal/datadir"
)

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

const (
	// CustomDomainStatusPending means the TXT record has not been verified yet.
	CustomDomainStatusPending = "pending"
	// CustomDomainStatusVerified means the TXT challenge was resolved successfully
	// and the custom domain is live.
	CustomDomainStatusVerified = "verified"

	customDomainDBFilename = "app_custom_domains.json"
)

// CustomDomain holds a user-supplied domain attached to a published app.
type CustomDomain struct {
	AppID          string    `json:"app_id"`
	Domain         string    `json:"domain"`          // e.g. "mysite.example.com"
	ChallengeToken string    `json:"challenge_token"` // random hex token
	Status         string    `json:"status"`          // "pending" | "verified"
	CreatedAt      time.Time `json:"created_at"`
	VerifiedAt     time.Time `json:"verified_at,omitempty"`
}

// TXTRecordName returns the DNS TXT record name the user must create.
func (cd *CustomDomain) TXTRecordName() string {
	return "_vulos-verify." + cd.Domain
}

// customDomainDB is the on-disk representation.
type customDomainDB struct {
	// Key: app_id
	Domains map[string]*CustomDomain `json:"domains"`
}

// ---------------------------------------------------------------------------
// CustomDomainStore
// ---------------------------------------------------------------------------

// CustomDomainStore persists CustomDomain records to disk as JSON.
// All methods are safe for concurrent use.
type CustomDomainStore struct {
	mu   sync.RWMutex
	path string
	db   customDomainDB
}

// NewCustomDomainStore creates a CustomDomainStore backed by the default path
// (~/.vulos/db/app_custom_domains.json or VULOS_CUSTOMDOMAIN_DB env var).
func NewCustomDomainStore() (*CustomDomainStore, error) {
	path := os.Getenv("VULOS_CUSTOMDOMAIN_DB")
	if path == "" {
		path = datadir.Join("db", customDomainDBFilename)
	}
	return NewCustomDomainStoreAt(path)
}

// NewCustomDomainStoreAt creates a CustomDomainStore backed by path.
func NewCustomDomainStoreAt(path string) (*CustomDomainStore, error) {
	cs := &CustomDomainStore{
		path: path,
		db:   customDomainDB{Domains: make(map[string]*CustomDomain)},
	}
	if err := cs.load(); err != nil {
		return nil, err
	}
	return cs, nil
}

func (cs *CustomDomainStore) load() error {
	data, err := os.ReadFile(cs.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("custom domain store: read %s: %w", cs.path, err)
	}
	var db customDomainDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("custom domain store: parse %s: %w", cs.path, err)
	}
	if db.Domains == nil {
		db.Domains = make(map[string]*CustomDomain)
	}
	cs.db = db
	return nil
}

func (cs *CustomDomainStore) save() error {
	if err := os.MkdirAll(filepath.Dir(cs.path), 0700); err != nil {
		return fmt.Errorf("custom domain store: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(cs.db, "", "  ")
	if err != nil {
		return fmt.Errorf("custom domain store: marshal: %w", err)
	}
	tmp := cs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("custom domain store: write tmp: %w", err)
	}
	if err := os.Rename(tmp, cs.path); err != nil {
		return fmt.Errorf("custom domain store: rename: %w", err)
	}
	return nil
}

// Get returns the stored CustomDomain for appID, or nil if none.
func (cs *CustomDomainStore) Get(appID string) *CustomDomain {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	cd := cs.db.Domains[appID]
	if cd == nil {
		return nil
	}
	cp := *cd
	return &cp
}

// Set stores (or replaces) a CustomDomain and persists to disk.
func (cs *CustomDomainStore) Set(cd *CustomDomain) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.db.Domains[cd.AppID] = cd
	return cs.save()
}

// Delete removes the custom domain record for appID.
func (cs *CustomDomainStore) Delete(appID string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.db.Domains, appID)
	return cs.save()
}

// ---------------------------------------------------------------------------
// Challenge token generation
// ---------------------------------------------------------------------------

// generateChallengeToken returns a cryptographically random 32-byte hex string.
func generateChallengeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate challenge token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// DNS TXT verification
// ---------------------------------------------------------------------------

// dnsLookupTXT is the function used for TXT lookups.  Swappable in tests.
var dnsLookupTXT = func(name string) ([]string, error) {
	return net.LookupTXT(name)
}

// VerifyDNSTXT checks whether the TXT record _vulos-verify.<domain> contains
// the expected challenge token.  Returns nil on success.
func VerifyDNSTXT(domain, challengeToken string) error {
	txtName := "_vulos-verify." + domain
	records, err := dnsLookupTXT(txtName)
	if err != nil {
		return fmt.Errorf("dns lookup %s: %w", txtName, err)
	}
	for _, r := range records {
		if strings.TrimSpace(r) == challengeToken {
			return nil
		}
	}
	return fmt.Errorf("TXT record %s does not contain expected token (found %d record(s))", txtName, len(records))
}

// ---------------------------------------------------------------------------
// Caddy snippet for custom domain
// ---------------------------------------------------------------------------

// writeCustomDomainCaddySnippet writes a Caddyfile virtual-host for the custom
// domain, routing traffic to upstreamAddr with automatic ACME TLS.
// The snippet is placed in caddyDir as "<appID>--custom.caddy".
// No-ops if caddyDir is empty or "noop".
func writeCustomDomainCaddySnippet(caddyDir, appID, domain, upstreamAddr string) error {
	if caddyDir == "" || caddyDir == "noop" {
		return nil
	}
	if err := os.MkdirAll(caddyDir, 0750); err != nil {
		return fmt.Errorf("caddy custom domain: mkdir %s: %w", caddyDir, err)
	}

	// SECURITY (Finding 1, HIGH): custom domains route through the auth gateway's
	// anonymous public entrypoint (rewrite to PubwebPathPrefix+{appID}), never
	// straight at the app namespace — same reasoning as the subdomain snippet.
	// upstreamAddr is the gateway loopback address (see upstreamAddrForApp).
	snippet := fmt.Sprintf(`# Vulos auto-generated — do not edit manually.
# App: %s  Custom domain: %s
%s {
	tls {
		# ACME (Let's Encrypt) — Caddy requests certs automatically.
	}
	# Route through the Vulos auth gateway's anonymous public entrypoint.
	rewrite * %s{uri}
	reverse_proxy %s
}
`, appID, domain, domain, PubwebUpstreamPath(appID), upstreamAddr)

	snippetPath := filepath.Join(caddyDir, appID+"--custom.caddy")
	tmp := snippetPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(snippet), 0640); err != nil {
		return fmt.Errorf("caddy custom domain: write tmp: %w", err)
	}
	return os.Rename(tmp, snippetPath)
}

// removeCustomDomainCaddySnippet deletes the Caddyfile snippet for the custom domain.
func removeCustomDomainCaddySnippet(caddyDir, appID string) error {
	if caddyDir == "" || caddyDir == "noop" {
		return nil
	}
	snippetPath := filepath.Join(caddyDir, appID+"--custom.caddy")
	if err := os.Remove(snippetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("caddy custom domain: remove %s: %w", snippetPath, err)
	}
	return nil
}
