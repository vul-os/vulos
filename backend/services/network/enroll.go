package network

// enroll.go — Mode B (Direct) enrollment and acme-dns management.
//
// Flow:
//  1. Detect public IP via an external echo service (VULOS_IP_ECHO_URL).
//  2. POST /api/enroll/direct {ulid, ip, email} to the Vulos control API
//     (VULOS_CONTROL_URL) and persist the returned acme-dns credentials
//     as VULOS_ACME_DNS_UUID / VULOS_ACME_DNS_KEY in the process environment.
//  3. Watch for public-IP changes; on change call PUT /api/dns/update.
//
// Ziti and TLS cert issuance are intentionally out of scope here.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Constants / defaults
// ---------------------------------------------------------------------------

const (
	defaultIPEchoURL = "https://api.ipify.org"
	// Vulos the org operates no hosted control plane; a Direct-mode enroller must
	// be pointed at one the operator runs via VULOS_CONTROL_URL. No hosted default.
	defaultControlURL   = ""
	defaultPollInterval = 5 * time.Minute
)

// ---------------------------------------------------------------------------
// Enroll request / response types
// ---------------------------------------------------------------------------

// DirectEnrollRequest is the body sent to POST /api/enroll/direct.
type DirectEnrollRequest struct {
	ULID  string `json:"ulid"`
	IP    string `json:"ip"`
	Email string `json:"email"`
}

// DirectEnrollResponse is the JSON response from POST /api/enroll/direct.
type DirectEnrollResponse struct {
	AcmeDNSUUID string `json:"acme_dns_uuid"`
	AcmeDNSKey  string `json:"acme_dns_key"`
}

// DNSUpdateRequest is the body sent to PUT /api/dns/update.
type DNSUpdateRequest struct {
	ULID string `json:"ulid"`
	IP   string `json:"ip"`
}

// AcmeDNSCredentials holds the credentials returned during enrollment.
type AcmeDNSCredentials struct {
	UUID string
	Key  string
}

// ---------------------------------------------------------------------------
// Enroller
// ---------------------------------------------------------------------------

// Enroller implements Mode B (Direct) enrollment and IP-change tracking.
// It is safe for concurrent use.
type Enroller struct {
	mu           sync.Mutex
	lastPublicIP string

	// configurable via options / env
	ipEchoURL    string
	controlURL   string
	pollInterval time.Duration
	httpClient   *http.Client
}

// EnrollerOption configures an Enroller.
type EnrollerOption func(*Enroller)

// WithIPEchoURL overrides the public-IP echo service URL.
func WithIPEchoURL(u string) EnrollerOption {
	return func(e *Enroller) { e.ipEchoURL = u }
}

// WithControlURL overrides the Vulos control API base URL.
func WithControlURL(u string) EnrollerOption {
	return func(e *Enroller) { e.controlURL = u }
}

// WithHTTPClient replaces the default HTTP client (useful in tests).
func WithHTTPClient(c *http.Client) EnrollerOption {
	return func(e *Enroller) { e.httpClient = c }
}

// WithPollInterval overrides the IP-change polling interval.
func WithPollInterval(d time.Duration) EnrollerOption {
	return func(e *Enroller) { e.pollInterval = d }
}

// NewEnroller creates an Enroller with defaults drawn from environment.
//
// Env vars read here (not in network.go):
//
//	VULOS_IP_ECHO_URL   — public-IP echo service (default: api.ipify.org)
//	VULOS_CONTROL_URL   — Vulos control API base (default: https://control.vulos.org)
func NewEnroller(opts ...EnrollerOption) *Enroller {
	e := &Enroller{
		ipEchoURL:    getenv("VULOS_IP_ECHO_URL", defaultIPEchoURL),
		controlURL:   getenv("VULOS_CONTROL_URL", defaultControlURL),
		pollInterval: defaultPollInterval,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// ---------------------------------------------------------------------------
// Public IP detection
// ---------------------------------------------------------------------------

// DetectPublicIP calls the configured echo service and returns the
// trimmed IP string.
func (e *Enroller) DetectPublicIP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ipEchoURL, nil)
	if err != nil {
		return "", fmt.Errorf("enroll: build ip-echo request: %w", err)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("enroll: ip-echo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enroll: ip-echo returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("enroll: read ip-echo body: %w", err)
	}
	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("enroll: ip-echo returned empty body")
	}
	return ip, nil
}

// ---------------------------------------------------------------------------
// Enrollment
// ---------------------------------------------------------------------------

