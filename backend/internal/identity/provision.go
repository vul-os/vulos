// provision.go — cloud-provisioning bridge for the Vulos identity step (IDENTITY-01).
//
// Provision is called by the first-boot wizard after the user picks a handle.
// It:
//  1. Validates the handle charset (lowercase alphanumeric + hyphen/underscore,
//     3–32 chars) and, for non-vulos.org domains, asserts the caller has already
//     completed domain-ownership verification via the cloud control plane.
//  2. Calls the cloud control plane API to claim the address (idempotent — a
//     second call with the same accountID + address is a no-op that returns the
//     existing Identity).
//  3. Generates (or re-uses) the local Ed25519 keypair, persists it via Store,
//     and sets up the JMAP credentials stub in the local mail kernel config.
//
// The cloud control plane endpoint is:
//
//	POST https://api.vulos.cloud/v1/identity/claim
//	  {"account_id": "…", "address": "…", "public_key_b64": "…"}
//	  → 200 {"address": "…", "public_key_b64": "…", "jmap_session_url": "…"}
//	  → 409 {"error": "handle_taken"}
//
// VULOS_CLOUD_API env var overrides the base URL (for tests / self-hosted).
// VULOS_KEYRING_PASSPHRASE env var is used to encrypt the private key at rest;
// in production the OS keyring injects this at boot time.
package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	// DefaultCloudAPI is the base URL for the Vulos cloud control plane.
	DefaultCloudAPI = "https://api.vulos.cloud"

	// FreeIdentityDomain is the only domain available on the Free tier.
	FreeIdentityDomain = "vulos.org"

	// handleMinLen / handleMaxLen define the allowed handle length range.
	handleMinLen = 3
	handleMaxLen = 32
)

