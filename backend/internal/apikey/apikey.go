// Package apikey implements the Vulos API-key introspection seam used by the
// OS API to authenticate `Authorization: Bearer vk_…` credentials.
//
// Introspection is brokered by the vulos-cloud control plane ("CP"). The OS (and
// every other Vulos product) presents the opaque key to the SAME endpoint and
// gets back the resolved account + scopes + products:
//
//	POST {CP}/api/keys/introspect
//	  Headers: Content-Type: application/json
//	           X-Relay-Auth: <VULOS_CP_TOKEN>      (service auth, optional)
//	  Body:    {"key": "vk_live_…"}
//	  200  →   {"valid": true,
//	            "account": "alice@vulos.org",
//	            "scopes":  ["files.read","files.write"],
//	            "products":["os","mail"]}
//	  200  →   {"valid": false}                     (unknown/revoked/expired key)
//
// Results are cached in-process for cacheTTL (~60s) so a burst of API calls does
// not hammer the CP and a single key is introspected at most once per minute.
//
// This package imports nothing from the rest of the OS: it is the wire seam, so
// the CP side (and other products) can implement the identical contract.
package apikey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// KeyPrefix is the required prefix of a Vulos API key when presented as a
	// Bearer credential. A token without this prefix is treated as a session token.
	KeyPrefix = "vk_"

	// ProductOS is the product scope a key MUST carry to use the OS /api/* endpoints.
	// The CP returns the products a key is entitled to in `products`.
	ProductOS = "os"

	// EnvCPBaseURL points at the control-plane base URL (e.g. https://cp.vulos.to).
	// When unset the key path is disabled and the OS falls back to session-only
	// auth (self-host unchanged). This is the SAME env the cloud billing seam uses.
	EnvCPBaseURL = "VULOS_CP_BASE_URL"

	// EnvCPToken is the service token the OS presents to the CP on the
	// introspection call. Optional (the CP may authorize by network instead).
	EnvCPToken = "VULOS_CP_TOKEN"

	// HeaderRelayAuth is the shared CP service-auth header (matches the contract
	// used across vulos products; the secret is VULOS_CP_TOKEN).
	HeaderRelayAuth = "X-Relay-Auth"

	// cacheTTL bounds how long an introspection result is trusted before the CP
	// is asked again. ~60s: short enough that a revoked key stops working
	// promptly, long enough to absorb a burst of API calls.
	cacheTTL = 60 * time.Second
)

// Result is the control-plane's response to a key introspection.
type Result struct {
	Valid    bool     `json:"valid"`
	Account  string   `json:"account"`
	Scopes   []string `json:"scopes"`
	Products []string `json:"products"`
}

// HasProduct reports whether the key is entitled to product p.
func (r Result) HasProduct(p string) bool {
	for _, x := range r.Products {
		if x == p {
			return true
		}
	}
	return false
}

// HasScope reports whether the key carries scope s.
func (r Result) HasScope(s string) bool {
	for _, x := range r.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// Introspector validates an opaque API key and returns the resolved identity.
// A non-nil error means the validation could not be completed (CP unreachable);
// callers should fail closed (HTTP 401/503) rather than grant access.
type Introspector interface {
	Introspect(ctx context.Context, key string) (Result, error)
}

// Config holds the resolved introspection-seam settings.
type Config struct {
	BaseURL string
	Token   string
}

// FromEnv reads the introspection config from the environment.
func FromEnv() Config {
	return Config{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv(EnvCPBaseURL)), "/"),
		Token:   strings.TrimSpace(os.Getenv(EnvCPToken)),
	}
}

// Enabled reports whether the key path is configured (a CP base URL is set).
// When false the caller must fall back to session-only auth.
func (c Config) Enabled() bool { return strings.TrimSpace(c.BaseURL) != "" }

// cpIntrospector is the control-plane-backed Introspector with a bounded cache.
type cpIntrospector struct {
	cfg  Config
	http *http.Client
	now  func() time.Time // injectable clock (tests)

	mu    sync.Mutex
	cache map[string]cachedResult
}

type cachedResult struct {
	res     Result
	fetched time.Time
}

// EnvCPAllowInsecure is the opt-in escape hatch for local dev / test
// environments. When set to "1", "true", or "yes", the apikey introspector
// accepts an http:// CP base URL. In production this must NEVER be set.
const EnvCPAllowInsecure = "VULOS_CP_ALLOW_INSECURE"

// cpAllowInsecure reports whether the plaintext-http escape hatch is enabled.
func cpAllowInsecure() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCPAllowInsecure))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// NewIntrospector builds a control-plane-backed Introspector from cfg. Returns
// nil when cfg is not Enabled() so the caller can detect the session-only path.
//
// M3 fix: rejects plaintext http:// base URLs to prevent leaking the
// X-Relay-Auth (VULOS_CP_TOKEN) bearer over an unencrypted connection.
// Only https:// is accepted; set VULOS_CP_ALLOW_INSECURE=1 for local dev.
func NewIntrospector(cfg Config) Introspector {
	if !cfg.Enabled() {
		return nil
	}
	if !cpAllowInsecure() {
		u, parseErr := url.Parse(cfg.BaseURL)
		if parseErr != nil || u.Scheme != "https" {
			scheme := "<unparseable>"
			if parseErr == nil {
				scheme = u.Scheme
			}
			log.Printf("[apikey] WARNING: VULOS_CP_BASE_URL scheme is %q — "+
				"API-key introspection DISABLED to avoid leaking X-Relay-Auth over plaintext; "+
				"set VULOS_CP_ALLOW_INSECURE=1 only for local dev (M3)", scheme)
			return nil
		}
	}
	return NewIntrospectorWithClient(cfg, &http.Client{Timeout: 5 * time.Second})
}

// NewIntrospectorWithClient builds an introspector over a caller-supplied HTTP
// client (tests point this at an httptest server).
func NewIntrospectorWithClient(cfg Config, hc *http.Client) *cpIntrospector {
	return &cpIntrospector{
		cfg:   cfg,
		http:  hc,
		now:   time.Now,
		cache: make(map[string]cachedResult),
	}
}

// cacheKey hashes the raw key so the in-memory cache never retains secrets in
// the clear.
func cacheKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (c *cpIntrospector) fromCache(key string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[cacheKey(key)]
	if !ok || c.now().Sub(e.fetched) >= cacheTTL {
		return Result{}, false
	}
	return e.res, true
}

func (c *cpIntrospector) remember(key string, res Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[cacheKey(key)] = cachedResult{res: res, fetched: c.now()}
}

// introspectRequest is the wire body POSTed to the CP.
type introspectRequest struct {
	Key string `json:"key"`
}

// Introspect resolves key against the control plane, consulting the ~60s cache
// first. Both valid AND invalid results are cached so a hot invalid key does not
// hammer the CP. A transport/non-200 error is returned (caller fails closed).
func (c *cpIntrospector) Introspect(ctx context.Context, key string) (Result, error) {
	if r, ok := c.fromCache(key); ok {
		return r, nil
	}

	raw, err := json.Marshal(introspectRequest{Key: key})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/api/keys/introspect", bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set(HeaderRelayAuth, c.cfg.Token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("apikey introspect: status %d", resp.StatusCode)
	}

	var res Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return Result{}, err
	}
	c.remember(key, res)
	return res, nil
}