// EnrollDirect POSTs to the control API's /api/enroll/direct endpoint and
// persists the returned acme-dns credentials in the process environment
// (VULOS_ACME_DNS_UUID, VULOS_ACME_DNS_KEY).
//
// Parameters:
//
//	ulid  — instance ULID (read from VULOS_INSTANCE_ID in normal operation)
//	ip    — public IP (from DetectPublicIP)
//	email — contact email for the Let's Encrypt registration
func (e *Enroller) EnrollDirect(ctx context.Context, ulid, ip, email string) (*AcmeDNSCredentials, error) {
	payload := DirectEnrollRequest{ULID: ulid, IP: ip, Email: email}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("enroll: marshal request: %w", err)
	}

	url := strings.TrimRight(e.controlURL, "/") + "/api/enroll/direct"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("enroll: build enroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enroll: enroll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("enroll: control API returned status %d", resp.StatusCode)
	}

	var result DirectEnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("enroll: decode enroll response: %w", err)
	}
	if result.AcmeDNSUUID == "" || result.AcmeDNSKey == "" {
		return nil, fmt.Errorf("enroll: control API returned empty acme-dns credentials")
	}

	creds := &AcmeDNSCredentials{UUID: result.AcmeDNSUUID, Key: result.AcmeDNSKey}
	if err := persistAcmeDNSCreds(creds); err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.lastPublicIP = ip
	e.mu.Unlock()

	log.Printf("[enroll] Mode B enrolled: ulid=%s ip=%s", ulid, ip)
	return creds, nil
}

// persistAcmeDNSCreds writes acme-dns credentials to the process environment
// so that downstream certbot hooks can read them without a process restart.
func persistAcmeDNSCreds(creds *AcmeDNSCredentials) error {
	if err := os.Setenv("VULOS_ACME_DNS_UUID", creds.UUID); err != nil {
		return fmt.Errorf("enroll: set VULOS_ACME_DNS_UUID: %w", err)
	}
	if err := os.Setenv("VULOS_ACME_DNS_KEY", creds.Key); err != nil {
		return fmt.Errorf("enroll: set VULOS_ACME_DNS_KEY: %w", err)
	}
	return nil
}

// LoadAcmeDNSCreds reads previously persisted credentials from the environment.
// Returns nil if the credentials are absent (not yet enrolled).
func LoadAcmeDNSCreds() *AcmeDNSCredentials {
	uuid := os.Getenv("VULOS_ACME_DNS_UUID")
	key := os.Getenv("VULOS_ACME_DNS_KEY")
	if uuid == "" || key == "" {
		return nil
	}
	return &AcmeDNSCredentials{UUID: uuid, Key: key}
}

// ---------------------------------------------------------------------------
// DNS update on IP change
// ---------------------------------------------------------------------------

// UpdateDNS calls PUT /api/dns/update to notify the Vulos control API that
// the server's public IP has changed. This keeps the A record for
// *.{ulid}.vulos.org pointing at the new address.
func (e *Enroller) UpdateDNS(ctx context.Context, ulid, newIP string) error {
	payload := DNSUpdateRequest{ULID: ulid, IP: newIP}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("enroll: marshal dns-update request: %w", err)
	}

	url := strings.TrimRight(e.controlURL, "/") + "/api/dns/update"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("enroll: build dns-update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("enroll: dns-update request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("enroll: dns-update returned status %d", resp.StatusCode)
	}

	e.mu.Lock()
	e.lastPublicIP = newIP
	e.mu.Unlock()

	log.Printf("[enroll] DNS updated: ulid=%s new_ip=%s", ulid, newIP)
	return nil
}

// ---------------------------------------------------------------------------
// IP-change watcher
// ---------------------------------------------------------------------------

// WatchIPChanges polls the echo service at the configured interval.
// When the public IP changes it calls UpdateDNS automatically.
// It blocks until ctx is cancelled.
//
// Intended to be run as a goroutine:
//
//	go enroller.WatchIPChanges(ctx, ulid)
func (e *Enroller) WatchIPChanges(ctx context.Context, ulid string) {
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ip, err := e.DetectPublicIP(ctx)
			if err != nil {
				log.Printf("[enroll] ip-check error: %v", err)
				continue
			}
			e.mu.Lock()
			last := e.lastPublicIP
			e.mu.Unlock()
			if ip != last {
				log.Printf("[enroll] public IP changed %s → %s", last, ip)
				if err := e.UpdateDNS(ctx, ulid, ip); err != nil {
					log.Printf("[enroll] dns-update error: %v", err)
				}
			}
		}
	}
}

// LastPublicIP returns the most recently detected public IP (empty if not yet detected).
func (e *Enroller) LastPublicIP() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastPublicIP
}
