package appnet

// PUBWEB-02: Subdomain provisioning for published apps.
//
// When an app's visibility is set to "public", a subdomain is provisioned via
// the Vulos cloud DNS API.  The FQDN is stored in an in-memory/file-backed
// DeploymentStore.  A Caddyfile snippet is also emitted so self-hosters can
// point their own domain.
//
// Subdomain scheme (from roadmap/NETWORK.md):
//
//	{app}--{profile}.{ulid}.vulos.org
//
// The ulid is the instance ULID read from VULOS_INSTANCE_ID (defaults to
// "local" for dev/testing).  The profile defaults to "default" when empty.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// defaultDNSAPI is the Vulos cloud DNS provisioning endpoint.
	// Override with VULOS_DNS_API for self-hosted / dev stubs.
	defaultDNSAPI = "https://api.vulos.org/dns/provision"

	// defaultBaseDomain is the public-web TLD used in FQDN construction.
	defaultBaseDomain = "vulos.org"

	// deploymentDBFilename is the filename for the deployment persistence file.
	deploymentDBFilename = "app_deployments.json"
)

// Deployment records the provisioned subdomain for one app+profile.
type Deployment struct {
	AppID       string    `json:"app_id"`
	Profile     string    `json:"profile"` // "default" when not specified
	FQDN        string    `json:"fqdn"`    // e.g. notes--default.01h5t3.vulos.org
	Provisioned time.Time `json:"provisioned"`
	TLSObtained bool      `json:"tls_obtained"` // true once ACME cert is issued
}

// deploymentDB is the on-disk representation.
type deploymentDB struct {
	// Key: appID + "::" + profile
	Deployments map[string]*Deployment `json:"deployments"`
}

// DeploymentStore persists Deployment records to disk as JSON.
// All methods are safe for concurrent use.
type DeploymentStore struct {
	mu   sync.RWMutex
	path string
	db   deploymentDB
}

// NewDeploymentStore creates a DeploymentStore backed by the default path
// (~/.vulos/db/app_deployments.json or VULOS_DEPLOY_DB env var).
func NewDeploymentStore() (*DeploymentStore, error) {
	path := os.Getenv("VULOS_DEPLOY_DB")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		path = filepath.Join(home, ".vulos", "db", deploymentDBFilename)
	}
	return NewDeploymentStoreAt(path)
}

// NewDeploymentStoreAt creates a DeploymentStore backed by path.
func NewDeploymentStoreAt(path string) (*DeploymentStore, error) {
	ds := &DeploymentStore{
		path: path,
		db:   deploymentDB{Deployments: make(map[string]*Deployment)},
	}
	if err := ds.load(); err != nil {
		return nil, err
	}
	return ds, nil
}

func deployKey(appID, profile string) string {
	if profile == "" {
		profile = "default"
	}
	return appID + "::" + profile
}

func (ds *DeploymentStore) load() error {
	data, err := os.ReadFile(ds.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deployment store: read %s: %w", ds.path, err)
	}
	var db deploymentDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("deployment store: parse %s: %w", ds.path, err)
	}
	if db.Deployments == nil {
		db.Deployments = make(map[string]*Deployment)
	}
	ds.db = db
	return nil
}

func (ds *DeploymentStore) save() error {
	if err := os.MkdirAll(filepath.Dir(ds.path), 0700); err != nil {
		return fmt.Errorf("deployment store: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(ds.db, "", "  ")
	if err != nil {
		return fmt.Errorf("deployment store: marshal: %w", err)
	}
	tmp := ds.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("deployment store: write tmp: %w", err)
	}
	if err := os.Rename(tmp, ds.path); err != nil {
		return fmt.Errorf("deployment store: rename: %w", err)
	}
	return nil
}

// Get returns the stored Deployment for (appID, profile), or nil if none.
func (ds *DeploymentStore) Get(appID, profile string) *Deployment {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	d := ds.db.Deployments[deployKey(appID, profile)]
	if d == nil {
		return nil
	}
	cp := *d
	return &cp
}

// Set stores (or replaces) a Deployment and persists to disk.
func (ds *DeploymentStore) Set(d *Deployment) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	key := deployKey(d.AppID, d.Profile)
	ds.db.Deployments[key] = d
	return ds.save()
}

