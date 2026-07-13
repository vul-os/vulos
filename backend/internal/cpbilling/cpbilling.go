// Package cpbilling is the OS-side client for the Vulos cloud control-plane
// (cp) billing contract: entitlement checks, usage metering, and suspension
// enforcement for every billable OS surface (LLM, compute, relay, GPU, Meet).
//
// # SEAM PHILOSOPHY — INDEPENDENT BY DEFAULT
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
// # DEGRADE GRACEFULLY, BUT FAIL CLOSED ON THE SECURITY DECISION
//
// Entitlement lookups are cached with a short TTL. On a cp error we serve the
// last-known entitlement if we have one (EVEN IF ITS TTL LAPSED) — that stale
// cache is the availability valve, and it is what keeps a cp blip from downing
// the OS: every account the box has ever verified keeps working, and a KNOWN
// suspension stays authoritative (a cached suspended=true refuses).
//
// A COLD cache (this account/product was NEVER verified) plus a cp error is a
// different situation: we have no evidence of entitlement at all. Allowing there
// grants the paid, metered capability to an account that may be suspended, over
// budget, or not entitled — and a cold cache is trivially arranged (any fresh
// account/product pair, or a box simply kept away from cp). So the gate REFUSES
// (Reason "cp_unverified", Degraded=true) rather than fail open. The refusal is
// logged loudly and surfaces to the caller as a normal not-allowed decision, so
// the request path degrades (402/503-style refusal) instead of crashing.
//
// A box with no cp configured at all (self-host) is unaffected: Enabled() is
// false and every gate allows.
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
// superset of fields; unknown ones are ignored.
//
// CAP SEMANTICS — cp returns the *allowance* (cap), not the remaining balance.
// The OS can enforce hard-disabled (Enabled=false) and hard-zero
// (Budget/Cap <= 0) gates locally. Precise remaining-balance enforcement (e.g.
// "you have 3 GB of the 10 GB relay budget left this month") requires cp to
// return a *_remaining field that reflects metered consumption. Until cp adds
// those fields, any budget/cap that is > 0 is treated as "not exhausted" — the
// OS trusts cp to cut off the account at source (e.g. by flipping Enabled=false
// or Suspended=true). Gates that need *_remaining are documented inline.
type Entitlement struct {
	Tier      string `json:"tier"`
	Suspended bool   `json:"suspended"`

	// LLM caps
	LLMEnabled   bool    `json:"llm_enabled"`
	LLMBudgetUSD float64 `json:"llm_budget_usd"`

	// Relay caps. RelayBytesBudget is the monthly volume cap in bytes.
	// NOTE: precise relay_bytes volume enforcement requires cp to return
	// relay_bytes_remaining (the un-metered balance); until then the OS refuses
	// only when RelayEnabled=false, Suspended=true, or RelayBytesBudget=0.
	RelayEnabled     bool  `json:"relay_enabled"`
	RelayBytesBudget int64 `json:"relay_bytes_budget"`

	// GPU caps. GPUSessionCap is the maximum number of *concurrent* GPU/stream
	// sessions permitted. The OS tracks active sessions locally via the stream
	// pool (see GateGPU) and refuses a launch when the count ≥ cap.
	GPUEnabled    bool `json:"gpu_enabled"`
	GPUSessionCap int  `json:"gpu_session_cap"`

	// Meet caps. MeetMinutesBudget is the monthly meeting-minutes allowance.
	// NOTE: precise meeting-minutes volume enforcement requires cp to return
	// meet_minutes_remaining; until then the OS refuses only when
	// MeetEnabled=false, Suspended=true, MeetMaxRooms=0, or
	// MeetMinutesBudget=0. MeetMaxRooms is the concurrent-room cap enforced
	// locally against SFU.RoomCount().
	MeetEnabled       bool `json:"meet_enabled"`
	MeetMinutesBudget int  `json:"meet_minutes_budget"`
	MeetMaxRooms      int  `json:"meet_max_rooms"`

	// Compute caps. ComputeBoxCap is the maximum number of provisioned cloud
	// instances. The OS enforces this locally against Registry.List() before
	// calling the cloud provision endpoint. ComputeStorageGB is informational
	// and exposed via GET /api/entitlements; block-level storage enforcement
	// lives in the cloud control plane, not the OS.
	ComputeEnabled   bool `json:"compute_enabled"`
	ComputeBoxCap    int  `json:"compute_box_cap"`
	ComputeStorageGB int  `json:"compute_storage_gb"`
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
	enabled := base != ""
	// Refuse plaintext: the CP_SHARED_SECRET bearer travels in the X-Relay-Auth
	// header on every connect-back, so a plaintext http base would let a network
	// attacker harvest it. Fail CLOSED (disable the client) unless the explicit
	// insecure escape hatch is set, mirroring the lancert puller (lan/lancert_puller.go).
	if enabled && !cpAllowInsecure() {
		if u, perr := url.Parse(base); perr != nil || u.Scheme != "https" {
			scheme := "<unparseable>"
			if perr == nil {
				scheme = u.Scheme
			}
			log.Printf("[cpbilling] WARNING: CP base URL must be https (got scheme %q) — "+
				"billing DISABLED to avoid leaking CP_SHARED_SECRET over plaintext; "+
				"set VULOS_CP_ALLOW_INSECURE=1 only for local dev", scheme)
			enabled = false
		}
	}
	return &Client{
		enabled: enabled,
		base:    base,
		secret:  secret,
		hc:      hc,
		ttl:     ttl,
		now:     nowFn,
		cache:   make(map[string]cacheEntry),
	}
}