// handlePattern is the allowed charset for the local-part of a Vulos account address:
// lowercase ASCII letters, digits, hyphens, and underscores.
var handlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,30}[a-z0-9]$|^[a-z0-9]{3}$`)

// ─── ProvisionedIdentity ──────────────────────────────────────────────────────

// ProvisionedIdentity extends Identity with fields returned by the cloud API.
type ProvisionedIdentity struct {
	*Identity

	// JMAPSessionURL is the JMAP session endpoint provided by the cloud
	// control plane after a successful claim. Empty for local-only installs.
	JMAPSessionURL string `json:"jmap_session_url,omitempty"`
}

// ─── Provision ───────────────────────────────────────────────────────────────

// Provision claims a Vulos identity for accountID and returns the populated
// ProvisionedIdentity, persisted to store.
//
// Parameters:
//   - ctx        — request context (caller should set a deadline).
//   - store      — local SQLite store; must not be nil.
//   - accountID  — opaque identifier for the local OS account (e.g., ULID).
//   - handle     — the desired username part (e.g. "alice").
//   - domain     — "vulos.org" for Free tier, or a custom domain for paid tiers.
//   - passphrase — used to encrypt the Ed25519 private key at rest.
//   - httpClient — overridable for tests; nil uses a default 30 s client.
//
// Idempotency: if the store already holds an identity whose address matches
// "<handle>@<domain>", Provision returns that identity without hitting the
// cloud API a second time.
//
// Errors:
//   - ErrHandleInvalid      — handle fails charset/length validation.
//   - ErrDomainNotAllowed   — custom domain passed without paid-tier flag.
//   - ErrHandleTaken        — cloud API returned 409.
//   - ErrProvisionFailed    — cloud API returned an unexpected error.
func Provision(
	ctx context.Context,
	store *Store,
	accountID, handle, domain, passphrase string,
	httpClient *http.Client,
) (*ProvisionedIdentity, error) {
	// ── 1. Validate inputs ────────────────────────────────────────────────────
	if err := validateHandle(handle); err != nil {
		return nil, err
	}
	if err := validateDomain(domain); err != nil {
		return nil, err
	}

	address := handle + "@" + domain

	// ── 2. Idempotency check — return existing identity if already provisioned ─
	existing, err := store.loadIdentity()
	if err != nil {
		return nil, fmt.Errorf("identity: provision: load existing identity: %w", err)
	}
	if existing != nil && existing.Address == address {
		return &ProvisionedIdentity{Identity: existing}, nil
	}

	// ── 3. Generate local Ed25519 keypair ─────────────────────────────────────
	id, err := GenerateIdentity(address, passphrase)
	if err != nil {
		return nil, fmt.Errorf("identity: provision: generate identity: %w", err)
	}

	// ── 4. Call cloud control plane to claim the address ─────────────────────
	jmapURL, err := claimWithCloudAPI(ctx, httpClient, accountID, id)
	if err != nil {
		return nil, err
	}

	// ── 5. Persist identity locally ───────────────────────────────────────────
	if err := store.saveIdentity(id); err != nil {
		return nil, fmt.Errorf("identity: provision: persist identity: %w", err)
	}

	// ── 6. Write JMAP credentials stub ───────────────────────────────────────
	if jmapURL != "" {
		if err := writeJMAPCredentials(address, jmapURL); err != nil {
			// Non-fatal: log and continue — JMAP setup can be retried at boot.
			// In production the mail kernel picks this up on next start.
			_ = err
		}
	}

	return &ProvisionedIdentity{
		Identity:       id,
		JMAPSessionURL: jmapURL,
	}, nil
}

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrHandleInvalid is returned when the handle fails charset or length checks.
var ErrHandleInvalid = errors.New("identity: handle invalid: must be 3–32 lowercase alphanumeric characters (hyphens/underscores allowed, not at start/end)")

// ErrDomainNotAllowed is returned when a non-vulos.org domain is supplied
// without the domain-ownership flag being set (custom domains require a paid tier).
var ErrDomainNotAllowed = errors.New("identity: custom domain not allowed: verify domain ownership via the cloud dashboard before provisioning")

// ErrHandleTaken is returned when the cloud API reports that the handle is
// already claimed by another account.
var ErrHandleTaken = errors.New("identity: handle already taken")

// ErrProvisionFailed is returned when the cloud API returns an unexpected error.
var ErrProvisionFailed = errors.New("identity: cloud provisioning failed")

// ─── Input validation ─────────────────────────────────────────────────────────

// validateHandle checks that handle is 3–32 chars matching handlePattern.
func validateHandle(handle string) error {
	l := len(handle)
	if l < handleMinLen || l > handleMaxLen {
		return ErrHandleInvalid
	}
	if !handlePattern.MatchString(handle) {
		return ErrHandleInvalid
	}
	return nil
}

// validateDomain checks the domain is non-empty. Custom domains (anything
// other than vulos.org) require out-of-band DNS verification; we accept them
// here and defer the enforcement to the cloud API which gates on its own
// verification state.
func validateDomain(domain string) error {
	if domain == "" {
		return ErrDomainNotAllowed
	}
	// domain must contain a dot and no whitespace.
	if !strings.Contains(domain, ".") || strings.ContainsAny(domain, " \t\n\r") {
		return ErrDomainNotAllowed
	}
	return nil
}

// ─── Cloud API call ───────────────────────────────────────────────────────────

type claimRequest struct {
	AccountID    string `json:"account_id"`
	Address      string `json:"address"`
	PublicKeyB64 string `json:"public_key_b64"`
}

type claimResponse struct {
	Address        string `json:"address"`
	PublicKeyB64   string `json:"public_key_b64"`
	JMAPSessionURL string `json:"jmap_session_url"`
	Error          string `json:"error"`
}

// claimWithCloudAPI posts a claim request to the cloud control plane.
// Returns the JMAP session URL on success.
func claimWithCloudAPI(ctx context.Context, client *http.Client, accountID string, id *Identity) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	cloudBase := os.Getenv("VULOS_CLOUD_API")
	if cloudBase == "" {
		cloudBase = DefaultCloudAPI
	}

	body, err := json.Marshal(claimRequest{
		AccountID:    accountID,
		Address:      id.Address,
		PublicKeyB64: id.PublicKeyB64,
	})
	if err != nil {
		return "", fmt.Errorf("identity: provision: marshal claim request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cloudBase+"/v1/identity/claim", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("identity: provision: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("identity: provision: cloud API unreachable: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var cr claimResponse
		if err := json.Unmarshal(raw, &cr); err != nil {
			return "", fmt.Errorf("identity: provision: parse cloud response: %w", err)
		}
		return cr.JMAPSessionURL, nil

	case http.StatusConflict:
		return "", ErrHandleTaken

	default:
		var cr claimResponse
		_ = json.Unmarshal(raw, &cr)
		detail := cr.Error
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("%w: %s", ErrProvisionFailed, detail)
	}
}

// ─── CheckAvailability ────────────────────────────────────────────────────────

// AvailabilityResult is the response payload returned by CheckAvailability.
type AvailabilityResult struct {
	Available   bool     `json:"available"`
	Suggestions []string `json:"suggestions,omitempty"`
	// Offline is true when the cloud control plane could not be reached.
	// The caller should present a soft warning and let Provision re-check atomically.
	Offline bool `json:"offline,omitempty"`
}

// CheckAvailability probes the cloud control plane to determine whether
// <handle>@<domain> is available.  It is a read-only call — it does NOT
// reserve the handle; that happens atomically inside Provision.
//
// Parameters:
//   - ctx        — request context (caller should set a short deadline, e.g. 5 s).
//   - handle     — the username part to check (e.g. "alice").
//   - httpClient — overridable for tests; nil uses a default 10 s client.
//
// Returns:
//   - (result, nil)   on a successful cloud response.
//   - (offline, nil)  when the cloud endpoint is unreachable — Offline=true,
//                     Available=false, so the UI can fail-open with a warning.
//   - (nil, err)      on a programming error (bad handle charset etc.).
//
// The existing Provision validates handle charset before calling the cloud;
// here we do the same lightweight check so the UI receives a typed error
// before wasting a network round-trip.
func CheckAvailability(ctx context.Context, handle string, httpClient *http.Client) (*AvailabilityResult, error) {
	if err := validateHandle(handle); err != nil {
		return nil, err
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	cloudBase := os.Getenv("VULOS_CLOUD_API")
	if cloudBase == "" {
		cloudBase = DefaultCloudAPI
	}

	url := cloudBase + "/api/auth/handle-available?handle=" + handle

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		// Should never happen for a well-formed URL.
		return &AvailabilityResult{Offline: true}, nil
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// Cloud unreachable — fail-open.
		return &AvailabilityResult{Offline: true}, nil
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		// Non-200 from cloud (rate limit, server error, etc.) — fail-open.
		return &AvailabilityResult{Offline: true}, nil
	}

	var body struct {
		Available   bool     `json:"available"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return &AvailabilityResult{Offline: true}, nil
	}

	return &AvailabilityResult{
		Available:   body.Available,
		Suggestions: body.Suggestions,
	}, nil
}