// Delete removes the deployment record for (appID, profile).
func (ds *DeploymentStore) Delete(appID, profile string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.db.Deployments, deployKey(appID, profile))
	return ds.save()
}

// All returns a copy of all deployments.
func (ds *DeploymentStore) All() []*Deployment {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	out := make([]*Deployment, 0, len(ds.db.Deployments))
	for _, d := range ds.db.Deployments {
		cp := *d
		out = append(out, &cp)
	}
	return out
}

// ---------------------------------------------------------------------------
// Provisioner — calls the cloud DNS API + manages Caddy config
// ---------------------------------------------------------------------------

// Provisioner handles subdomain lifecycle for published apps.
type Provisioner struct {
	store      *DeploymentStore
	httpClient *http.Client
	dnsAPI     string // POST endpoint for DNS provisioning
	baseDomain string // e.g. "vulos.org"
	instanceID string // ULID of this instance
	caddyDir   string // dir where Caddyfile snippets are written
}

// NewProvisioner creates a Provisioner with the given stores and an optional
// HTTP client (nil → use a default 15-second client).
func NewProvisioner(ds *DeploymentStore, client *http.Client) *Provisioner {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	dnsAPI := os.Getenv("VULOS_DNS_API")
	if dnsAPI == "" {
		dnsAPI = defaultDNSAPI
	}
	baseDomain := os.Getenv("VULOS_BASE_DOMAIN")
	if baseDomain == "" {
		baseDomain = defaultBaseDomain
	}
	instanceID := os.Getenv("VULOS_INSTANCE_ID")
	if instanceID == "" {
		instanceID = "local"
	}
	caddyDir := os.Getenv("VULOS_CADDY_DIR")
	if caddyDir == "" {
		caddyDir = "/etc/caddy/vulos-apps"
	}
	return &Provisioner{
		store:      ds,
		httpClient: client,
		dnsAPI:     dnsAPI,
		baseDomain: baseDomain,
		instanceID: instanceID,
		caddyDir:   caddyDir,
	}
}

// BuildFQDN constructs the canonical public FQDN for an app+profile.
//
//	{app}--{profile}.{instanceID}.{baseDomain}
func (p *Provisioner) BuildFQDN(appID, profile string) string {
	if profile == "" {
		profile = "default"
	}
	return fmt.Sprintf("%s--%s.%s.%s", appID, profile, p.instanceID, p.baseDomain)
}

// dnsProvisionRequest is the body sent to the cloud DNS API.
type dnsProvisionRequest struct {
	FQDN       string `json:"fqdn"`
	AppID      string `json:"app_id"`
	InstanceID string `json:"instance_id"`
}

