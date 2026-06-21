// Package cpbilling is the OS-side client for the Vulos cloud control-plane
// (cp) billing contract: entitlement checks, usage metering, and suspension
// enforcement for every billable OS surface (LLM, compute, relay, GPU, Meet).
//
// SEAM PHILOSOPHY — INDEPENDENT BY DEFAULT
//
// The whole package is a no-op unless CP_URL (or an explicitly supplied base
// URL) is configured. A standalone vulos OS — one with no cp wired — behaves
// exactly as before: [New] returns a client whose Enabled() is false, every
// gate Allows, and every meter is dropped. Core packages take a *Client and
// must tolerate a nil/disabled one; they never hard-depend on cp.
//
// CONTRACT (service-to-service, header X-Relay-Auth: <CP_SHARED_SECRET>)
//
//	GET  /api/entitlements?account_id=<email>&product=<llm|...>
//	       → 200 {tier, suspended, llm_enabled, llm_budget_usd, ...}
//	POST /api/usage  {product, account_id, kind, count, bytes, cost_usd}
//
// FAIL-OPEN, NOT FAIL-BLIND
//
// Entitlement lookups are cached with a short TTL. On a cp error we serve the
// last-known entitlement if we have one (even if its TTL lapsed); only on a
// COLD cache (never seen this account/product) do we allow-but-log "degraded".
// This avoids hard-downing the OS when cp blips, while still letting a KNOWN
// suspension stay authoritative: a cached suspended=true refuses.
package cpbilling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultCacheTTL is the freshness window for a cached entitlement before we
// re-fetch from cp. A stale-but-present entry is still used on cp error.
const DefaultCacheTTL = 30 * time.Second

// Product identifiers used on the wire. cp keys caps/usage by product.
const (
	ProductLLM     = "llm"
	ProductCompute = "compute"
	ProductRelay   = "relay"
	ProductGPU     = "gpu"
	ProductMeet    = "meet"
)

// Usage kinds used on the wire.
const (
	KindLLMTokens      = "llm_tokens"
	KindComputeMachine = "compute_machine"
	KindRelayBytes     = "relay_bytes"
	KindGPUSession     = "gpu_session"
	KindMeetMinutes    = "meet_minutes"
	KindMeetRoom       = "meet_room"
)

// Entitlement is the decoded GET /api/entitlements response. cp returns a
// superset of fields; unknown ones are ignored. For surfaces where cp does not
// yet return product-specific caps, only Tier and Suspended are meaningful.
type Entitlement struct {
	Tier        string  `json:"tier"`
	Suspended   bool    `json:"suspended"`
	LLMEnabled  bool    `json:"llm_enabled"`
	LLMBudgetUSD float64 `json:"llm_budget_usd"`
}

// UsageEvent is the POST /api/usage body. Zero-valued fields are still sent so
// cp can decide what to bill on; Count/Bytes/CostUSD are independent.
type UsageEvent struct {
	Product   string  `json:"product"`
	AccountID string  `json:"account_id"`
	Kind      string  `json:"kind"`
	Count     int64   `json:"count"`
	Bytes     int64   `json:"bytes"`
	CostUSD   float64 `json:"cost_usd"`
}

// Config configures a Client. Sensible defaults are filled by [New].
type Config struct {
	// BaseURL is the cp control-plane base (e.g. https://cp.vulos.org). When
	// empty, sourced from CP_URL. When still empty the client is DISABLED.
	BaseURL string
	// SharedSecret is sent as X-Relay-Auth on every call. Defaults to
	// CP_SHARED_SECRET.
	SharedSecret string
	// HTTPClient overrides the transport (test seam). Defaults to a 10s client.
	HTTPClient *http.Client
	// CacheTTL overrides DefaultCacheTTL.
	CacheTTL time.Duration
	// Now overrides the clock (test seam). Defaults to time.Now.
	Now func() time.Time
}

type cacheEntry struct {
	ent       Entitlement
	fetchedAt time.Time
}

// Client is the reusable cp billing client. It is safe for concurrent use.
// A disabled client (no BaseURL) allows everything and meters nothing — the
// standalone-OS path.
type Client struct {
	enabled bool
	base    string
	secret  string
	hc      *http.Client
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry // key: product\x00account_id
}

// New builds a Client from cfg, filling defaults from the environment. When no
// base URL is configured (cfg.BaseURL and CP_URL both empty) the returned
// client is disabled: a fully standalone OS. New never returns an error so
// callers can wire it unconditionally.
func New(cfg Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("CP_URL")), "/")
	}
	secret := cfg.SharedSecret
	if secret == "" {
		secret = os.Getenv("CP_SHARED_SECRET")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Client{
		enabled: base != "",
		base:    base,
		secret:  secret,
		hc:      hc,
		ttl:     ttl,
		now:     nowFn,
		cache:   make(map[string]cacheEntry),
	}
}

// Enabled reports whether cp billing is wired (CP_URL set). When false the
// client is a transparent no-op.
func (c *Client) Enabled() bool { return c != nil && c.enabled }

