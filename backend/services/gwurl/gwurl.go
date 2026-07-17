// Package gwurl is the SINGLE source of truth for the Vulos control-plane
// ("gateway") base URL that every box-side broker consumes: cloud login /
// signup / enroll, identity-username claim, instance routing (PlaceFor), cloud
// sync, and the OAuth integrations broker.
//
// # Why this exists
//
// Historically the CP base URL was read directly from a handful of environment
// variables (VULOS_CP_URL, VULOS_CLOUD_URL, VULOS_CLOUD_API_URL) scattered
// across packages, each with its own default. A self-hoster running their own
// OSS control plane (vulos-management) could only repoint the OS by setting
// every one of those vars before boot — brittle, and impossible to change from
// the running OS. This package unifies them behind one resolver and adds a
// PERSISTED, runtime-settable override so first-boot and Settings can point the
// OS at any gateway without touching the environment.
//
// # Resolution precedence (highest wins)
//
//  1. configured — a persisted override set via Set (the self-host affordance).
//  2. env        — the first non-empty of the canonical CP env vars.
//  3. default    — https://api.vulos.org (Vulos Cloud).
//
// Managed users never set an override, so they resolve to env (unset) → default
// and nothing changes. The override is purely additive.
//
// # Security
//
//   - Validate enforces a well-formed https:// URL with a host. Plaintext http
//     is rejected (the device bearer token rides these requests).
//   - CheckReachable dials through the shared SSRF-safe dialer (internal/
//     safedial): private/loopback/link-local/CGNAT targets are refused at dial
//     time (defeats DNS-rebinding), UNLESS the operator opts into LAN gateways
//     with VULOS_GATEWAY_ALLOW_PRIVATE=1 (a self-hoster running the CP on their
//     own network). The check is owner-initiated, so this is a scoping measure,
//     not the primary trust boundary — the SET endpoint's authz is.
package gwurl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/safedial"
)

// Default is the production Vulos Cloud control-plane base URL. It mirrors the
// per-package DefaultCloudBaseURL constants so behaviour is byte-identical when
// no override and no env var are present.
const Default = "https://api.vulos.org"

// Source describes where the effective gateway URL came from.
type Source string

const (
	// SourceDefault: no override and no env var — the built-in Vulos Cloud URL.
	SourceDefault Source = "default"
	// SourceEnv: a canonical CP env var supplied the URL.
	SourceEnv Source = "env"
	// SourceConfigured: a persisted runtime override (Set) supplied the URL.
	SourceConfigured Source = "configured"
)

// canonicalEnvVars are the historical CP base-URL environment variables, in
// precedence order. The first non-empty one wins when no override is set.
var canonicalEnvVars = []string{"VULOS_CP_URL", "VULOS_CLOUD_URL", "VULOS_CLOUD_API_URL"}

// allowPrivateEnv opts CheckReachable into private/LAN gateway hosts (for a
// self-hoster running the control plane on their own network).
const allowPrivateEnv = "VULOS_GATEWAY_ALLOW_PRIVATE"

var (
	mu          sync.RWMutex
	configured  string // persisted override; "" means none
	persistPath string // where the override is persisted; "" until Init

	// Test seams.
	envLookup = os.Getenv
	nowFn     = time.Now
	// checkReachable is the reachability probe. It is a var so tests can target a
	// loopback httptest server that the production SSRF guard would (correctly)
	// refuse; production always uses defaultCheckReachable.
	checkReachable = defaultCheckReachable
)

