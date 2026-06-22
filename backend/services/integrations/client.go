// Package integrations is the Vulos OS box-side consumer of the cloud OAuth
// broker (INTEG-03).
//
// The cloud control plane (vulos-cloud) custodies the long-lived Google refresh
// token and the client secret. A box NEVER holds those: it mints a short-lived
// access token on demand over the device-HMAC channel — the same trust path as
// fleet heartbeats (DEVICE_SHARED_SECRET + the box ULID, resolved to the owning
// account via the CP routing binding).
//
// The box presents:
//
//	GET  {VULOS_CLOUD_URL}/api/integrations/{provider}/token
//	X-Device-ULID:     <box ULID>
//	X-Integration-Sig: hex(HMAC-SHA256(DEVICE_SHARED_SECRET,
//	                       "integrations:token:" + provider + ":" + ulid))
//
// and receives a short-lived access token, which it caches in memory. Tokens
// are short-lived and never persisted to disk; the broker is the source of
// truth. Behaviour is fail-closed: no cloud reachability and no fresh cached
// token ⇒ error, never a stale token.
package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// DefaultCloudBaseURL mirrors multiinstance.DefaultCloudBaseURL.
const DefaultCloudBaseURL = "https://api.vulos.org"

// ProviderGoogle is the only provider in Wave 1.
const ProviderGoogle = "google"

// cacheSkew refreshes a cached token slightly before its real expiry.
const cacheSkew = 60 * time.Second

// notConnectedTTL bounds how long a "not connected" result is cached, so the
// gateway (which probes on every request to a permitted app) does not round-trip
// to the broker on every request when the account has no connection.
const notConnectedTTL = 60 * time.Second

// Sentinel errors.
var (
	ErrNotConfigured = errors.New("integrations: box not provisioned for cloud integrations (set VULOS_DEVICE_ULID + DEVICE_SHARED_SECRET)")
	ErrNotConnected  = errors.New("integrations: account has not connected this provider")
	ErrCloudUnavail  = errors.New("integrations: cloud broker unavailable")
)

// Token is a short-lived access token minted by the cloud broker.
type Token struct {
	AccessToken string    `json:"access_token"`
	Expiry      time.Time `json:"expires_at"`
	Scopes      string    `json:"scopes"`
}

// Client mints + caches short-lived access tokens from the cloud broker.
// Safe for concurrent use.
type Client struct {
	cloudBaseURL string
	deviceULID   string
	deviceSecret string
	httpClient   *http.Client

	mu           sync.Mutex
	cache        map[string]Token     // provider → cached access token
	notConnected map[string]time.Time // provider → time until which "not connected" holds
	now          func() time.Time
}

// NewClientFromEnv builds a Client from the box's provisioned environment:
//
//	VULOS_CLOUD_URL       — broker base URL (default https://api.vulos.org)
//	VULOS_DEVICE_ULID     — this box's canonical ULID
//	DEVICE_SHARED_SECRET  — fleet device secret (same as heartbeat signing)
func NewClientFromEnv() *Client {
	base := os.Getenv("VULOS_CLOUD_URL")
	if base == "" {
		base = DefaultCloudBaseURL
	}
	return &Client{
		cloudBaseURL: base,
		deviceULID:   os.Getenv("VULOS_DEVICE_ULID"),
		deviceSecret: os.Getenv("DEVICE_SHARED_SECRET"),
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		cache:        make(map[string]Token),
		notConnected: make(map[string]time.Time),
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// Configured reports whether the box has the credentials to talk to the broker.
func (c *Client) Configured() bool {
	return c.deviceULID != "" && c.deviceSecret != ""
}

// sign computes the purpose-bound device signature for a token-mint request.
func (c *Client) sign(provider string) string {
	mac := hmac.New(sha256.New, []byte(c.deviceSecret))
	mac.Write([]byte("integrations:token:" + provider + ":" + c.deviceULID))
	return hex.EncodeToString(mac.Sum(nil))
}

// MintToken returns a valid short-lived access token for provider, serving a
// fresh cached token when possible and otherwise fetching a new one from the
// broker. Fail-closed: an unreachable broker with no fresh cache returns an
// error, never a stale token.
func (c *Client) MintToken(ctx context.Context, provider string) (Token, error) {
	if !c.Configured() {
		return Token{}, ErrNotConfigured
	}

	now := c.now()
	c.mu.Lock()
	if tok, ok := c.cache[provider]; ok && now.Add(cacheSkew).Before(tok.Expiry) {
		c.mu.Unlock()
		return tok, nil
	}
	// Short-circuit a recent "not connected" result to avoid hammering the
	// broker on every gateway-injected request.
	if until, ok := c.notConnected[provider]; ok && now.Before(until) {
		c.mu.Unlock()
		return Token{}, ErrNotConnected
	}
	c.mu.Unlock()

	tok, err := c.fetch(ctx, provider)
	if err != nil {
		if errors.Is(err, ErrNotConnected) {
			c.mu.Lock()
			c.notConnected[provider] = now.Add(notConnectedTTL)
			c.mu.Unlock()
		}
		return Token{}, err
	}

	c.mu.Lock()
	c.cache[provider] = tok
	delete(c.notConnected, provider) // a successful mint clears the negative cache
	c.mu.Unlock()
	return tok, nil
}

// fetch performs the broker round-trip (no caching).
func (c *Client) fetch(ctx context.Context, provider string) (Token, error) {
	url := c.cloudBaseURL + "/api/integrations/" + provider + "/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("X-Device-ULID", c.deviceULID)
	req.Header.Set("X-Integration-Sig", c.sign(provider))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %v", ErrCloudUnavail, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var tok Token
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok); err != nil {
			return Token{}, fmt.Errorf("integrations: decode token: %w", err)
		}
		if tok.AccessToken == "" {
			return Token{}, errors.New("integrations: broker returned empty access token")
		}
		return tok, nil
	case http.StatusNotFound:
		return Token{}, ErrNotConnected
	case http.StatusUnauthorized, http.StatusForbidden:
		return Token{}, fmt.Errorf("%w: device authentication rejected (status %d)", ErrCloudUnavail, resp.StatusCode)
	default:
		return Token{}, fmt.Errorf("%w: broker status %d", ErrCloudUnavail, resp.StatusCode)
	}
}

// Status reports whether the box is configured and whether the account has a
// usable connection for provider. A configured box probes the broker (the
// result is cached). connected=false with err=nil means "not connected".
func (c *Client) Status(ctx context.Context, provider string) (configured, connected bool, err error) {
	if !c.Configured() {
		return false, false, nil
	}
	_, err = c.MintToken(ctx, provider)
	switch {
	case err == nil:
		return true, true, nil
	case errors.Is(err, ErrNotConnected):
		return true, false, nil
	default:
		return true, false, err
	}
}