// Decision is the outcome of a gate check. Callers refuse iff Allowed is false.
type Decision struct {
	// Allowed is the final verdict.
	Allowed bool
	// Reason is a short machine-ish reason ("ok", "suspended", "llm_disabled",
	// "llm_budget_exhausted", "disabled", "degraded").
	Reason string
	// Degraded is true when we allowed on a cold-cache cp error (fail-open).
	Degraded bool
	// Entitlement is the entitlement the decision was based on (zero when the
	// client is disabled or the cache was cold and cp errored).
	Entitlement Entitlement
}

// Entitlement fetches (or serves from cache) the entitlement for account+product.
// Disabled client returns a zero entitlement. On cp error a stale cached value
// is returned with err==nil; a cold-cache error returns the zero entitlement
// and the error so the caller can choose fail-open.
func (c *Client) Entitlement(ctx context.Context, accountID, product string) (Entitlement, error) {
	if !c.Enabled() {
		return Entitlement{}, nil
	}
	key := product + "\x00" + accountID

	c.mu.Lock()
	entry, ok := c.cache[key]
	fresh := ok && c.now().Sub(entry.fetchedAt) < c.ttl
	c.mu.Unlock()
	if fresh {
		return entry.ent, nil
	}

	ent, err := c.fetchEntitlement(ctx, accountID, product)
	if err != nil {
		if ok {
			// Stale-but-present: serve last-known rather than failing.
			log.Printf("[cpbilling] entitlement %s/%s: cp error %v — using last-known (tier=%s suspended=%v)",
				product, accountID, err, entry.ent.Tier, entry.ent.Suspended)
			return entry.ent, nil
		}
		return Entitlement{}, err
	}

	c.mu.Lock()
	c.cache[key] = cacheEntry{ent: ent, fetchedAt: c.now()}
	c.mu.Unlock()
	return ent, nil
}

// Gate is the suspension-authoritative entitlement check used by every surface.
// It honors suspension (refuse when known-suspended) and is fail-open on a cold
// cp outage (allow-but-log "degraded"). Product-specific caps (e.g. LLM budget)
// are layered on by [GateLLM]; for surfaces where cp returns no caps this still
// enforces suspension + presence.
func (c *Client) Gate(ctx context.Context, accountID, product string) Decision {
	if !c.Enabled() {
		return Decision{Allowed: true, Reason: "disabled"}
	}
	ent, err := c.Entitlement(ctx, accountID, product)
	if err != nil {
		// Cold-cache cp error: fail-open but flag degraded so it's visible.
		log.Printf("[cpbilling] gate %s/%s: cold-cache cp error %v — allowing (degraded)", product, accountID, err)
		return Decision{Allowed: true, Reason: "degraded", Degraded: true}
	}
	if ent.Suspended {
		return Decision{Allowed: false, Reason: "suspended", Entitlement: ent}
	}
	return Decision{Allowed: true, Reason: "ok", Entitlement: ent}
}

// GateLLM is Gate plus the LLM product caps: refuse when llm_enabled is false
// or the budget is exhausted (<= 0 while a budget field is present). A degraded
// (cold-cache) decision is passed through allowed so a cp blip doesn't black out
// the LLM path.
func (c *Client) GateLLM(ctx context.Context, accountID string) Decision {
	d := c.Gate(ctx, accountID, ProductLLM)
	if !d.Allowed || d.Degraded || d.Reason == "disabled" {
		return d
	}
	ent := d.Entitlement
	if !ent.LLMEnabled {
		return Decision{Allowed: false, Reason: "llm_disabled", Entitlement: ent}
	}
	if ent.LLMBudgetUSD <= 0 {
		return Decision{Allowed: false, Reason: "llm_budget_exhausted", Entitlement: ent}
	}
	return d
}

// Meter sends a usage event to cp. It is best-effort and never returns an error
// to the hot path (failures are logged); call it after a resource is issued.
// A disabled client drops the event. Use [Client.MeterAsync] to fire-and-forget.
func (c *Client) Meter(ctx context.Context, ev UsageEvent) {
	if !c.Enabled() {
		return
	}
	if err := c.postUsage(ctx, ev); err != nil {
		log.Printf("[cpbilling] usage %s/%s kind=%s: %v (dropped)", ev.Product, ev.AccountID, ev.Kind, err)
	}
}

// MeterAsync reports usage on a detached goroutine with its own timeout so the
// caller's request path is never blocked on cp. Safe on a disabled client (it
// returns immediately without spawning).
func (c *Client) MeterAsync(ev UsageEvent) {
	if !c.Enabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.Meter(ctx, ev)
	}()
}

func (c *Client) fetchEntitlement(ctx context.Context, accountID, product string) (Entitlement, error) {
	u, err := url.Parse(c.base + "/api/entitlements")
	if err != nil {
		return Entitlement{}, err
	}
	q := u.Query()
	q.Set("account_id", accountID)
	q.Set("product", product)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Entitlement{}, err
	}
	req.Header.Set("X-Relay-Auth", c.secret)

	resp, err := c.hc.Do(req)
	if err != nil {
		return Entitlement{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Entitlement{}, fmt.Errorf("cp entitlements: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var ent Entitlement
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ent); err != nil {
		return Entitlement{}, fmt.Errorf("cp entitlements: decode: %w", err)
	}
	return ent, nil
}

func (c *Client) postUsage(ctx context.Context, ev UsageEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/usage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Auth", c.secret)

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cp usage: http %d", resp.StatusCode)
	}
	return nil
}