// cpAllowInsecure reports whether the plaintext-http escape hatch is enabled
// (VULOS_CP_ALLOW_INSECURE=1|true|yes). For local dev only.
func cpAllowInsecure() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_CP_ALLOW_INSECURE"))) {
	case "1", "true", "yes":
		return true
	}
	return false
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
	// Degraded is true when the decision was made without a usable entitlement
	// because cp was unreachable on a COLD cache. Such a decision is NOT allowed
	// (see [Gate]); the flag lets a caller distinguish "cp is down" from a
	// substantive refusal like suspension, and surface a different message.
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
// It honors suspension (refuse when known-suspended) and REFUSES on a cold cp
// outage — see the package doc: a stale cached entitlement is served happily (so
// a cp blip never downs a verified account), but an account/product we have
// NEVER verified is not handed a paid, metered capability on the strength of a
// failed lookup. Product-specific caps (e.g. LLM budget) are layered on by
// [GateLLM]; for surfaces where cp returns no caps this still enforces
// suspension + presence.
func (c *Client) Gate(ctx context.Context, accountID, product string) Decision {
	if !c.Enabled() {
		return Decision{Allowed: true, Reason: "disabled"}
	}
	ent, err := c.Entitlement(ctx, accountID, product)
	if err != nil {
		// Cold cache + cp error: no evidence of entitlement — fail CLOSED. Logged
		// loudly (this is an outage signal, not a routine refusal). Degraded flags
		// it so a caller can tell "cp is down" from "you are suspended".
		log.Printf("[cpbilling] gate %s/%s: cold-cache cp error %v — REFUSING (unverified; cp unreachable and no last-known entitlement)",
			product, accountID, err)
		return Decision{Allowed: false, Reason: "cp_unverified", Degraded: true}
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
	if !d.Allowed || d.Reason == "disabled" {
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

// GateGPU is Gate plus GPU product caps.
//
// Enforced caps:
//   - gpu_enabled=false → refuse ("gpu_disabled")
//   - suspended → refuse ("suspended")
//   - activeSessions >= gpu_session_cap (when cap > 0) → refuse ("gpu_session_cap_reached")
//     activeSessions is the caller-supplied count of currently running GPU/stream
//     sessions for this account, tracked locally in the stream pool. The OS uses
//     local counting because the cp entitlements response contains the cap but not
//     the live session count; querying cp on every launch would add latency and a
//     single-process in-memory counter is authoritative for the local OS.
//
// ENFORCEMENT STATUS:
//   - gpu_enabled + suspended: ENFORCED (cp authoritative)
//   - gpu_session_cap concurrent cap: ENFORCED locally (activeSessions arg)
func (c *Client) GateGPU(ctx context.Context, accountID string, activeSessions int) Decision {
	d := c.Gate(ctx, accountID, ProductGPU)
	if !d.Allowed || d.Reason == "disabled" {
		return d
	}
	ent := d.Entitlement
	if !ent.GPUEnabled {
		return Decision{Allowed: false, Reason: "gpu_disabled", Entitlement: ent}
	}
	if ent.GPUSessionCap > 0 && activeSessions >= ent.GPUSessionCap {
		return Decision{Allowed: false, Reason: "gpu_session_cap_reached", Entitlement: ent}
	}
	return d
}

// GateCompute is Gate plus compute product caps.
//
// Enforced caps:
//   - compute_enabled=false → refuse ("compute_disabled")
//   - suspended → refuse ("suspended")
//   - activeBoxes >= compute_box_cap (when cap > 0) → refuse ("compute_box_cap_reached")
//     activeBoxes is the caller-supplied count of provisioned instances,
//     obtained from Registry.List() before calling the cloud endpoint.
//
// ENFORCEMENT STATUS:
//   - compute_enabled + suspended: ENFORCED (cp authoritative)
//   - compute_box_cap: ENFORCED locally (activeBoxes arg)
//   - compute_storage_gb: NOT enforced by OS — storage limits live in the cloud
//     control plane (it owns the Fly API token and enforces disk quotas).
func (c *Client) GateCompute(ctx context.Context, accountID string, activeBoxes int) Decision {
	d := c.Gate(ctx, accountID, ProductCompute)
	if !d.Allowed || d.Reason == "disabled" {
		return d
	}
	ent := d.Entitlement
	if !ent.ComputeEnabled {
		return Decision{Allowed: false, Reason: "compute_disabled", Entitlement: ent}
	}
	if ent.ComputeBoxCap > 0 && activeBoxes >= ent.ComputeBoxCap {
		return Decision{Allowed: false, Reason: "compute_box_cap_reached", Entitlement: ent}
	}
	return d
}

// GateRelay is Gate plus relay product caps.
//
// Enforced caps:
//   - relay_enabled=false → refuse ("relay_disabled")
//   - suspended → refuse ("suspended")
//   - relay_bytes_budget <= 0 → refuse ("relay_budget_exhausted")
//
// ENFORCEMENT STATUS:
//   - relay_enabled + suspended: ENFORCED (cp authoritative)
//   - relay_bytes_budget=0: ENFORCED (hard-zero = no budget at all)
//   - relay_bytes_budget > 0 (remaining volume): NOT precisely enforced — cp
//     returns the monthly cap, not the remaining balance. Precise per-account
//     relay volume enforcement requires cp to expose relay_bytes_remaining
//     (metered balance). Until then the OS refuses only on disabled/budget=0;
//     cp must enforce the running balance at source (e.g. flip relay_enabled=false
//     or suspended=true when the budget is exhausted).
func (c *Client) GateRelay(ctx context.Context, accountID string) Decision {
	d := c.Gate(ctx, accountID, ProductRelay)
	if !d.Allowed || d.Reason == "disabled" {
		return d
	}
	ent := d.Entitlement
	if !ent.RelayEnabled {
		return Decision{Allowed: false, Reason: "relay_disabled", Entitlement: ent}
	}
	if ent.RelayBytesBudget <= 0 {
		return Decision{Allowed: false, Reason: "relay_budget_exhausted", Entitlement: ent}
	}
	return d
}

// GateMeet is Gate plus Meet product caps.
//
// Enforced caps:
//   - meet_enabled=false → refuse ("meet_disabled")
//   - suspended → refuse ("suspended")
//   - meet_minutes_budget <= 0 → refuse ("meet_budget_exhausted")
//   - activeRooms >= meet_max_rooms (when cap > 0) → refuse ("meet_room_cap_reached")
//     activeRooms is the caller-supplied count from SFU.RoomCount(), tracked
//     in-process as the authoritative concurrent-room count.
//
// ENFORCEMENT STATUS:
//   - meet_enabled + suspended: ENFORCED (cp authoritative)
//   - meet_max_rooms concurrent cap: ENFORCED locally (activeRooms arg)
//   - meet_minutes_budget=0: ENFORCED (hard-zero = no budget at all)
//   - meet_minutes_budget > 0 (remaining minutes): NOT precisely enforced — cp
//     returns the monthly cap, not the remaining balance. Precise meeting-minute
//     enforcement requires cp to expose meet_minutes_remaining. Until then cp
//     must enforce the running balance at source.
func (c *Client) GateMeet(ctx context.Context, accountID string, activeRooms int) Decision {
	d := c.Gate(ctx, accountID, ProductMeet)
	if !d.Allowed || d.Reason == "disabled" {
		return d
	}
	ent := d.Entitlement
	if !ent.MeetEnabled {
		return Decision{Allowed: false, Reason: "meet_disabled", Entitlement: ent}
	}
	if ent.MeetMinutesBudget <= 0 {
		return Decision{Allowed: false, Reason: "meet_budget_exhausted", Entitlement: ent}
	}
	if ent.MeetMaxRooms > 0 && activeRooms >= ent.MeetMaxRooms {
		return Decision{Allowed: false, Reason: "meet_room_cap_reached", Entitlement: ent}
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