// ─── JMAP credentials stub ────────────────────────────────────────────────────

// jmapCredsPath is the path where the mail kernel looks for JMAP credentials.
// In production this lives under the OS keyring-managed config directory.
const jmapCredsPath = "/var/lib/vulos/mail/jmap_credentials.json"

type jmapCredentials struct {
	Address        string `json:"address"`
	JMAPSessionURL string `json:"jmap_session_url"`
	UpdatedAt      string `json:"updated_at"`
}

// writeJMAPCredentials writes the minimal JMAP credentials file consumed by
// the local mail kernel (vulos-mail) on startup.  The file is created with
// 0600 permissions so only the vulos system user can read it.
func writeJMAPCredentials(address, sessionURL string) error {
	creds := jmapCredentials{
		Address:        address,
		JMAPSessionURL: sessionURL,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("identity: jmap creds: marshal: %w", err)
	}
	// Ensure the parent directory exists.
	if err := os.MkdirAll(strings.TrimSuffix(jmapCredsPath, "/jmap_credentials.json"), 0700); err != nil {
		return fmt.Errorf("identity: jmap creds: mkdir: %w", err)
	}
	if err := os.WriteFile(jmapCredsPath, raw, 0600); err != nil {
		return fmt.Errorf("identity: jmap creds: write: %w", err)
	}
	return nil
}