// dnsProvisionResponse is the expected JSON response from the DNS API.
type dnsProvisionResponse struct {
	FQDN    string `json:"fqdn"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// callDNSAPI sends the provisioning request to the Vulos cloud DNS endpoint.
// In self-hosted or dev mode (VULOS_DNS_API unset) this is a no-op that returns
// the pre-built FQDN immediately.
func (p *Provisioner) callDNSAPI(ctx context.Context, fqdn, appID string) error {
	// If the API is the sentinel "noop" value skip the network call (dev/self-hosted).
	if p.dnsAPI == "noop" {
		return nil
	}

	payload := dnsProvisionRequest{
		FQDN:       fqdn,
		AppID:      appID,
		InstanceID: p.instanceID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("dns provision: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.dnsAPI, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dns provision: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dns provision: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("dns provision: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}

// writeCaddySnippet writes a Caddyfile virtual-host snippet for the app so that
// self-hosters can include it from their main Caddyfile.  No-ops if caddyDir is
// empty or "noop".
func (p *Provisioner) writeCaddySnippet(appID, profile, fqdn, upstreamAddr string) error {
	if p.caddyDir == "" || p.caddyDir == "noop" {
		return nil
	}

	if err := os.MkdirAll(p.caddyDir, 0750); err != nil {
		return fmt.Errorf("caddy snippet: mkdir %s: %w", p.caddyDir, err)
	}

	snippetPath := filepath.Join(p.caddyDir, fmt.Sprintf("%s--%s.caddy", appID, profile))

	// Use ACME (Let's Encrypt) TLS by default — Caddy handles this automatically
	// when a hostname is specified without an explicit tls directive.
	snippet := fmt.Sprintf(`# Vulos auto-generated — do not edit manually.
# App: %s  Profile: %s
%s {
	tls {
		# ACME (Let's Encrypt) — Caddy requests certs automatically.
	}
	reverse_proxy %s
}
`, appID, profile, fqdn, upstreamAddr)

	tmp := snippetPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(snippet), 0640); err != nil {
		return fmt.Errorf("caddy snippet: write tmp: %w", err)
	}
	return os.Rename(tmp, snippetPath)
}

// removeCaddySnippet deletes the Caddyfile snippet for the app.
func (p *Provisioner) removeCaddySnippet(appID, profile string) error {
	if p.caddyDir == "" || p.caddyDir == "noop" {
		return nil
	}
	snippetPath := filepath.Join(p.caddyDir, fmt.Sprintf("%s--%s.caddy", appID, profile))
	if err := os.Remove(snippetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("caddy snippet: remove %s: %w", snippetPath, err)
	}
	return nil
}

// ProvisionSubdomain provisions a public subdomain for appID+profile.
// Steps:
//  1. Build the FQDN.
//  2. Call the Vulos DNS API.
//  3. Write the Caddy snippet.
//  4. Store the Deployment record.
//
// Idempotent: if a deployment already exists for this (appID, profile) it is
// returned immediately without re-provisioning.
func (p *Provisioner) ProvisionSubdomain(ctx context.Context, appID, profile, upstreamAddr string) (*Deployment, error) {
	if profile == "" {
		profile = "default"
	}

	// Idempotency: return existing deployment.
	if existing := p.store.Get(appID, profile); existing != nil {
		return existing, nil
	}

	fqdn := p.BuildFQDN(appID, profile)

	if err := p.callDNSAPI(ctx, fqdn, appID); err != nil {
		return nil, fmt.Errorf("provision subdomain for %s: %w", appID, err)
	}

	// FAIL CLOSED: the Caddy snippet is what actually routes the subdomain. If
	// this write fails we must NOT persist the Deployment record — doing so would
	// (a) report a provisioned subdomain that silently never routes, and (b) poison
	// the idempotency short-circuit above so a later retry returns the broken
	// deployment instead of re-attempting. (writeCaddySnippet is a no-op that
	// returns nil when caddyDir is unset/"noop", so dev/test behaviour is
	// unchanged.) Surface the error so the caller can retry.
	if err := p.writeCaddySnippet(appID, profile, fqdn, upstreamAddr); err != nil {
		return nil, fmt.Errorf("provision subdomain for %s: write proxy config: %w", appID, err)
	}

	d := &Deployment{
		AppID:       appID,
		Profile:     profile,
		FQDN:        fqdn,
		Provisioned: time.Now().UTC(),
		TLSObtained: false, // ACME is async; set to true by a future cert-check hook
	}
	if err := p.store.Set(d); err != nil {
		return nil, fmt.Errorf("provision subdomain: persist: %w", err)
	}

	return d, nil
}

// DeprovisionSubdomain removes the subdomain reservation and cleans up config.
// Safe to call even if no deployment exists.
func (p *Provisioner) DeprovisionSubdomain(appID, profile string) error {
	if profile == "" {
		profile = "default"
	}

	if err := p.removeCaddySnippet(appID, profile); err != nil {
		return err
	}

	return p.store.Delete(appID, profile)
}