// persisted is the on-disk shape of the override.
type persisted struct {
	URL       string `json:"url"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Init points the resolver at dir for persistence and loads any previously
// configured override. Call once at startup. Idempotent. A missing file is not
// an error (no override yet). A corrupt file is logged by the caller via the
// returned error but leaves the resolver on env/default (fail-safe).
func Init(dir string) error {
	mu.Lock()
	defer mu.Unlock()
	persistPath = filepath.Join(dir, "gateway.json")
	raw, err := os.ReadFile(persistPath)
	if errors.Is(err, os.ErrNotExist) {
		configured = ""
		return nil
	}
	if err != nil {
		return fmt.Errorf("gwurl: read %s: %w", persistPath, err)
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("gwurl: parse %s: %w", persistPath, err)
	}
	// Persisted values were validated at Set time; normalise defensively.
	configured = normalize(p.URL)
	return nil
}

// normalize trims whitespace and a trailing slash so callers can concatenate
// "/api/..." safely.
func normalize(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

// envURL returns the first non-empty canonical CP env var (normalised), or "".
func envURL() string {
	for _, k := range canonicalEnvVars {
		if v := normalize(envLookup(k)); v != "" {
			return v
		}
	}
	return ""
}

// Resolved returns the effective gateway base URL (no trailing slash) and the
// source it came from.
func Resolved() (string, Source) {
	mu.RLock()
	c := configured
	mu.RUnlock()
	if c != "" {
		return c, SourceConfigured
	}
	if e := envURL(); e != "" {
		return e, SourceEnv
	}
	return Default, SourceDefault
}

// URL returns just the effective gateway base URL (no trailing slash). This is
// the accessor every CP broker calls.
func URL() string {
	u, _ := Resolved()
	return u
}

// Configured returns the current persisted override, or "" if none.
func Configured() string {
	mu.RLock()
	defer mu.RUnlock()
	return configured
}

// EnvURL returns the env-provided gateway URL, or "" — exposed for diagnostics
// (e.g. showing an operator which env var is in effect).
func EnvURL() string { return envURL() }

// allowPrivate reports whether LAN/private gateway hosts are permitted.
func allowPrivate() bool {
	switch strings.ToLower(strings.TrimSpace(envLookup(allowPrivateEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// Validate parses raw and enforces the security invariants: a well-formed
// absolute https:// URL with a host. It returns the normalised URL (scheme +
// host [+ port], no trailing slash, no path/query/fragment) on success.
func Validate(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("gateway URL is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("gateway URL is not valid: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("gateway URL must use https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("gateway URL must include a host")
	}
	if u.Hostname() == "" {
		return "", errors.New("gateway URL host is empty")
	}
	// Reject an embedded path/query/fragment — the base URL is a bare origin the
	// brokers concatenate "/api/..." onto. A stray path would corrupt every call.
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("gateway URL must be a bare origin (no path, query, or fragment)")
	}
	// Rebuild from the parsed parts so we return a clean canonical origin.
	return normalize((&url.URL{Scheme: u.Scheme, Host: u.Host}).String()), nil
}

// CheckReachable performs an owner-initiated reachability probe of a candidate
// gateway. It validates the URL, then GETs {url}/healthz through the SSRF-safe
// dialer. It is satisfied when the host answers HTTP with a status below 500
// (proving a live server + valid TLS); a 5xx or a transport error is a failure.
// Returns the normalised URL on success.
func CheckReachable(ctx context.Context, raw string) (string, error) {
	return checkReachable(ctx, raw)
}

// defaultCheckReachable is the production probe (see CheckReachable).
func defaultCheckReachable(ctx context.Context, raw string) (string, error) {
	clean, err := Validate(raw)
	if err != nil {
		return "", err
	}
	// Pre-validate the host so an obviously-private target gives a clear error
	// before we even dial (the dial-time Control hook is the hard guard).
	u, _ := url.Parse(clean)
	if _, verr := safedial.ValidateHost(u.Hostname(), allowPrivate()); verr != nil {
		return "", fmt.Errorf("gateway host %q is not reachable as a public gateway: %w", u.Hostname(), verr)
	}

	dialer := safedial.New(allowPrivate())
	dialer.Timeout = 8 * time.Second
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   8 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
		},
		// Do not silently follow redirects to a different (possibly private) host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clean+"/healthz", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("gateway responded with server error (status %d) — not a healthy control plane", resp.StatusCode)
	}
	return clean, nil
}

// Set validates raw, reachability-checks it, and persists it as the runtime
// override. All CP brokers that read URL() at request time pick it up
// immediately; startup-bound clients (integrations, enrollment) apply it on the
// next restart / re-enroll. Persisting fails closed: if the file cannot be
// written the override is NOT applied and the error is returned.
func Set(ctx context.Context, raw string) error {
	clean, err := checkReachable(ctx, raw)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if persistPath == "" {
		return errors.New("gwurl: not initialised (persist path unset)")
	}
	blob, _ := json.MarshalIndent(persisted{URL: clean, UpdatedAt: nowFn().UTC().Format(time.RFC3339)}, "", "  ")
	if err := writeFileAtomic(persistPath, blob, 0o600); err != nil {
		return fmt.Errorf("gwurl: persist gateway: %w", err)
	}
	configured = clean
	return nil
}

// Clear removes the persisted override, reverting to env/default. No-op (nil) if
// no override is set.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()
	configured = ""
	if persistPath == "" {
		return nil
	}
	if err := os.Remove(persistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gwurl: clear gateway: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in the same directory and renames
// it over path, so a crash mid-write never leaves a truncated gateway.json.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gateway-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
